#!/usr/bin/env bash
# Register ingot as hilt's regional S3 provider.
#
# WHY THIS EXISTS: in local dev, hilt's post_start hook (systems/hilt/post_start.sh)
# reads ingot's DID from the generated keys dir and registers it as the regional
# provider right after hilt becomes healthy. Across the split staging bundles that
# hook can't run — hilt's container has no ingot key and the two bundles deploy
# independently — so nothing registers the provider, and hilt rejects every
# tenant creation for the region ("unknown region") and every /s3/* invocation
# from ingot. This script closes the gap: it derives ingot's DID in the piri
# bundle and runs the same admin call the local post_start hook makes.
#
# NOTE: this is the ONLY cross-bundle registration hilt needs. Its authority to
# call sprue's /customer/add comes entirely from the committed
# environments/staging/proofs/hilt-customer-add-proof.txt; sprue resolves
# did:web:hilt.staging.fil.one over https at invocation time.
#
# Run it AFTER both `staging-deploy-core` and `staging-deploy-piri` report
# healthy, and BEFORE creating any tenants. It is idempotent: an
# already-registered provider is tolerated.
#
# Carries NO secrets. Needs only SSH to the box and the deployed bundles.
#
# Usage:
#   scripts/staging-register-ingot.sh
#
# Env overrides:
#   FORGE_HOST         ssh target            (default: root@23.83.66.244)
#   FORGE_DIR          on-box repo checkout  (default: /root/fil-one/forge)
#   FORGE_SECRETS_DIR  host secrets dir      (default: /root/fil-one/forge/secrets)
#   INGOT_REGION       provider region       (default: us-west-1 — must match the
#                      `region` in ingot-config.yaml and clients' AWS_REGION)
set -euo pipefail

FORGE_HOST="${FORGE_HOST:-root@23.83.66.244}"
FORGE_DIR="${FORGE_DIR:-/root/fil-one/forge}"
FORGE_SECRETS_DIR="${FORGE_SECRETS_DIR:-/root/fil-one/forge/secrets}"
INGOT_REGION="${INGOT_REGION:-us-west-1}"

CORE_DIR="$FORGE_DIR/environments/staging/core"
PIRI_DIR="$FORGE_DIR/environments/staging/piri"

CORE_ENV="--env-file versions.env --env-file config.env --env-file ../smart-contracts.env --env-file $FORGE_SECRETS_DIR/secrets.env"
PIRI_ENV="--env-file versions.env --env-file config.env --env-file ../smart-contracts.env --env-file $FORGE_SECRETS_DIR/piri-secrets.env"

CORE_COMPOSE="docker compose -p forge-staging-core $CORE_ENV"
PIRI_COMPOSE="docker compose -p forge-staging-piri $PIRI_ENV"

echo "Registering ingot as hilt's $INGOT_REGION provider on $FORGE_HOST"
ssh "$FORGE_HOST" bash -s <<REMOTE
set -euo pipefail

# Derive ingot's DID from its provisioned key. A throwaway 'run' loads only the
# config + key (no postgres needed), so this works even if ingot is unhealthy.
#
# Two redirects are load-bearing on every 'compose run'/'exec' below:
#   2>&1  — 'ingot whoami' prints the DID via cobra's cmd.Print*, which go to
#           STDERR, not stdout; without folding stderr in, the grep sees nothing.
#   </dev/null — this whole script is piped to the remote 'bash -s' over stdin, and
#           'compose run'/'exec' attach the container's stdin to that same stream.
#           Without the redirect they devour the rest of the heredoc, the session
#           hits EOF, and ssh exits 0 having silently skipped the remaining steps.
cd "$PIRI_DIR"
WHOAMI_OUTPUT=\$($PIRI_COMPOSE run --rm --no-deps -T ingot whoami </dev/null 2>&1) || {
  echo "ERROR: failed to derive ingot DID via 'ingot whoami':" >&2
  echo "\$WHOAMI_OUTPUT" >&2
  exit 1
}
INGOT_DID=\$(printf '%s\n' "\$WHOAMI_OUTPUT" | grep -oE 'did:key:z[a-zA-Z0-9]+' | head -n1 || true)
if [ -z "\$INGOT_DID" ]; then
  echo "ERROR: no did:key found in 'ingot whoami' output:" >&2
  echo "\$WHOAMI_OUTPUT" >&2
  exit 1
fi
echo "  ingot DID: \$INGOT_DID"

# Same admin call as the local post_start hook. Tolerate "already registered"
# so re-running is a no-op; any other failure is fatal.
cd "$CORE_DIR"
echo "  registering provider for region $INGOT_REGION"
if REG_OUTPUT=\$($CORE_COMPOSE exec -T hilt hilt client admin provider add "\$INGOT_DID" "$INGOT_REGION" </dev/null 2>&1); then
  :
elif printf '%s\n' "\$REG_OUTPUT" | grep -qi "already registered"; then
  echo "  (already registered — continuing)"
else
  echo "ERROR: provider registration failed:" >&2
  echo "\$REG_OUTPUT" >&2
  exit 1
fi

echo "  provider registered: \$INGOT_DID -> $INGOT_REGION"
REMOTE

echo "Done. Hilt tenants can now be created in region $INGOT_REGION."
