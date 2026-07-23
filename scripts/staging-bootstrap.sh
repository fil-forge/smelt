#!/usr/bin/env bash
# One-time box bootstrap for the Forge staging deployment.
#
# Idempotent: clones/updates the repo on the box, creates the secrets + data
# directories, wires the Forge Caddy snippet into the host's main Caddyfile, and
# verifies the setup. Safe to re-run.
#
# Usage:
#   scripts/staging-bootstrap.sh
#
# Env overrides:
#   FORGE_HOST         ssh target           (default: root@23.83.66.244)
#   FORGE_DIR          on-box repo checkout (default: /root/fil-one/forge)
#   FORGE_SECRETS_DIR  host secrets dir     (default: /root/fil-one/forge/secrets)
#   FORGE_DATA_DIR     host data dir        (default: /mnt/data/fil-one/forge)
#   MAIN_CADDYFILE     host main Caddyfile  (default: /root/storacha/caddy/Caddyfile)
#   CADDY_SERVICE      systemd unit         (default: caddy-guppy)
#   REPO_URL           git URL to clone     (default: https://github.com/fil-forge/smelt.git)
#   FORGE_REF          branch/tag to deploy (default: main)
set -euo pipefail

FORGE_HOST="${FORGE_HOST:-root@23.83.66.244}"
FORGE_DIR="${FORGE_DIR:-/root/fil-one/forge}"
FORGE_SECRETS_DIR="${FORGE_SECRETS_DIR:-/root/fil-one/forge/secrets}"
FORGE_DATA_DIR="${FORGE_DATA_DIR:-/mnt/data/fil-one/forge}"
MAIN_CADDYFILE="${MAIN_CADDYFILE:-/root/storacha/caddy/Caddyfile}"
CADDY_SERVICE="${CADDY_SERVICE:-caddy-guppy}"
REPO_URL="${REPO_URL:-https://github.com/fil-forge/smelt.git}"
FORGE_REF="${FORGE_REF:-main}"

# Hosts expected to serve a did:web document. ingot is NOT here — it acts under
# a did:key and serves no did.json (checked separately via /health below).
HOSTS="sprue signing-service delegator piri-0 hilt"

