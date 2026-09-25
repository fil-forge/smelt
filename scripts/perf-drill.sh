#!/usr/bin/env bash
# Run the fil-one/storage-qualification drill against the ingot in this stack,
# capped at STOP_INGEST_AT bytes, and keep everything needed to compare runs.
#
#   ./scripts/perf-drill.sh setup             # once per stack: tenant, key, provider .env
#   LABEL=before ./scripts/perf-drill.sh run
#   # edit ingot / sprue / piri, then: make redeploy
#   LABEL=after  ./scripts/perf-drill.sh run
#   ./scripts/perf-results.py compare drill before after
#
# Environment:
#   STORAGE_QUALIFICATION_DIR  checkout of fil-one/storage-qualification (default:
#                              fil-one/storage-qualification beside the fil-forge/
#                              directory this smelt checkout lives in)
#   PROFILE         drill profile: import (default), smoke or import-blocks
#   STOP_INGEST_AT  stop ingesting after this many bytes, in GB (default 50GB)
#   RAMP            ramp-up before the first measured window (default 10s)
#   WINDOW          measurement window length (default 10s; the drill's own is 60s)
#   VERIFY_LAG_MIN  earliest read-back of a written block (default 30s)
#   VERIFY_LAG_MAX  latest read-back of a written block (default 60s)
#   RATE_TARGET     offered ingest rate in bytes per second, e.g. 5GB (default:
#                   the profile's own); a stack faster than this measures as this
#   WORKERS         fixed in-flight block count (default 16; the drill's own
#                   is a ramp from 64 to 512)
#   DURATION        upper bound on the run (default 15m; the profile's is 1h)
#   LABEL           run label (default: the profile name)
#
# A run needs about 2.5x STOP_INGEST_AT of free disk, on the host and in
# Docker's disk, and refuses to start with less; `make clean` reclaims it.
#
# Output: generated/perf-runs/drill/<utc-ts>-<label>/ plus one row per run
# appended to generated/perf-runs/drill/runs.jsonl.
set -euo pipefail

# shellcheck source=perf-lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/perf-lib.sh"

SUITE=drill
PROJECT="$(perf_project_dir)"
# Go-style layout, as for s3-speedtests in perf-s3-speedtest.sh.
SQ_DIR="${STORAGE_QUALIFICATION_DIR:-$(dirname "$(dirname "$PROJECT")")/fil-one/storage-qualification}"
DRILL_BIN="$SQ_DIR/bin/drill"
PROFILE="${PROFILE:-import}"
STOP_INGEST_AT="${STOP_INGEST_AT:-50GB}"
RAMP="${RAMP:-10s}"
# A capped local run ingests for minutes, not the profile's hour. With the
# drill's 60-second windows that is a handful of windows, and p5 over fewer
# than 20 windows is just the slowest one. 10 seconds still holds a dozen or
# more of the import profile's ~128 MiB blobs at 0.2 GB/s.
WINDOW="${WINDOW:-10s}"
# After the cap the drill keeps running until every written blob has been
# read back. With its default lag (5 to 15 minutes) a few minutes of ingest
# would see no reads at all and then wait up to a quarter of an hour for them.
# With 30 to 60 seconds, reads run alongside ingest within the first minute
# and the run ends about a minute after the cap.
VERIFY_LAG_MIN="${VERIFY_LAG_MIN:-30s}"
VERIFY_LAG_MAX="${VERIFY_LAG_MAX:-60s}"
# The drill's ramp from 64 to 512 blobs in flight is more than a laptop stack
# can serve.
WORKERS="${WORKERS:-16}"
# A stack too slow to reach STOP_INGEST_AT still finishes in minutes.
DURATION="${DURATION:-15m}"
LABEL="${LABEL:-$PROFILE}"
TENANT=drill
# PROFILE names the drill profile in this script, so the AWS CLI profile that
# s3-key.sh writes gets another name.
AWS_CLI_PROFILE=smelt-drill
# The drill reads its endpoint and key from <provider>/.env. Each run gets its
# own provider directory inside the run directory (see run), with .env linked
# to this one.
PROVIDER_DIR="$PROJECT/generated/perf-runs/$SUITE/provider"
# Containers whose CPU/memory and logs are captured around each run.
SERVICES=(ingot upload piri-0 hilt)
STATS_SERVICES=(ingot upload piri-0 piri-postgres ingot-postgres)

