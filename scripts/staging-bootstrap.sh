#!/usr/bin/env bash
# One-time box bootstrap for the Forge staging deployment.
#
# Idempotent: clones/updates the repo on the box, creates the secrets + data
# directories, wires the Forge Caddy snippet into the host's main Caddyfile, and
# verifies the setup. Safe to re-run.
#
# Usage:
#   REPO_URL=git@github.com:fil-forge/smelt.git scripts/staging-bootstrap.sh
#
# Env overrides:
#   FORGE_HOST         ssh target           (default: root@23.83.66.244)
#   FORGE_DIR          on-box repo checkout (default: /root/fil-one/forge)
#   FORGE_SECRETS_DIR  host secrets dir     (default: /root/fil-one/forge/secrets)
#   FORGE_DATA_DIR     host data dir        (default: /mnt/data/fil-one/forge)
#   MAIN_CADDYFILE     host main Caddyfile  (default: /root/storacha/caddy/Caddyfile)
#   CADDY_SERVICE      systemd unit         (default: caddy-guppy)
#   REPO_URL           git URL to clone     (required on first run)
set -euo pipefail

FORGE_HOST="${FORGE_HOST:-root@23.83.66.244}"
FORGE_DIR="${FORGE_DIR:-/root/fil-one/forge}"
FORGE_SECRETS_DIR="${FORGE_SECRETS_DIR:-/root/fil-one/forge/secrets}"
FORGE_DATA_DIR="${FORGE_DATA_DIR:-/mnt/data/fil-one/forge}"
MAIN_CADDYFILE="${MAIN_CADDYFILE:-/root/storacha/caddy/Caddyfile}"
CADDY_SERVICE="${CADDY_SERVICE:-caddy-guppy}"
REPO_URL="${REPO_URL:-}"

HOSTS="sprue signing-service delegator piri-0"

echo "Bootstrapping Forge staging on $FORGE_HOST"
ssh "$FORGE_HOST" \
  FORGE_DIR="$FORGE_DIR" \
  FORGE_SECRETS_DIR="$FORGE_SECRETS_DIR" \
  FORGE_DATA_DIR="$FORGE_DATA_DIR" \
  MAIN_CADDYFILE="$MAIN_CADDYFILE" \
  CADDY_SERVICE="$CADDY_SERVICE" \
  REPO_URL="$REPO_URL" \
  HOSTS="$HOSTS" \
  bash -s <<'REMOTE'
set -euo pipefail

# 1. Repo checkout (clone on first run, fast-forward thereafter).
if [ -d "$FORGE_DIR/.git" ]; then
  echo "==> updating $FORGE_DIR"
  git -C "$FORGE_DIR" pull --ff-only
else
  [ -n "$REPO_URL" ] || { echo "ERROR: $FORGE_DIR has no checkout and REPO_URL is unset" >&2; exit 1; }
  echo "==> cloning $REPO_URL -> $FORGE_DIR"
  git clone "$REPO_URL" "$FORGE_DIR"
fi

# 2. Secrets + persistent data directories.
echo "==> creating secrets + data directories"
mkdir -p "$FORGE_SECRETS_DIR" && chmod 700 "$FORGE_SECRETS_DIR"
for d in postgres minio dynamodb piri-0; do mkdir -p "$FORGE_DATA_DIR/$d"; done

# 3. Wire the Forge Caddy snippet into the host's main Caddyfile (idempotent).
echo "==> wiring Caddy"
mkdir -p "$FORGE_DIR/caddy"
cp "$FORGE_DIR/environments/staging/caddy/forge-staging.caddy" "$FORGE_DIR/caddy/"
IMPORT_LINE="import $FORGE_DIR/caddy/*.caddy"
if ! grep -qF "$IMPORT_LINE" "$MAIN_CADDYFILE"; then
  printf '\n%s\n' "$IMPORT_LINE" >> "$MAIN_CADDYFILE"
  echo "    added import to $MAIN_CADDYFILE"
fi
caddy validate --config "$MAIN_CADDYFILE" --adapter caddyfile
systemctl reload "$CADDY_SERVICE"

# 4. Verify did:web resolution through Caddy. Warns (does not fail) — the services
#    must be deployed for these to return; run again after `make staging-deploy-*`.
echo "==> verifying did:web endpoints (warn-only; needs services deployed)"
for h in $HOSTS; do
  if curl -fsS "https://$h.staging.fil.one/.well-known/did.json" >/dev/null 2>&1; then
    echo "    ok:   https://$h.staging.fil.one/.well-known/did.json"
  else
    echo "    WARN: https://$h.staging.fil.one/.well-known/did.json not resolving yet"
  fi
done
echo "Bootstrap complete."
REMOTE
