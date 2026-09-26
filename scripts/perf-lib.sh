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

# perf_git_info <repo-dir>: JSON {sha, dirty} for a checkout, or null when the
# directory is missing or is not the root of a repository. Linked worktrees
# have a .git file rather than a directory, so ask git instead of testing for
# one; comparing the toplevel keeps a plain directory nested inside some other
# repository from reporting that repository's SHA.
perf_git_info() {
  local dir="$1" top
  if [ ! -d "$dir" ]; then echo null; return; fi
  top="$(git -C "$dir" rev-parse --show-toplevel 2>/dev/null)" || { echo null; return; }
  if [ "$top" != "$(cd "$dir" && pwd -P)" ]; then echo null; return; fi
  local sha dirty=false
  sha="$(git -C "$dir" rev-parse HEAD)"
  # Untracked files count: a workspace build compiles them all the same.
  [ -z "$(git -C "$dir" status --porcelain)" ] || dirty=true
  jq -cn --arg sha "$sha" --argjson dirty "$dirty" '{sha: $sha, dirty: $dirty}'
}

# perf_check_extra: stop unless PERF_EXTRA_METADATA is unset, empty or exactly
# one JSON object. Suites call it before they create a run directory.
perf_check_extra() {
  [ -n "${PERF_EXTRA_METADATA:-}" ] || return 0
  perf_require jq
  jq -e -s 'length == 1 and (.[0] | type == "object")' <<<"$PERF_EXTRA_METADATA" >/dev/null 2>&1 \
    || perf_die "PERF_EXTRA_METADATA must be a single JSON object"
}