usage() { sed -n '2,/^set -euo/p' "$0" | sed '$d' | sed 's/^# \{0,1\}//'; }

setup() {
  perf_require aws jq docker go
  build_drill

  (cd "$PROJECT" && TENANT="$TENANT" PROFILE="$AWS_CLI_PROFILE" ./scripts/s3-key.sh)

  # `aws configure get` exits 1 for a missing setting; the checks below say which.
  local endpoint region access_key secret_key
  endpoint="$(aws configure get --profile "$AWS_CLI_PROFILE" endpoint_url || true)"
  region="$(aws configure get --profile "$AWS_CLI_PROFILE" region || true)"
  access_key="$(aws configure get --profile "$AWS_CLI_PROFILE" aws_access_key_id || true)"
  secret_key="$(aws configure get --profile "$AWS_CLI_PROFILE" aws_secret_access_key || true)"
  [ -n "$access_key" ] && [ -n "$secret_key" ] || perf_die "profile $AWS_CLI_PROFILE has no access key; s3-key.sh did not finish"
  [ -n "$region" ] || perf_die "profile $AWS_CLI_PROFILE has no region"
  # SQ_ENDPOINT is host:port; SQ_INSECURE=true makes the drill use plain HTTP.
  [[ "$endpoint" = http://* ]] || perf_die "profile $AWS_CLI_PROFILE has endpoint_url '$endpoint'; expected http://host:port"
  # A key for a stack that cannot store a blob is no use to the drill.
  smoke_check

  mkdir -p "$PROVIDER_DIR"
  # umask applies only when a file is created, so never write over an old one.
  # printf is a builtin, so the secret never shows up in the process list.
  rm -f "$PROVIDER_DIR/.env"
  (
    umask 077
    printf '%s\n' \
      "# Written by scripts/perf-drill.sh setup for tenant $(aws configure get --profile "$AWS_CLI_PROFILE" tenant_id)." \
      "# The run step sets SQ_BUCKET_PREFIX per run." \
      "SQ_ENDPOINT=${endpoint#http://}" \
      "SQ_INSECURE=true" \
      "SQ_REGION=$region" \
      "SQ_ACCESS_KEY=$access_key" \
      "SQ_SECRET_KEY=$secret_key" \
      > "$PROVIDER_DIR/.env"
  )
  echo "wrote $PROVIDER_DIR/.env"
}

run() {
  perf_require aws jq docker python3 go
  case "$PROFILE" in
    import|smoke|import-blocks) ;;
    *) perf_die "PROFILE must be import, smoke or import-blocks, got '$PROFILE'" ;;
  esac
  # The disk check reads the number of GB from STOP_INGEST_AT; the drill
  # itself gets the value as given.
  [[ "$STOP_INGEST_AT" =~ ^[0-9]+(\.[0-9]+)?GB$ ]] \
    || perf_die "STOP_INGEST_AT must be a number of GB such as 50GB or 7.5GB, got '$STOP_INGEST_AT'"
  [[ "$WORKERS" =~ ^[1-9][0-9]*$ ]] || perf_die "WORKERS must be a positive integer, got '$WORKERS'"
  # The drill would reject a bad duration too, but only after the run
  # directory exists.
  local knob
  for knob in RAMP WINDOW VERIFY_LAG_MIN VERIFY_LAG_MAX DURATION; do
    [[ -z "${!knob:-}" || "${!knob}" =~ ^([0-9]+(\.[0-9]+)?(ns|us|ms|s|m|h))+$ ]] \
      || perf_die "$knob must be a Go duration such as 10s or 1m30s, got '${!knob}'"
  done
  [ -f "$PROVIDER_DIR/.env" ] || perf_die "no $PROVIDER_DIR/.env; run '$0 setup' first"
  build_drill
  check_disk
  smoke_check

  local run_dir
  run_dir="$(perf_run_dir "$SUITE" "$LABEL")"
  # The drill writes its evidence, report, per-window log and journal under
  # the provider directory, with a random run id in each file name. A fresh
  # directory per run leaves exactly one of each, already inside the run
  # directory, and no journal from an interrupted run to block the next one.
  mkdir -p "$run_dir/drill"
  ln -s ../../provider/.env "$run_dir/drill/.env"

  local tenant
  tenant="$(aws configure get --profile "$AWS_CLI_PROFILE" tenant_id 2>/dev/null)" \
    || perf_die "profile $AWS_CLI_PROFILE has no tenant_id; run '$0 setup' first"
  perf_metadata "$run_dir" "$LABEL" "$(jq -cn \
    --arg profile "$PROFILE" --arg stop_ingest_at "$STOP_INGEST_AT" --arg ramp "$RAMP" --arg window "$WINDOW" \
    --arg verify_lag_min "$VERIFY_LAG_MIN" --arg verify_lag_max "$VERIFY_LAG_MAX" \
    --argjson workers "$WORKERS" --arg duration "$DURATION" --arg rate_target "${RATE_TARGET:-}" \
    --arg tenant "$tenant" \
    --argjson needed_gb "$DISK_NEEDED_GB" --argjson host_free_gb "$DISK_HOST_FREE_GB" \
    --argjson docker_free_gb "$DISK_DOCKER_FREE_GB" \
    --arg storage_qualification "$(perf_git_info "$SQ_DIR")" \
    'def nullable: if . == "" then null else . end;
     {profile: $profile, stop_ingest_at: $stop_ingest_at, ramp: $ramp, window: $window,
      verify_lag_min: $verify_lag_min, verify_lag_max: $verify_lag_max,
      workers: $workers, duration: $duration, rate_target: ($rate_target|nullable),
      tenant: $tenant,
      disk: {needed_gb: $needed_gb, host_free_gb: $host_free_gb, docker_free_gb: $docker_free_gb},
      storage_qualification: ($storage_qualification|fromjson)}')"
  echo "run dir: $run_dir"

  local optional_flags=()
  [ -z "${RATE_TARGET:-}" ] || optional_flags+=(--rate-target "$RATE_TARGET")

  perf_stats_start "$run_dir" "${STATS_SERVICES[@]}"
  trap 'perf_stats_stop' EXIT
  local started
  started="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

  # SQ_* variables in the environment win over .env, so a shell that still
  # exports them from a session against another store would send the drill
  # there. `env -u` drops them; without a notary token the drill also never
  # registers the run with a notary, and without a bucket prefix it names the
  # buckets after its run id, which ingot's global bucket names need. tee
  # ignores SIGINT so that Ctrl-C reaches only the drill, which then writes its
  # evidence and sweeps its buckets without losing its output pipe.
  local status=0
  env -u SQ_ENDPOINT -u SQ_ACCESS_KEY -u SQ_SECRET_KEY -u SQ_INSECURE -u SQ_REGION \
      -u SQ_NOTARY_URL -u SQ_NOTARY_TOKEN -u SQ_BUCKET_PREFIX \
    "$DRILL_BIN" --provider "$run_dir/drill" --profile "$PROFILE" \
      --stop-ingest-at "$STOP_INGEST_AT" --ramp "$RAMP" --window "$WINDOW" \
      --verify-lag-min "$VERIFY_LAG_MIN" --verify-lag-max "$VERIFY_LAG_MAX" \
      --workers "$WORKERS" --duration "$DURATION" \
      ${optional_flags[@]+"${optional_flags[@]}"} \
    2>&1 | (trap '' INT; exec tee "$run_dir/drill.out") || status=$?

  local ended
  ended="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  perf_stats_stop
  trap - EXIT
  perf_dump_logs "$run_dir" "$started" "$ended" "${SERVICES[@]}"
  jq --argjson code "$status" '.suite.drill_exit = $code' "$run_dir/metadata.json" > "$run_dir/metadata.json.tmp"
  mv "$run_dir/metadata.json.tmp" "$run_dir/metadata.json"

  # The cap downgrades the evidence to "not qualified" on every run, so the
  # exit code is the verdict (storage-qualification MANUAL.md): 0 completed
  # with no failures, 1 a failure, 2 a usage error or an interrupt. A failure
  # before the run starts, such as a bucket the store refuses, is 1 with no
  # evidence.
  local evidence=("$run_dir"/drill/evidence/drill-*.json)
  [ -f "${evidence[0]}" ] || evidence=()
  if [ "$status" -eq 1 ] && [ "${#evidence[@]}" -eq 0 ]; then
    perf_die "the drill failed before it wrote any evidence; nothing recorded. See $run_dir/drill.out"
  fi
  case "$status" in
    0|1)
      [ "${#evidence[@]}" -eq 1 ] \
        || perf_die "the drill exited $status but left ${#evidence[@]} evidence files matching $run_dir/drill/evidence/drill-*.json instead of one"
      "$PROJECT/scripts/perf-results.py" record "$SUITE" "$run_dir"
      local report=("$run_dir"/drill/reports/drill-*.md)
      if [ -f "${report[0]}" ]; then
        echo "drill report: ${report[0]}"
      else
        echo "WARNING: the drill wrote no report under $run_dir/drill/reports/" >&2
      fi
      [ "$status" -eq 0 ] || perf_die "the drill recorded a failure (exit 1); see the drill report"
      ;;
    2) perf_die "the drill stopped on a usage error or an interrupt (exit 2); nothing recorded. See $run_dir/drill.out. Objects it wrote stay in the stack until 'make clean'." ;;
    *) perf_die "the drill exited $status; nothing recorded. See $run_dir/drill.out" ;;
  esac
}

