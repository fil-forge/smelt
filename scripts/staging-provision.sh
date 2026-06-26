#!/usr/bin/env bash
# Render-and-ship staging secrets onto the box — DEVELOPER-MACHINE ONLY.
#
# Pulls secret values from 1Password (op inject for templated config files,
# op read for standalone key/wallet fields) and streams each result directly into
# an SSH session that writes it atomically into the host secrets dir. NOTHING
# secret is ever written to local disk. Never run from CI.
#
# Prerequisites on the dev machine:
#   - an authenticated 1Password session (`op signin`)
#   - SSH access to the box
#
# Usage:
#   scripts/staging-provision.sh [core|piri|all]    # default: all
#
# Env overrides:
#   FORGE_HOST         ssh target           (default: root@23.83.66.244)
#   FORGE_SECRETS_DIR  host secrets dir     (default: /root/fil-one/forge/secrets)
#   OP_ITEM            1Password item ref   (default: op://Fil One/FilOne Forge Staging)
set -euo pipefail

FORGE_HOST="${FORGE_HOST:-root@23.83.66.244}"
FORGE_SECRETS_DIR="${FORGE_SECRETS_DIR:-/root/fil-one/forge/secrets}"
OP_ITEM="${OP_ITEM:-op://Fil One/FilOne Forge Staging}"
BUNDLE="${1:-all}"

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
STAGING="$REPO_ROOT/environments/staging"
CONTRACTS_ENV="$STAGING/smart-contracts.env"
WALLETS_ENV="$STAGING/wallets.env"

command -v op >/dev/null  || { echo "ERROR: 1Password CLI 'op' not found in PATH" >&2; exit 1; }
op whoami >/dev/null 2>&1 || { echo "ERROR: not signed in to 1Password — run 'op signin'" >&2; exit 1; }
[ -f "$CONTRACTS_ENV" ] || { echo "ERROR: $CONTRACTS_ENV not found" >&2; exit 1; }
[ -f "$WALLETS_ENV" ]   || { echo "ERROR: $WALLETS_ENV not found" >&2; exit 1; }

# Build sed expressions that substitute every ${VAR} from smart-contracts.env
# (chain id, RPC URL, contract addresses) and wallets.env (PAYER_ADDRESS, ...).
# Runs after `op inject` so config templates carry both op:// secret refs and
# ${VAR} refs.
SED_ARGS=()
while IFS='=' read -r k v; do
  [ -z "$k" ] && continue
  case "$k" in \#*) continue;; esac
  SED_ARGS+=(-e "s|\${${k}}|${v}|g")
done < <(cat "$CONTRACTS_ENV" "$WALLETS_ENV")
render_chain() { sed "${SED_ARGS[@]}"; }

echo "Provisioning ($BUNDLE) to $FORGE_HOST:$FORGE_SECRETS_DIR"
ssh "$FORGE_HOST" "mkdir -p '$FORGE_SECRETS_DIR' && chmod 700 '$FORGE_SECRETS_DIR'"

# ship <dest-basename> <mode> — content arrives on stdin, written atomically
# (temp file, fsync via mv) so a partial transfer never replaces a good file.
ship() {
  local name="$1" mode="$2" dest="$FORGE_SECRETS_DIR/$1"
  ssh "$FORGE_HOST" "umask 077; t=\$(mktemp '$FORGE_SECRETS_DIR/.ship.XXXXXX'); cat > \"\$t\" && chmod $mode \"\$t\" && mv \"\$t\" '$dest' || { rm -f \"\$t\"; exit 1; }"
  echo "  shipped $name ($mode)"
}

# op inject resolves {{ op:// }} secret refs; render_chain resolves ${VAR} refs.
render_secret_tpl() { op inject -i "$1" | render_chain | ship "$2" 0440; }
render_plain_tpl()  { render_chain < "$1" | ship "$2" 0440; }   # no secrets
read_field()        { op read "$OP_ITEM/$1" | ship "$2" "$3"; }

provision_core() {
  echo "[core] rendering configs..."
  render_secret_tpl "$STAGING/core/config/sprue/config.yaml.tpl"        sprue-config.yaml
  render_secret_tpl "$STAGING/core/config/delegator/delegator.yaml.tpl" delegator.yaml
  render_secret_tpl "$STAGING/core/secrets.env.tpl"                     secrets.env
  echo "[core] shipping key files..."
  read_field sprue-key           sprue.pem             0440
  read_field signing-service-key signing-service.pem   0440
  read_field delegator-key       delegator.pem         0440
  # Tier-1 fund-bearing key: tightest perms.
  read_field payer-key           payer-key.hex         0400
}

provision_piri() {
  # piri's base config embeds PAYER_ADDRESS — fail loudly if keygen hasn't filled it.
  grep -qE '^PAYER_ADDRESS=0x[0-9a-fA-F]{40}$' "$WALLETS_ENV" || {
    echo "ERROR: PAYER_ADDRESS not set in $WALLETS_ENV — run 'make staging-keygen' and commit it first" >&2
    exit 1
  }
  echo "[piri] rendering base config..."
  render_plain_tpl "$STAGING/piri/config/piri/piri-base-config.toml.tpl" piri-base-config.toml
  echo "[piri] shipping key files..."
  read_field piri-0-key    piri-0.pem         0440
  read_field piri-0-wallet piri-0-wallet.hex  0440
}

case "$BUNDLE" in
  core) provision_core ;;
  piri) provision_piri ;;
  all)  provision_core; provision_piri ;;
  *)    echo "ERROR: unknown bundle '$BUNDLE' (want core|piri|all)" >&2; exit 1 ;;
esac

echo "Provisioning complete."
