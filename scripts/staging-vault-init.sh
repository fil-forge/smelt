#!/usr/bin/env bash
# Initialize + unseal the staging hilt-vault, storing its keys in 1Password —
# DEVELOPER-MACHINE ONLY.
#
# WHY THIS EXISTS: staging's hilt-vault runs real (non-dev) HashiCorp Vault with
# integrated Raft storage. Unlike every other staging secret (minted offline by
# `make staging-keygen`), Vault's Shamir unseal key and root token are produced
# by `vault operator init` at RUNTIME inside the container — they cannot be
# generated ahead of time, and the box has no 1Password access. So this script,
# run from a developer machine with `op` signed in, boots Vault, initializes it
# when needed, captures the unseal key + root token, stores them in the shared
# 1Password item, and renders/ships vault-secrets.env to the box. The
# hilt-vault-unseal sidecar then keeps Vault unsealed across restarts.
#
# Run it AFTER `make staging-provision-core` (which wipes /vault/file) and BEFORE
# `make staging-deploy-core` (which consumes vault-secrets.env). Idempotent:
#   - Vault uninitialized (fresh box, or post-provision wipe) -> operator init,
#     store/overwrite the two 1Password fields, unseal, enable KV v2 at `secret`.
#   - Vault already initialized -> skip init; re-ship vault-secrets.env from the
#     existing 1Password fields; unseal if sealed.
#
# Secret handling matches the sibling staging scripts: values are read into
# memory, streamed over stdin, never written to local disk, never logged, and
# kept off argv (unseal key via `vault operator unseal -`, root token via
# `vault login -`). Do NOT enable `set -x`.
#
# Prerequisites on the dev machine:
#   - an authenticated 1Password session (`op signin`)
#   - `jq` on PATH
#   - SSH access to the box
#
# Usage:
#   scripts/staging-vault-init.sh
#
# Env overrides:
#   FORGE_HOST         ssh target           (default: root@23.83.66.244)
#   FORGE_DIR          on-box repo checkout (default: /root/fil-one/forge)
#   FORGE_SECRETS_DIR  host secrets dir     (default: /root/fil-one/forge/secrets)
#   FORGE_REF          branch/tag/sha to check out on the box (default: main)
#   OP_ITEM            1Password item ref   (default: op://Fil One/FilOne Forge Staging)
set -euo pipefail

FORGE_HOST="${FORGE_HOST:-root@23.83.66.244}"
FORGE_DIR="${FORGE_DIR:-/root/fil-one/forge}"
FORGE_SECRETS_DIR="${FORGE_SECRETS_DIR:-/root/fil-one/forge/secrets}"
FORGE_REF="${FORGE_REF:-main}"
OP_ITEM="${OP_ITEM:-op://Fil One/FilOne Forge Staging}"

# op read takes the full op://vault/item/field ref; op item get/edit take the
# vault and item title separately. Split OP_ITEM (op://<vault>/<item>) into both.
OP_REF="${OP_ITEM#op://}"
OP_VAULT="${OP_REF%%/*}"
OP_ITEM_TITLE="${OP_REF#*/}"

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
VAULT_TPL="$REPO_ROOT/environments/staging/core/vault-secrets.env.tpl"

CORE_DIR="$FORGE_DIR/environments/staging/core"
# Booting hilt-vault alone needs only VAULT_IMAGE (versions.env) + FORGE_DATA_DIR
# (config.env); the secret env files don't exist yet on a first init.
COMPOSE="docker compose -p forge-staging-core --env-file versions.env --env-file config.env"

# --- Preflight --------------------------------------------------------------
command -v op >/dev/null || { echo "ERROR: 1Password CLI 'op' not found in PATH" >&2; exit 1; }
command -v jq >/dev/null || { echo "ERROR: 'jq' not found in PATH" >&2; exit 1; }
op whoami >/dev/null 2>&1 || { echo "ERROR: not signed in to 1Password — run 'op signin'" >&2; exit 1; }
[ -f "$VAULT_TPL" ] || { echo "ERROR: $VAULT_TPL not found" >&2; exit 1; }

# Local scratch for secret material, removed on exit (0700).
TMP="$(mktemp -d "${TMPDIR:-/tmp}/forge-vault-init.XXXXXX")"
chmod 700 "$TMP"