# build_drill: build bin/drill from the checkout and check that it has the
# --stop-ingest-at flag. Every run rebuilds, so the binary always matches the
# revision recorded in metadata.json (bin/ is gitignored there).
build_drill() {
  [ -d "$SQ_DIR/cmd/drill" ] || perf_die "storage-qualification checkout not found at $SQ_DIR (set STORAGE_QUALIFICATION_DIR)"
  # GOWORK=off: a go.work above the checkout (the workspace flow keeps one at
  # the fil-forge/ parent) does not list storage-qualification.
  (cd "$SQ_DIR" && GOWORK=off go build -o bin/drill ./cmd/drill) \
    || perf_die "cannot build the drill in $SQ_DIR"
  local help
  help="$("$DRILL_BIN" --help 2>&1 || true)"
  # Go prints flags with a single dash; the pattern matches either form.
  grep -q -e '-stop-ingest-at' <<<"$help" \
    || perf_die "bin/drill has no --stop-ingest-at flag; update $SQ_DIR to a revision that has it"
}

# smoke_check: upload one small object through ingot with the drill's key,
# download it and compare, so a stack that cannot store a blob fails here in
# seconds rather than as a drill run whose every request fails. An upload
# goes ingot -> piri -> indexer, so this catches a broken piri or a stale
# delegation proof as well as a bad key.
smoke_check() {
  local aws=(aws --profile "$AWS_CLI_PROFILE") tenant bucket key tmp
  tenant="$(aws configure get --profile "$AWS_CLI_PROFILE" tenant_id 2>/dev/null)" \
    || perf_die "profile $AWS_CLI_PROFILE has no tenant_id; run '$0 setup' first"
  # Ingot bucket names are global, so the bucket carries the tenant. It is
  # kept between checks; each check writes its own key.
  bucket="drill-smoke-$tenant"
  key="smoke-$(date -u +%Y%m%dT%H%M%SZ)"
  tmp="$(mktemp -d)"
  # A 4 MiB object is one PUT, as the drill's blobs are below 16 MiB.
  head -c 4194304 /dev/urandom > "$tmp/up"
  if ! "${aws[@]}" s3api head-bucket --bucket "$bucket" >/dev/null 2>&1; then
    "${aws[@]}" s3api create-bucket --bucket "$bucket" >/dev/null 2>"$tmp/err" \
      || smoke_fail "creating bucket $bucket" "$tmp"
  fi
  "${aws[@]}" s3 cp --only-show-errors "$tmp/up" "s3://$bucket/$key" 2>"$tmp/err" \
    || smoke_fail "uploading s3://$bucket/$key" "$tmp"
  "${aws[@]}" s3 cp --only-show-errors "s3://$bucket/$key" "$tmp/down" 2>"$tmp/err" \
    || smoke_fail "downloading s3://$bucket/$key" "$tmp"
  cmp -s "$tmp/up" "$tmp/down" \
    || { rm -rf "$tmp"; perf_die "smoke check: s3://$bucket/$key came back with different bytes"; }
  "${aws[@]}" s3 rm --only-show-errors "s3://$bucket/$key" >/dev/null 2>&1 || true
  rm -rf "$tmp"
  echo "smoke check: uploaded and downloaded 4 MiB through ingot"
}

