#!/usr/bin/env bash
# Register the piri node as a storage provider with sprue (the core upload service).
#
# WHY THIS EXISTS: in local dev, sprue's post_start hook (systems/upload/post_start.sh)
# registers every piri node as a provider right after sprue becomes healthy. Across
# the split staging bundles that hook can't run — sprue's container has no piri keys
# and the two bundles deploy independently — so nothing registers the provider, and
# uploads fail with CandidateUnavailable ("no storage providers available").
# This script closes the gap: it derives the piri DID from the piri bundle's key and
# runs the same admin calls the local post_start hook makes.
#
# Run it AFTER both `staging-deploy-core` and `staging-deploy-piri` report healthy.
# It is idempotent: an already-registered provider is tolerated and the weight is
# simply (re)applied.
#
# Carries NO secrets. Needs only SSH to the box and the deployed bundles.
#
# Usage:
#   scripts/staging-register-piri.sh
#
# Env overrides:
#   FORGE_HOST         ssh target            (default: root@23.83.66.244)
#   FORGE_DIR          on-box repo checkout  (default: /root/fil-one/forge)
#   FORGE_SECRETS_DIR  host secrets dir      (default: /root/fil-one/forge/secrets)
#   STAGING_DOMAIN     public base domain    (default: staging.fil.one)
#   PIRI_ENDPOINT      piri public URL       (default: https://piri-0.$STAGING_DOMAIN)
set -euo pipefail

FORGE_HOST="${FORGE_HOST:-root@23.83.66.244}"
FORGE_DIR="${FORGE_DIR:-/root/fil-one/forge}"
FORGE_SECRETS_DIR="${FORGE_SECRETS_DIR:-/root/fil-one/forge/secrets}"
STAGING_DOMAIN="${STAGING_DOMAIN:-staging.fil.one}"
PIRI_ENDPOINT="${PIRI_ENDPOINT:-https://piri-0.$STAGING_DOMAIN}"

CORE_DIR="$FORGE_DIR/environments/staging/core"
PIRI_DIR="$FORGE_DIR/environments/staging/piri"

CORE_ENV="--env-file versions.env --env-file config.env --env-file ../smart-contracts.env --env-file $FORGE_SECRETS_DIR/secrets.env"
PIRI_ENV="--env-file versions.env --env-file config.env --env-file ../smart-contracts.env"

CORE_COMPOSE="docker compose -p forge-staging-core $CORE_ENV"
PIRI_COMPOSE="docker compose -p forge-staging-piri $PIRI_ENV"

echo "Registering the piri provider ($PIRI_ENDPOINT) with sprue on $FORGE_HOST"
ssh "$FORGE_HOST" bash -s <<REMOTE
set -euo pipefail

# Derive the piri DID from its provisioned key. A throwaway 'run' with the
# entrypoint overridden parses the key without booting the node, so this works
# even if piri-0 is unhealthy.
#
# Two redirects are load-bearing on every 'compose run'/'exec' below:
#   2>&1  — 'piri identity parse' prints the DID via cobra's cmd.Print*, which go
#           to STDERR, not stdout; without folding stderr in, the grep sees nothing.
#   </dev/null — this whole script is piped to the remote 'bash -s' over stdin, and
#           'compose run'/'exec' attach the container's stdin to that same stream.
#           Without the redirect they devour the rest of the heredoc, the session
#           hits EOF, and ssh exits 0 having silently skipped the remaining steps.
cd "$PIRI_DIR"
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

# Same admin calls as the local post_start hook. The proof file is the committed
# environments/staging/proofs/piri-0-proof.txt, mounted into sprue at /proofs.
# Tolerate "already registered" so re-running is a no-op; any other failure is fatal.
cd "$CORE_DIR"
echo "  registering provider at $PIRI_ENDPOINT"
if REG_OUTPUT=\$($CORE_COMPOSE exec -T sprue sprue client admin provider register "\$PIRI_DID" "$PIRI_ENDPOINT" /proofs/piri-0-proof.txt </dev/null 2>&1); then
  :
elif printf '%s\n' "\$REG_OUTPUT" | grep -q "already registered"; then
  echo "  (already registered — continuing)"
else
  echo "ERROR: provider registration failed:" >&2
  echo "\$REG_OUTPUT" >&2
  exit 1
fi

echo "  setting provider weight (100 100)"
$CORE_COMPOSE exec -T sprue sprue client admin provider weight set "\$PIRI_DID" 100 100 </dev/null

echo "  provider registered: \$PIRI_DID -> $PIRI_ENDPOINT"
REMOTE

echo "Done. Uploads via sprue can now select the piri provider."
