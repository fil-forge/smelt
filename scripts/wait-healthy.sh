#!/usr/bin/env bash
# Wait until every running container that defines a health check reports
# healthy. `docker compose up -d` returns once the containers have started,
# and a request against a service that is still starting fails like a
# missing bucket or a bad key would.
#
#   ./scripts/wait-healthy.sh               # every service in the stack
#   ./scripts/wait-healthy.sh ingot upload  # only these services
#
# Containers without a health check (guppy) and one-shot init containers that
# have exited are not waited for. `docker compose up --wait` would treat an
# exited init container as a failure, depending on the compose version, and
# prints no progress. Gives up after WAIT_TIMEOUT seconds (default 600) and
# names the services that are not healthy yet.
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."

timeout="${WAIT_TIMEOUT:-600}"
[[ "$timeout" =~ ^[1-9][0-9]*$ ]] || { echo "ERROR: WAIT_TIMEOUT must be a positive number of seconds, got '$timeout'" >&2; exit 1; }
deadline=$((SECONDS + timeout))
reported=""

while :; do
  states="$(docker compose ps --format '{{.Service}} {{.State}} {{.Health}}' "$@")" \
    || { echo "ERROR: docker compose ps failed; is Docker running?" >&2; exit 1; }
  # A crash-looping container can report no health between restarts, so its
  # state counts too. Compare whole fields: "unhealthy" contains "healthy".
  pending="$(awk '$2 == "restarting" { print $1 " (restarting)"; next }
                  $3 != "" && $3 != "healthy" { print $1 " (" $3 ")" }' <<<"$states" \
    | sort | paste -s -d ' ' -)"
  if [ -z "$pending" ]; then
    echo "All services healthy."
    exit 0
  fi
  if [ "$SECONDS" -ge "$deadline" ]; then
    echo "ERROR: not healthy after ${timeout}s: $pending" >&2
    echo "Read their logs with: docker compose logs <service>" >&2
    exit 1
  fi
  if [ "$pending" != "$reported" ]; then
    echo "Waiting for: $pending"
    reported="$pending"
  fi
  sleep 5
done