# smoke_fail <step> <tmp-dir>: report a failed smoke check step with the AWS
# CLI's error and where to look next.
smoke_fail() {
  local step="$1" tmp="$2" err
  err="$(cat "$tmp/err")"
  rm -rf "$tmp"
  perf_die "smoke check failed $step:
$err
The drill would fail the same way. Check the stack's logs: docker compose logs ingot piri-0
If piri reports a signature mismatch, the keys and proofs in generated/ are likely out of
sync; 'make regen', then 'make clean && make up' and '$0 setup' regenerates both."
}

# check_disk: refuse to start unless the host and Docker's disk both have
# 2.5x STOP_INGEST_AT free. Ingot's spool keeps every body byte for good
# (fil-forge/ingot#48), and piri frees its copy only minutes after the drill's
# sweep, so both copies of everything ingested are on disk when the run ends.
# On Docker Desktop the VM disk is an image file on the host disk, so the host
# needs the space as well. Sets DISK_NEEDED_GB, DISK_HOST_FREE_GB and
# DISK_DOCKER_FREE_GB (GB = 1e9 bytes, the drill's unit).
check_disk() {
  local gb="${STOP_INGEST_AT%GB}" docker_df
  DISK_NEEDED_GB="$(awk -v n="$gb" 'BEGIN { printf "%.1f", n * 2.5 }')"
  DISK_HOST_FREE_GB="$(df -Pk "$PROJECT" | awk 'NR == 2 { printf "%.1f", $4 * 1024 / 1e9 }')"
  # /data is the ingot-data volume, on the disk Docker keeps its volumes on.
  docker_df="$(cd "$PROJECT" && docker compose exec -T ingot df -Pk /data 2>/dev/null)" \
    || perf_die "cannot read free space in the ingot container; is the stack up? (make up)"
  DISK_DOCKER_FREE_GB="$(awk 'NR == 2 { printf "%.1f", $4 * 1024 / 1e9 }' <<<"$docker_df")"
  [ -n "$DISK_HOST_FREE_GB" ] && [ -n "$DISK_DOCKER_FREE_GB" ] || perf_die "cannot parse df output"

  if ! awk -v host="$DISK_HOST_FREE_GB" -v docker="$DISK_DOCKER_FREE_GB" -v needed="$DISK_NEEDED_GB" \
      'BEGIN { exit !(host >= needed && docker >= needed) }'; then
    perf_die "STOP_INGEST_AT=$STOP_INGEST_AT needs about $DISK_NEEDED_GB GB free, but the host has $DISK_HOST_FREE_GB GB and Docker's disk has $DISK_DOCKER_FREE_GB GB.
Ingot's spool never frees body bytes (fil-forge/ingot#48) and piri frees its copy only minutes after the drill's sweep.
Free space with 'make clean' (drops every volume; then 'make up' and '$0 setup'), raise the Docker Desktop disk limit, or lower STOP_INGEST_AT."
  fi
  echo "disk: need ~$DISK_NEEDED_GB GB; host has $DISK_HOST_FREE_GB GB free, Docker $DISK_DOCKER_FREE_GB GB"
}

case "${1:-}" in
  setup) setup ;;
  run)   run ;;
  -h|--help|help|"") usage ;;
  *) perf_die "unknown command '$1' (setup | run)" ;;
esac
