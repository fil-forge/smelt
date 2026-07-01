#!/usr/bin/env bash
# Run the guppy CLI LOCALLY against the staging deployment.
#
# guppy is a pure client: staging's sprue + piri are public over
# https://*.staging.fil.one (real Caddy TLS, public DNS), so nothing here touches
# the box. State (agent identity + delegations) persists in a local dir so a
# multi-step flow — login -> space generate -> space provision -> upload — shares
# one agent across invocations. Put files to upload in the mounted work dir.
#
# Usage:
#   scripts/staging-guppy.sh <guppy-subcommand> [args...]
#   e.g. scripts/staging-guppy.sh login you@fil.org
#        scripts/staging-guppy.sh space generate
#        scripts/staging-guppy.sh upload <space-did>
#
# Env overrides:
#   GUPPY_IMAGE     guppy image           (default: ghcr.io/fil-forge/guppy:main)
#   STAGING_DOMAIN  public base domain    (default: staging.fil.one)
#   SPRUE_DID       upload service DID     (default: did:web:sprue.$STAGING_DOMAIN)
#   SPRUE_URL       upload service URL     (default: https://sprue.$STAGING_DOMAIN)
#   FORGE_HOST      box ssh target, for the login-link hint (default: root@23.83.66.244)
set -euo pipefail

GUPPY_IMAGE="${GUPPY_IMAGE:-ghcr.io/fil-forge/guppy:main}"
STAGING_DOMAIN="${STAGING_DOMAIN:-staging.fil.one}"
SPRUE_DID="${SPRUE_DID:-did:web:sprue.$STAGING_DOMAIN}"
SPRUE_URL="${SPRUE_URL:-https://sprue.$STAGING_DOMAIN}"
FORGE_HOST="${FORGE_HOST:-root@23.83.66.244}"

command -v docker >/dev/null || { echo "ERROR: docker not found in PATH" >&2; exit 1; }
[ "$#" -ge 1 ] || { echo "usage: $0 <guppy-subcommand> [args...]" >&2; exit 1; }

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# Under generated/ (gitignored): keeps the agent private key out of git.
STATE_DIR="$REPO_ROOT/generated/staging-guppy"
# guppy writes its identity to the *config* dir (~/.config/guppy), not --data-dir,
# and won't create the parent — so set HOME=/data and pre-create the dir here.
mkdir -p "$STATE_DIR/state/.config/guppy" "$STATE_DIR/work"

# guppy login blocks until the validation link is approved; staging's nop mailer
# does not send mail, it LOGS the link. Tell the operator how to fetch it.
if [ "$1" = "login" ]; then
  cat >&2 <<EOF
────────────────────────────────────────────────────────────────────────────
 guppy login will BLOCK waiting for approval. Staging's mailer is "nop" — it
 does not send email, it LOGS the validation link. In ANOTHER terminal:

   ssh $FORGE_HOST
   docker logs "\$(docker ps -qf name=sprue)" 2>&1 | grep -i "Validation email" | tail -1

 Open the url=... value from that log line in a browser (or curl it). Once
 approved, this command returns and stores the account delegation locally.
────────────────────────────────────────────────────────────────────────────
EOF
fi

# Attach a TTY only when stdin is one (so this works in pipes/CI too).
tty_flags=(-i)
[ -t 0 ] && tty_flags=(-i -t)

exec docker run --rm "${tty_flags[@]}" \
  -e HOME=/data \
  -v "$STATE_DIR/state:/data" \
  -v "$STATE_DIR/work:/work" \
  "$GUPPY_IMAGE" \
  --data-dir /data \
  --upload-service-did "$SPRUE_DID" \
  --upload-service-url "$SPRUE_URL" \
  --receipts-url "$SPRUE_URL/receipt" \
  "$@"