# Reuse a single multiplexed SSH connection for every box call below (the box
# rate-limits new connections). Same pattern as staging-provision.sh.
SSH_CM_DIR="$(mktemp -d "${TMPDIR:-/tmp}/forge-vault-init-ssh.XXXXXX")"
SSH_OPTS=(-o ControlMaster=auto -o "ControlPath=$SSH_CM_DIR/cm" -o ControlPersist=30)
forge_ssh() { ssh "${SSH_OPTS[@]}" "$FORGE_HOST" "$@"; }
cleanup() {
  ssh "${SSH_OPTS[@]}" -O exit "$FORGE_HOST" 2>/dev/null || true
  rm -rf "$SSH_CM_DIR" "$TMP"
}
trap cleanup EXIT

# ship <dest-basename> <mode> — content on stdin, written atomically on the box.
# Copied from staging-provision.sh so this script stays self-contained.
ship() {
  local name="$1" mode="$2" dest="$FORGE_SECRETS_DIR/$1"
  {
    cat <<REMOTE
umask 077
t=\$(mktemp '$dest.ship.XXXXXX') && cat > "\$t" && chmod $mode "\$t" && mv "\$t" '$dest' || { rm -f "\$t"; exit 1; }
REMOTE
    cat
  } | forge_ssh bash -s
  echo "  shipped $name ($mode)"
}

echo "Initializing hilt-vault on $FORGE_HOST (ref $FORGE_REF)"

# --- 1. Sync the box checkout + boot hilt-vault alone, report its status -----
# The checkout must carry this version's compose block + config/vault/vault.hcl
# before we can boot Vault, so sync it here (same guard/logic as staging-deploy).
#
# This runs inside a command substitution, so only the status JSON goes to stdout;
# ALL progress + diagnostics go to the box's stderr, which streams live to the
# operator's terminal (command substitution captures stdout only).
echo "==> [1/4] syncing box checkout to $FORGE_REF and booting hilt-vault (may take ~30-60s on first run)…"
STATUS_JSON="$(forge_ssh bash -s <<REMOTE
set -euo pipefail
if ! git -C "$FORGE_DIR" diff --quiet || ! git -C "$FORGE_DIR" diff --cached --quiet; then
  echo "ERROR: $FORGE_DIR has uncommitted changes to tracked files; aborting" >&2
  exit 1
fi
echo "  [box] fetching origin…" >&2
git -C "$FORGE_DIR" fetch --quiet --tags --force origin
git -C "$FORGE_DIR" reset --quiet --hard "origin/$FORGE_REF" 2>/dev/null \
  || git -C "$FORGE_DIR" reset --quiet --hard "$FORGE_REF"
echo "  [box] checkout at \$(git -C "$FORGE_DIR" rev-parse --short HEAD)" >&2
cd "$CORE_DIR"
echo "  [box] (re)creating hilt-vault container…" >&2
# stdout -> stderr (1>&2) so compose's own output (image pulls, create/start
# lines, and any error) is visible without polluting the captured status JSON.
$COMPOSE up -d hilt-vault 1>&2
echo "  [box] waiting for Vault to respond…" >&2
for _ in \$(seq 1 30); do
  # Accept ANY valid status JSON (contains "sealed"), regardless of vault's exit
  # code — an uninitialized/sealed Vault exits non-zero but still prints usable
  # status. Only genuine unreachability (empty output) counts as not-ready.
  out=\$($COMPOSE exec -T hilt-vault vault status -format=json </dev/null 2>/dev/null) || true
  if printf '%s' "\$out" | grep -q '"sealed"'; then
    printf '%s' "\$out"; exit 0
  fi
  printf '.' >&2
  sleep 2
done
echo >&2
echo "ERROR: hilt-vault did not become reachable within ~60s. Current state:" >&2
$COMPOSE ps -a hilt-vault >&2 2>/dev/null || true
echo "--- hilt-vault logs (last 40) ---" >&2
$COMPOSE logs --tail=40 hilt-vault >&2 2>/dev/null || true
exit 1
REMOTE
)"

INITIALIZED="$(jq -r '.initialized' <<<"$STATUS_JSON")"
SEALED="$(jq -r '.sealed' <<<"$STATUS_JSON")"
echo "  hilt-vault status: initialized=$INITIALIZED sealed=$SEALED"

DID_INIT=0

# --- 2. Initialize (only if needed) and store the keys in 1Password ----------
if [ "$INITIALIZED" = "false" ]; then
  echo "==> initializing Vault (key-shares=1, key-threshold=1)"
  INIT_JSON="$(forge_ssh bash -s <<REMOTE
