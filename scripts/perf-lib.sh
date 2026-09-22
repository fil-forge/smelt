# Shared helpers for the perf suites (scripts/perf-*.sh). Source, don't run.
#
# A perf run lives in generated/perf-runs/<suite>/<utc-ts>-<label>/ and holds
# everything needed to interpret it later: metadata.json (what code and
# settings ran), stats.csv (container CPU/memory samples), logs/ (service
# logs for the run window) and the suite's own output.

perf_die() { echo "ERROR: $*" >&2; exit 1; }

perf_require() {
  local tool
  for tool in "$@"; do
    command -v "$tool" >/dev/null 2>&1 || perf_die "$tool is required"
  done
}

# perf_project_dir prints the smelt checkout root (the directory above scripts/).
perf_project_dir() {
  cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd
}

# perf_run_dir <suite> <label>: create and print the run directory.
perf_run_dir() {
  local suite="$1" label="$2"
  [[ "$label" =~ ^[A-Za-z0-9._-]+$ ]] || perf_die "LABEL must match [A-Za-z0-9._-]+, got '$label'"
  local dir
  dir="$(perf_project_dir)/generated/perf-runs/$suite/$(date -u +%Y%m%dT%H%M%SZ)-$label"
  mkdir -p "$dir"
  echo "$dir"
}

# perf_git_info <repo-dir>: JSON {sha, dirty} for a checkout, or null when absent.
perf_git_info() {
  local dir="$1"
  if [ ! -d "$dir/.git" ]; then echo null; return; fi
  local sha dirty=false
  sha="$(git -C "$dir" rev-parse --short HEAD)"
  [ -z "$(git -C "$dir" status --porcelain --untracked-files=no)" ] || dirty=true
  jq -cn --arg sha "$sha" --argjson dirty "$dirty" '{sha: $sha, dirty: $dirty}'
}

# perf_metadata <run-dir> <label> <suite-json>: write metadata.json.
# suite-json is a JSON object with the suite's own settings (file set, runs,
# aws profile settings, ...); it lands under the "suite" key.
perf_metadata() {
  local run_dir="$1" label="$2" suite_json="$3"
  local project sibling repos manifest arch workspace_services
  project="$(perf_project_dir)"
  sibling="$(dirname "$project")"

  repos="{}"
  local name
  for name in smelt ingot sprue piri hilt; do
    local dir="$sibling/$name"
    [ "$name" = smelt ] && dir="$project"
    repos="$(jq -cn --argjson acc "$repos" --arg name "$name" --argjson info "$(perf_git_info "$dir")" '$acc + {($name): $info}')"
  done

  manifest="${SMELT_MANIFEST:-smelt.yml}"
  if [ -f "$project/generated/snapshot-scratch/smelt.yml" ] && [ -z "${SMELT_MANIFEST:-}" ]; then
    manifest="generated/snapshot-scratch/smelt.yml"
  fi

  workspace_services="[]"
  if [ -f "$project/generated/compose/workspace.override.yml" ]; then
    workspace_services="$(cd "$project" && go run ./cmd/smelt workspace services 2>/dev/null | tr ' ' '\n' | jq -cR . | jq -cs .)"
  fi
  arch="$(docker version --format '{{.Server.Arch}}' 2>/dev/null || echo unknown)"

  (cd "$project" && docker compose images --format json 2>/dev/null || echo '[]') > "$run_dir/images.json"
  docker system df > "$run_dir/docker-df-before.txt" 2>/dev/null || true

  jq -n \
    --arg label "$label" \
    --arg started "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    --arg host "$(hostname)" \
    --arg manifest "$manifest" \
    --arg blob "$(grep -o 'blob: *[a-z0-9]*' "$project/$manifest" 2>/dev/null | head -1 | sed 's/blob: *//')" \
    --arg arch "$arch" \
    --arg ncpu "$(docker info --format '{{.NCPU}}' 2>/dev/null || echo unknown)" \
    --arg mem "$(docker info --format '{{.MemTotal}}' 2>/dev/null || echo unknown)" \
    --arg docker_version "$(docker version --format '{{.Server.Version}}' 2>/dev/null || echo unknown)" \
    --argjson repos "$repos" \
    --argjson workspace_services "$workspace_services" \
    --argjson suite "$suite_json" \
    '{label: $label, started_at_utc: $started, host: $host,
      manifest: $manifest, piri_blob_backend: $blob,
      docker: {server_arch: $arch, ncpu: $ncpu, mem_total_bytes: $mem, version: $docker_version},
      repos: $repos, workspace_services: $workspace_services, suite: $suite}' \
    > "$run_dir/metadata.json"
}

# perf_stats_start <run-dir> <compose-service>...: sample docker stats for the
# named compose services every 2s into stats.csv until perf_stats_stop.
perf_stats_start() {
  local run_dir="$1"; shift
  local project ids="" svc id
  project="$(perf_project_dir)"
  # One service at a time: a name the current manifest does not define (e.g.
  # piri-postgres with sqlite nodes) must not abort the whole sampler.
  for svc in "$@"; do
    id="$(cd "$project" && docker compose ps -q "$svc" 2>/dev/null || true)"
    [ -n "$id" ] && ids="$ids $id"
  done
  [ -n "${ids// /}" ] || perf_die "no running containers for: $*"
  echo "timestamp_utc,name,cpu_perc,mem_usage,net_io,block_io" > "$run_dir/stats.csv"
  (
    # shellcheck disable=SC2086
    while :; do
      docker stats --no-stream --format '{{.Name}},{{.CPUPerc}},{{.MemUsage}},{{.NetIO}},{{.BlockIO}}' $ids \
        | sed "s/^/$(date -u +%Y-%m-%dT%H:%M:%SZ),/" >> "$run_dir/stats.csv"
      sleep 2
    done
  ) &
  PERF_STATS_PID=$!
}

perf_stats_stop() {
  if [ -n "${PERF_STATS_PID:-}" ]; then
    kill "$PERF_STATS_PID" 2>/dev/null || true
    wait "$PERF_STATS_PID" 2>/dev/null || true
    PERF_STATS_PID=
  fi
}

# perf_dump_logs <run-dir> <since> <until> <compose-service>...: one log file
# per service for the run window (RFC3339 timestamps).
perf_dump_logs() {
  local run_dir="$1" since="$2" until="$3"; shift 3
  local project svc
  project="$(perf_project_dir)"
  mkdir -p "$run_dir/logs"
  for svc in "$@"; do
    (cd "$project" && docker compose logs --no-color --timestamps --since "$since" --until "$until" "$svc" \
      > "$run_dir/logs/$svc.log" 2>&1) || true
  done
  docker system df > "$run_dir/docker-df-after.txt" 2>/dev/null || true
}