# perf_images <run-dir>: write images.lock.json, one entry per compose
# container, exited one-shots included: service, ref (the image as compose
# named it), digest, revision and source labels, created time, arch and repo
# digests. digest is the ref's own when the ref pins one, else the first repo
# digest; revision is null for an image without the OCI label.
perf_images() {
  local run_dir="$1" project cids services raw ids
  project="$(perf_project_dir)"
  cids="$(cd "$project" && docker compose ps -a -q 2>/dev/null)" || cids=""
  services='[]'
  if [ -n "$cids" ]; then
    # shellcheck disable=SC2086
    services="$(docker inspect $cids 2>/dev/null | jq -c '[.[] | {
      service: .Config.Labels["com.docker.compose.service"],
      ref: .Config.Image, image_id: .Image}] | sort_by(.service, .ref)')" || services='[]'
  fi
  ids="$(jq -r '[.[].image_id | select(. != null)] | unique | .[]' <<<"$services")"
  raw='[]'
  if [ -n "$ids" ]; then
    # docker image inspect exits 1 when any image is gone but still prints the
    # others; an entry without a match keeps only what the container said.
    # shellcheck disable=SC2086
    raw="$(docker image inspect $ids 2>/dev/null || true)"
    [ -n "$raw" ] || raw='[]'
  fi
  jq -n --argjson s "$services" --argjson i "$raw" '
    ([$i[] | {image_id: .Id, repo_digests: .RepoDigests,
              revision: .Config.Labels["org.opencontainers.image.revision"],
              source: .Config.Labels["org.opencontainers.image.source"],
              created: .Created, arch: .Architecture}] | INDEX(.image_id)) as $ids
    | [$s[] | . + ($ids[.image_id] // {})
       | .digest = (((.ref // "") | capture("@(?<d>sha256:[0-9a-f]{64})").d)
                    // ((.repo_digests // [])[0] // "" | split("@")[1]))
       | del(.image_id)]' > "$run_dir/images.lock.json"
}

# perf_container_env <service> <variable>: the variable's value in the running
# service's container as a JSON string, "" when it is set empty, or null when
# it is unset or the service is down. Only non-secret variables go through it.
perf_container_env() {
  local value
  value="$(cd "$(perf_project_dir)" && docker compose exec -T "$1" printenv "$2" 2>/dev/null)" \
    && jq -cn --arg v "$value" '$v' || echo null
}

# perf_metadata <run-dir> <label> <suite-json>: write metadata.json.
# suite-json is a JSON object with the suite's own settings (file set, runs,
# aws profile settings, ...); it lands under the "suite" key.
# PERF_EXTRA_METADATA, a JSON object, lands verbatim under "extra"; smelt
# attaches no meaning to it.
perf_metadata() {
  perf_require git jq docker
  local run_dir="$1" label="$2" suite_json="$3"
  perf_check_extra
  local project sibling repos manifest manifest_file arch workspace_services
  project="$(perf_project_dir)"
  sibling="$(dirname "$project")"

  # libforge is in the list because a go.work that includes it rebuilds every
  # service from it (see pkg/workspace), so its revision changes the binaries.
  repos="{}"
  local name
  for name in smelt ingot sprue piri hilt libforge; do
    local dir="$sibling/$name"
    [ "$name" = smelt ] && dir="$project"
    repos="$(jq -cn --argjson acc "$repos" --arg name "$name" --argjson info "$(perf_git_info "$dir")" '$acc + {($name): $info}')"
  done

  # Same precedence as manifest.ResolveManifestPath: SMELT_MANIFEST (absolute
  # or relative to the project), then the snapshot session, then smelt.yml.
  manifest="${SMELT_MANIFEST:-smelt.yml}"
  if [ -f "$project/generated/snapshot-scratch/smelt.yml" ] && [ -z "${SMELT_MANIFEST:-}" ]; then
    manifest="generated/snapshot-scratch/smelt.yml"
  fi
  manifest_file="$manifest"
  [[ "$manifest_file" = /* ]] || manifest_file="$project/$manifest_file"

  workspace_services="[]"
  if [ -f "$project/generated/compose/workspace.override.yml" ]; then
    workspace_services="$(cd "$project" && go run ./cmd/smelt workspace services 2>/dev/null | tr ' ' '\n' | jq -cR . | jq -cs .)"
  fi
  arch="$(docker version --format '{{.Server.Arch}}' 2>/dev/null || echo unknown)"

  (cd "$project" && docker compose images --format json 2>/dev/null || echo '[]') > "$run_dir/images.json"
  perf_images "$run_dir"
  docker system df > "$run_dir/docker-df-before.txt" 2>/dev/null || true

  jq -n \
    --arg label "$label" \
    --arg started "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    --arg host "$(hostname)" \
    --arg manifest "$manifest" \
    --arg blob "$(grep -o 'blob: *[a-z0-9]*' "$manifest_file" 2>/dev/null | head -1 | sed 's/blob: *//')" \
    --arg arch "$arch" \
    --arg ncpu "$(docker info --format '{{.NCPU}}' 2>/dev/null || echo unknown)" \
    --arg mem "$(docker info --format '{{.MemTotal}}' 2>/dev/null || echo unknown)" \
    --arg docker_version "$(docker version --format '{{.Server.Version}}' 2>/dev/null || echo unknown)" \
    --argjson repos "$repos" \
    --argjson workspace_services "$workspace_services" \
    --argjson suite "$suite_json" \
    --arg storage_driver "$(docker info --format '{{.Driver}}' 2>/dev/null || true)" \
    --arg root_dir "$(docker info --format '{{.DockerRootDir}}' 2>/dev/null || true)" \
    --arg kernel "$(uname -r)" \
    --arg os "$(docker info --format '{{.OperatingSystem}}' 2>/dev/null || true)" \
    --argjson piri_s3_endpoint "$(perf_container_env piri-0 PIRI_S3_ENDPOINT)" \
    --argjson piri_indexer "$(perf_container_env piri-0 PIRI_INDEXER)" \
    --argjson sprue_indexer_endpoint "$(perf_container_env upload SPRUE_INDEXER_ENDPOINT)" \
    --slurpfile images "$run_dir/images.lock.json" \
    --argjson extra "${PERF_EXTRA_METADATA:-null}" \
    'def nullable: if . == "" then null else . end;
     {label: $label, started_at_utc: $started, host: $host,
      manifest: $manifest, piri_blob_backend: $blob,
      docker: {server_arch: $arch, ncpu: $ncpu, mem_total_bytes: $mem, version: $docker_version,
               storage_driver: ($storage_driver|nullable), root_dir: ($root_dir|nullable)},
      system: {kernel: $kernel, os: ($os|nullable)},
      piri: {s3_endpoint: $piri_s3_endpoint, indexer: (($piri_indexer // "")|nullable // "on")},
      sprue: {indexer_endpoint: $sprue_indexer_endpoint},
      repos: $repos, workspace_services: $workspace_services, images: $images[0],
      extra: $extra, suite: $suite}' \
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