echo "Bootstrapping Forge staging on $FORGE_HOST"
# Pass config as `export` statements prepended to the script bash reads from
# stdin, rather than as `VAR=val` args on the ssh command line. ssh runs the
# command line through the box's *login* shell (fish here), which doesn't grok
# POSIX inline-assignment prefixes and word-splits unquoted values; bash -s
# only ever sees a clean stdin stream. printf %q keeps values safely quoted.
{
  printf 'export FORGE_DIR=%q\n'        "$FORGE_DIR"
  printf 'export FORGE_SECRETS_DIR=%q\n' "$FORGE_SECRETS_DIR"
  printf 'export FORGE_DATA_DIR=%q\n'   "$FORGE_DATA_DIR"
  printf 'export MAIN_CADDYFILE=%q\n'   "$MAIN_CADDYFILE"
  printf 'export CADDY_SERVICE=%q\n'    "$CADDY_SERVICE"
  printf 'export REPO_URL=%q\n'         "$REPO_URL"
  printf 'export FORGE_REF=%q\n'        "$FORGE_REF"
  printf 'export HOSTS=%q\n'            "$HOSTS"
  cat <<'REMOTE'
set -euo pipefail

TOTAL=5
step() { printf '\n[%d/%d] %s\n' "$1" "$TOTAL" "$2"; }

echo "==> bootstrapping on $(hostname) ($(date -u '+%Y-%m-%dT%H:%M:%SZ'))"

# 1. Repo checkout (clone on first run, fast-forward thereafter), pinned to FORGE_REF.
if [ -d "$FORGE_DIR/.git" ]; then
  step 1 "updating repo at $FORGE_DIR ($FORGE_REF)"
  git -C "$FORGE_DIR" fetch --quiet --tags --force origin
  # FORGE_REF may be a branch, a tag, or a commit SHA. `git pull --ff-only` only
  # works on a branch with an upstream and fails outright on a tag/detached SHA, so
  # branch on the ref kind instead. For a branch, land on the freshly-fetched
  # origin tip (a stale local branch would otherwise stick). Tags and SHAs are
  # immutable — a detached checkout of what we just fetched is already correct.
  if git -C "$FORGE_DIR" show-ref --verify --quiet "refs/remotes/origin/$FORGE_REF"; then
    git -C "$FORGE_DIR" checkout --quiet -B "$FORGE_REF" "origin/$FORGE_REF"
  else
    git -C "$FORGE_DIR" checkout --quiet --detach "$FORGE_REF"
  fi
else
  [ -n "$REPO_URL" ] || { echo "ERROR: $FORGE_DIR has no checkout and REPO_URL is unset" >&2; exit 1; }
  step 1 "cloning $REPO_URL ($FORGE_REF) -> $FORGE_DIR"
  git clone --branch "$FORGE_REF" "$REPO_URL" "$FORGE_DIR"
fi
echo "    done: $FORGE_REF at $(git -C "$FORGE_DIR" rev-parse --short HEAD)"

# 2. Secrets + persistent data directories.
step 2 "creating secrets + data directories"
mkdir -p "$FORGE_SECRETS_DIR" && chmod 700 "$FORGE_SECRETS_DIR"
echo "    secrets: $FORGE_SECRETS_DIR"
for d in postgres minio dynamodb piri-0 piri-postgres ingot; do
  mkdir -p "$FORGE_DATA_DIR/$d"
  echo "    data:    $FORGE_DATA_DIR/$d"
done

# 3. Wire the Forge Caddy snippet into the host's main Caddyfile (idempotent).
step 3 "wiring Caddy"
mkdir -p "$FORGE_DIR/caddy"
cp "$FORGE_DIR/environments/staging/caddy/forge-staging.caddy" "$FORGE_DIR/caddy/"
IMPORT_LINE="import $FORGE_DIR/caddy/*.caddy"
if ! grep -qF "$IMPORT_LINE" "$MAIN_CADDYFILE"; then
  printf '\n%s\n' "$IMPORT_LINE" >> "$MAIN_CADDYFILE"
  echo "    added import to $MAIN_CADDYFILE"
else
  echo "    import already present in $MAIN_CADDYFILE"
fi
echo "    validating $MAIN_CADDYFILE"
caddy validate --config "$MAIN_CADDYFILE" --adapter caddyfile
echo "    reloading $CADDY_SERVICE"
systemctl reload "$CADDY_SERVICE"

# 4. Let containers reach the host's Caddy on :443 through UFW.
#    Containers resolve https://*.staging.fil.one to the box's public IP (real DNS)
#    and connect to it — but that traffic arrives at the host over the Docker bridge,
#    while the stock ':443 ALLOW' rule is scoped to the public NIC. UFW's default
#    deny-incoming then drops it, which is the i/o timeout piri hit on the registrar
#    call. This rule permits the Docker subnet to reach :443 regardless of arrival
#    interface, mirroring the existing ':1234 from 172.16.0.0/12' Lotus rule that
#    already lets containers reach the host. Idempotent: ufw skips a duplicate rule.
step 4 "allowing Docker containers -> host Caddy (:443) through UFW"
if command -v ufw >/dev/null 2>&1 && ufw status 2>/dev/null | grep -q '^Status: active'; then
  ufw allow from 172.16.0.0/12 to any port 443 proto tcp \
    comment 'forge containers -> host caddy https'
  echo "    ufw: 443/tcp from 172.16.0.0/12 allowed"
else
  echo "    WARN: ufw not active; ensure containers can reach the host on :443"
fi

# 5. Verify did:web resolution through Caddy. Warns (does not fail) — the services
#    must be deployed for these to return; run again after `make staging-deploy-*`.
step 5 "verifying did:web endpoints (warn-only; needs services deployed)"
for h in $HOSTS; do
  if curl -fsS "https://$h.staging.fil.one/.well-known/did.json" >/dev/null 2>&1; then
    echo "    ok:   https://$h.staging.fil.one/.well-known/did.json"
  else
    echo "    WARN: https://$h.staging.fil.one/.well-known/did.json not resolving yet"
  fi
done
# ingot serves no did.json (did:key identity) — check its health endpoint instead.
if curl -fsS "https://ingot.staging.fil.one/health" >/dev/null 2>&1; then
  echo "    ok:   https://ingot.staging.fil.one/health"
else
  echo "    WARN: https://ingot.staging.fil.one/health not resolving yet"
fi

printf '\n==> Bootstrap complete.\n'
REMOTE
} | ssh "$FORGE_HOST" bash -s
