#!/usr/bin/env bash
# Deploy a staging bundle to the box: pull pinned images, recreate, verify health.
#
# Carries NO secrets (provisioning is a separate, prior step). Runnable from a dev
# machine or CI — it only needs SSH to the box and the git tree checked out there.
#
# Usage:
#   scripts/staging-deploy.sh <core|piri>
#
# Env overrides:
#   FORGE_HOST         ssh target            (default: root@23.83.66.244)
#   FORGE_DIR          on-box repo checkout  (default: /root/fil-one/forge)
#   FORGE_SECRETS_DIR  host secrets dir      (default: /root/fil-one/forge/secrets)
#   HEALTH_TIMEOUT     seconds               (default: 300)
set -euo pipefail

BUNDLE="${1:?usage: staging-deploy.sh <core|piri>}"
FORGE_HOST="${FORGE_HOST:-root@23.83.66.244}"
FORGE_DIR="${FORGE_DIR:-/root/fil-one/forge}"
FORGE_SECRETS_DIR="${FORGE_SECRETS_DIR:-/root/fil-one/forge/secrets}"
HEALTH_TIMEOUT="${HEALTH_TIMEOUT:-300}"

case "$BUNDLE" in
  core)
    PROJECT="forge-staging-core"
    ENV_FILES="--env-file versions.env --env-file config.env --env-file ../smart-contracts.env --env-file $FORGE_SECRETS_DIR/secrets.env"
    ;;
  piri)
    PROJECT="forge-staging-piri"
    ENV_FILES="--env-file versions.env --env-file config.env --env-file ../smart-contracts.env"
    ;;
  *) echo "ERROR: unknown bundle '$BUNDLE' (want core|piri)" >&2; exit 1 ;;
esac

DIR="$FORGE_DIR/environments/staging/$BUNDLE"
COMPOSE="docker compose -p $PROJECT $ENV_FILES"

echo "Deploying $BUNDLE to $FORGE_HOST ($DIR)"
ssh "$FORGE_HOST" bash -s <<REMOTE
set -euo pipefail
cd "$DIR"
echo "==> pulling pinned images"
$COMPOSE pull
echo "==> applying stack"
$COMPOSE up -d --remove-orphans

echo "==> waiting for health (timeout ${HEALTH_TIMEOUT}s)"
deadline=\$(( \$(date +%s) + $HEALTH_TIMEOUT ))
while :; do
  # Any service still starting?  Any unhealthy?
  starting=\$($COMPOSE ps --format '{{.Health}}' | grep -c '^starting$' || true)
  unhealthy=\$($COMPOSE ps --format '{{.Health}}' | grep -c '^unhealthy$' || true)
  if [ "\$unhealthy" -gt 0 ]; then
    echo "DEPLOY FAILED: \$unhealthy service(s) unhealthy"
    $COMPOSE ps
    exit 1
  fi
  if [ "\$starting" -eq 0 ]; then
    echo "all services healthy"
    break
  fi
  if [ "\$(date +%s)" -ge "\$deadline" ]; then
    echo "DEPLOY FAILED: timed out waiting for health"
    $COMPOSE ps
    exit 1
  fi
  sleep 5
done
$COMPOSE ps
REMOTE

echo "Deploy of $BUNDLE complete."
