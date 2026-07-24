#!/usr/bin/env bash
# Add the piri node's DID to the core delegator's allow list (DynamoDB).
#
# WHY THIS EXISTS: in local dev, piri's entrypoint registers its own DID with the
# delegator allow list (systems/piri/register-did.sh) by writing straight to the
# shared dynamodb-local before `piri init`. Across the split staging bundles that
# can't happen — the piri bundle has no route to the core bundle's DynamoDB — so
# the staging piri entrypoint drops that step. Nothing else fills the gap, so
# `piri init` step [4/7] ("request approval from Storacha") hits the delegator,
# which finds the DID absent from the allow list and returns 403 (ErrDIDNotAllowed).
# This script closes the gap from the core side.
#
# Run it AFTER `staging-deploy-core` and BEFORE `staging-deploy-piri`, so piri's
# first init already finds itself allow-listed. It is idempotent: re-running with
# an already-allowed DID is a no-op.
#
# Carries NO secrets. Needs only SSH to the box and the deployed bundles.
#
# Usage:
#   scripts/staging-allowlist-piri.sh
#
# Env overrides:
#   FORGE_HOST         ssh target            (default: root@23.83.66.244)
#   FORGE_DIR          on-box repo checkout  (default: /root/fil-one/forge)
#   FORGE_SECRETS_DIR  host secrets dir      (default: /root/fil-one/forge/secrets)
set -euo pipefail

FORGE_HOST="${FORGE_HOST:-root@23.83.66.244}"
FORGE_DIR="${FORGE_DIR:-/root/fil-one/forge}"
FORGE_SECRETS_DIR="${FORGE_SECRETS_DIR:-/root/fil-one/forge/secrets}"

CORE_DIR="$FORGE_DIR/environments/staging/core"
PIRI_DIR="$FORGE_DIR/environments/staging/piri"

CORE_ENV="--env-file versions.env --env-file config.env --env-file ../smart-contracts.env --env-file $FORGE_SECRETS_DIR/secrets.env --env-file $FORGE_SECRETS_DIR/vault-secrets.env"
PIRI_ENV="--env-file versions.env --env-file config.env --env-file ../smart-contracts.env --env-file $FORGE_SECRETS_DIR/piri-secrets.env"

CORE_COMPOSE="docker compose -p forge-staging-core $CORE_ENV"
PIRI_COMPOSE="docker compose -p forge-staging-piri $PIRI_ENV"

echo "Allow-listing the piri DID with the core delegator on $FORGE_HOST"
ssh "$FORGE_HOST" bash -s <<REMOTE
set -euo pipefail

# Derive the piri DID from its provisioned key. piri-0 may be crash-looping (that
# is the very failure this script fixes), so 'exec' into the running service is
# unreliable; a throwaway 'run' with the entrypoint overridden parses the key
# without booting the node. Pull first so the image is present even before the
# piri bundle has been deployed.
#
# Two redirects are load-bearing on every 'compose run'/'exec' below:
#   2>&1  — 'piri identity parse' prints the DID via cobra's cmd.Print*, which go
#           to STDERR, not stdout; without folding stderr in, the grep sees nothing.
#   </dev/null — this whole script is piped to the remote 'bash -s' over stdin, and
#           'compose run'/'exec' attach the container's stdin to that same stream.
#           Without the redirect they devour the rest of the heredoc (the store
#           allow-did line included), the session hits EOF, and ssh exits 0 having
#           silently skipped the actual write.
cd "$PIRI_DIR"
$PIRI_COMPOSE pull piri-0 >/dev/null 2>&1 </dev/null
PARSE_OUTPUT=\$($PIRI_COMPOSE run --rm --no-deps -T --entrypoint /usr/bin/piri piri-0 identity parse /keys/piri.pem </dev/null 2>&1) || {
  echo "ERROR: failed to parse piri DID from /keys/piri.pem:" >&2
  echo "\$PARSE_OUTPUT" >&2
  exit 1
}
PIRI_DID=\$(printf '%s\n' "\$PARSE_OUTPUT" | grep -oE 'did:key:z[a-zA-Z0-9]+' | head -n1 || true)
if [ -z "\$PIRI_DID" ]; then
  echo "ERROR: no did:key found in 'piri identity parse' output:" >&2
  echo "\$PARSE_OUTPUT" >&2
  exit 1
fi
echo "  piri DID: \$PIRI_DID"

# Add it to the delegator's allow list. The delegator binary is 'registrar'; its
# 'store allow-did' subcommand reuses the same DynamoDB config the server reads
# (region + table + dynamodb-local endpoint) from the mounted /.delegator.yaml.
# Idempotent: a DID already present reports "already allowed" and exits 0.
cd "$CORE_DIR"
$CORE_COMPOSE exec -T delegator /usr/bin/registrar store allow-did "\$PIRI_DID" --config /.delegator.yaml </dev/null
REMOTE

echo "piri DID allow-listed. You can now run: make staging-deploy-piri"