set -euo pipefail
cd "$CORE_DIR"
$COMPOSE exec -T hilt-vault vault operator init -key-shares=1 -key-threshold=1 -format=json </dev/null
REMOTE
)"
  # Extract to 0700-tmp files without a trailing newline (jq -j) so they render
  # byte-for-byte; values pass via --rawfile below, never on argv.
  jq -j '.unseal_keys_b64[0]' <<<"$INIT_JSON" > "$TMP/unseal"
  jq -j '.root_token'         <<<"$INIT_JSON" > "$TMP/root"
  INIT_JSON=""

  echo "==> storing unseal key + root token in 1Password item \"$OP_ITEM_TITLE\""
  # Read-modify-write the item (mirrors pkg/staging/onepassword.go): read every
  # existing field, upsert the two vault fields, pipe the complete CONCEALED-field
  # template back via stdin. Keeps values off argv and preserves other secrets.
  existing="$(op item get "$OP_ITEM_TITLE" --vault "$OP_VAULT" --format json --reveal 2>/dev/null || echo '{"fields":[]}')"
  jq -n \
    --arg title "$OP_ITEM_TITLE" \
    --argjson existing "$existing" \
    --rawfile unseal "$TMP/unseal" \
    --rawfile root "$TMP/root" '
    {
      title: $title,
      category: "SECURE_NOTE",
      fields: (
        [ $existing.fields[]?
          | select((.label // .id) != "hilt-vault-unseal-key"
                and (.label // .id) != "hilt-vault-root-token")
          | { id: (.id // .label), type: (.type // "CONCEALED"),
              label: (.label // .id), value: (.value // "") }
        ]
        + [ { id: "hilt-vault-unseal-key", type: "CONCEALED",
              label: "hilt-vault-unseal-key", value: $unseal },
            { id: "hilt-vault-root-token", type: "CONCEALED",
              label: "hilt-vault-root-token", value: $root } ]
      )
    }' | op item edit "$OP_ITEM_TITLE" --vault "$OP_VAULT" >/dev/null
  existing=""
  DID_INIT=1
else
  # Already initialized: the keys must already be in 1Password (this script put
  # them there). If not, the unseal key is lost and Vault must be re-created.
  op item get "$OP_ITEM_TITLE" --vault "$OP_VAULT" --fields label=hilt-vault-unseal-key >/dev/null 2>&1 || {
    echo "ERROR: hilt-vault is already initialized but 1Password has no hilt-vault-unseal-key." >&2
    echo "       The unseal key is lost. Re-create Vault from scratch:" >&2
    echo "         make staging-provision-core && make staging-vault-init" >&2
    exit 1
  }
  echo "  Vault already initialized; reusing the stored keys."
fi

# --- 3. Render + ship vault-secrets.env (both paths) -------------------------
# vault-secrets.env.tpl carries only op:// refs (no ${VAR} chain refs), so a
# plain `op inject` is enough — no render_chain needed.
echo "==> rendering + shipping vault-secrets.env"
op inject -i "$VAULT_TPL" | ship vault-secrets.env 0440

# --- 4. Unseal (if sealed) + ensure the KV v2 engine at secret/ (idempotent) --
# The unseal key + root token come from the just-shipped vault-secrets.env on the
# box (0440, root-only) — the same file the sidecar uses. Two image quirks matter:
#   - `vault operator unseal` does NOT read the key from stdin here (`-` is taken
#     literally), so the key is passed as an argument. It already lives at rest in
#     vault-secrets.env on this box, so its brief presence in the container argv is
#     not a new exposure (and the sidecar unseals the same way).
#   - `vault login -` DOES read the token from stdin, so the root token stays off
#     argv.
# KV v2 is (re)ensured every run so a partial earlier run self-heals; it is
# enabled only when the secret/ mount is absent.
echo "==> [4/4] unsealing (if sealed) + ensuring KV v2 engine at secret/"
forge_ssh bash -s <<REMOTE
set -euo pipefail
cd "$CORE_DIR"
set -a; . "$FORGE_SECRETS_DIR/vault-secrets.env"; set +a
if $COMPOSE exec -T hilt-vault vault status -format=json </dev/null >/dev/null 2>&1; then
  echo "  already unsealed"
else
  $COMPOSE exec -T hilt-vault vault operator unseal "\$HILT_VAULT_UNSEAL_KEY" </dev/null >/dev/null
  echo "  unsealed"
fi
printf '%s' "\$HILT_VAULT_TOKEN" | $COMPOSE exec -T hilt-vault sh -c '
  vault login - >/dev/null 2>&1 || { echo "  ERROR: vault login failed" >&2; exit 1; }
  if vault secrets list 2>/dev/null | grep -q "^secret/"; then
    echo "  KV v2 already enabled at secret/"
  else
    vault secrets enable -path=secret -version=2 kv >/dev/null && echo "  KV v2 enabled at secret/"
  fi
'
REMOTE

echo "Vault init complete. Next: make staging-deploy-core"
