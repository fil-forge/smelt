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
#   FORGE_REF          branch/tag/sha to deploy, checked out on the box (default: main)
#   HEALTH_TIMEOUT     seconds               (default: 300)
set -euo pipefail

BUNDLE="${1:?usage: staging-deploy.sh <core|piri>}"
FORGE_HOST="${FORGE_HOST:-root@23.83.66.244}"
FORGE_DIR="${FORGE_DIR:-/root/fil-one/forge}"
FORGE_SECRETS_DIR="${FORGE_SECRETS_DIR:-/root/fil-one/forge/secrets}"
FORGE_REF="${FORGE_REF:-main}"
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

echo "Deploying $BUNDLE to $FORGE_HOST ($DIR) at ref $FORGE_REF"
ssh "$FORGE_HOST" bash -s <<REMOTE
set -euo pipefail
echo "==> checking out $FORGE_REF in $FORGE_DIR"
# Refuse to clobber hand-edits: the reset --hard below would silently discard
# uncommitted changes to tracked files. Untracked files (provisioned secrets,
# generated artifacts) are intentionally ignored.
if ! git -C "$FORGE_DIR" diff --quiet || ! git -C "$FORGE_DIR" diff --cached --quiet; then
  echo "ERROR: $FORGE_DIR has uncommitted changes to tracked files; aborting deploy" >&2
  git -C "$FORGE_DIR" status --short --untracked-files=no >&2
  exit 1
fi
git -C "$FORGE_DIR" fetch --quiet --tags --force origin
# reset --hard makes the box a faithful checkout of FORGE_REF. Prefer the
# freshly-fetched remote branch tip (origin/<ref>); fall back to the bare ref
# for a tag or commit sha, which the fetch above brought local.
git -C "$FORGE_DIR" reset --hard "origin/$FORGE_REF" 2>/dev/null \
  || git -C "$FORGE_DIR" reset --hard "$FORGE_REF"
echo "    on \$(git -C "$FORGE_DIR" rev-parse --short HEAD)"
cd "$DIR"
echo "==> pulling pinned images"
$COMPOSE pull
echo "==> applying stack"
$COMPOSE up -d --remove-orphans

echo "==> waiting for health (timeout ${HEALTH_TIMEOUT}s)"
deadline=\$(( \$(date +%s) + $HEALTH_TIMEOUT ))
# A crash-looping container oscillates running<->restarting faster than we poll,
# so a single 'compose ps' State snapshot can catch it mid-restart in a momentary
# 'running' state with an empty Health field and wrongly count it ready (this is
# how sprue slipped through). RestartCount is the non-racy signal: a fresh
# 'up -d' starts every container at 0, so any nonzero count means the container
# has already died and been restarted at least once -> crash-looping. It is not
# exposed by 'compose ps --format', so we drive the gate off 'docker inspect'.
#
# ready_streak guards the opposite race: a service that comes up clean and only
# crashes a second or two later. We require the whole stack to read fully-ready
# on two consecutive polls (>= one 5s sleep apart) before declaring success, so a
# crash inside that window bumps RestartCount and flips the verdict to failed.
ready_streak=0
while :; do
  bad=0; pending=0
  while IFS='|' read -r name status health restarts exitcode; do
    name="\${name#/}"   # docker inspect .Name carries a leading slash
    case "\$status" in
      running)
        if [ "\$restarts" -gt 0 ]; then
          echo "  crash-looping (restarts=\$restarts): \$name"; bad=\$((bad+1))
        else
          case "\$health" in
            starting)  pending=\$((pending+1)) ;;
            unhealthy) echo "  unhealthy:   \$name"; bad=\$((bad+1)) ;;
            # healthy, or no healthcheck (none) -> ready
          esac
        fi
        ;;
      restarting)
        echo "  restarting (restarts=\$restarts): \$name"; bad=\$((bad+1)) ;;
      exited)
        # One-shot containers (e.g. minio-init, restart:no) are fine once exit 0.
        [ "\$exitcode" = "0" ] || { echo "  exited (\$exitcode): \$name"; bad=\$((bad+1)); }
        ;;
      dead)
        echo "  dead: \$name"; bad=\$((bad+1)) ;;
      *)
        # created/paused/removing/etc. — not ready yet
        pending=\$((pending+1)) ;;
    esac
  done < <(docker inspect \
    -f '{{.Name}}|{{.State.Status}}|{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}|{{.RestartCount}}|{{.State.ExitCode}}' \
    \$($COMPOSE ps -aq))

  if [ "\$bad" -gt 0 ]; then
    echo "DEPLOY FAILED: \$bad service(s) crash-looping, dead, or unhealthy"
    $COMPOSE ps -a
    exit 1
  fi
  if [ "\$pending" -eq 0 ]; then
    ready_streak=\$((ready_streak+1))
    if [ "\$ready_streak" -ge 2 ]; then
      echo "all services healthy"
      break
    fi
  else
    ready_streak=0
  fi
  if [ "\$(date +%s)" -ge "\$deadline" ]; then
    echo "DEPLOY FAILED: timed out waiting for health"
    $COMPOSE ps -a
    exit 1
  fi
  sleep 5
done
$COMPOSE ps -a
REMOTE

echo "Deploy of $BUNDLE complete."
