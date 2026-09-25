#!/usr/bin/env bash
# Run the fil-one/s3-speedtests upload + download benchmark against the
# ingot in this stack and keep everything needed to compare runs later.
#
#   ./scripts/perf-s3-speedtest.sh setup      # once per stack: tenant, key, bucket, test files
#   LABEL=before ./scripts/perf-s3-speedtest.sh run
#   # edit ingot / sprue / piri, then: make redeploy
#   LABEL=after  ./scripts/perf-s3-speedtest.sh run
#   ./scripts/perf-results.py compare s3-speedtest before after
#
# Environment:
#   S3_SPEEDTESTS_DIR  checkout of fil-one/s3-speedtests (default: fil-one/s3-speedtests
#                      beside the fil-forge/ directory this smelt checkout lives in)
#   TESTFILES_DIR      where the random_*.bin payloads live (default generated/perf/testfiles)
#   FILE_SET           quick | standard | large | full (default quick)
#   LABEL              run label, required for `run`
#   RUNS               repeats per file (default 1)
#   SNAPSHOT           run: `make down && make up SNAPSHOT=...` first, then re-run setup
#                      (hilt's dev vault is in memory, so a restore loses the access key).
#                      With SMELT_MANIFEST also set, the two manifests must match.
#
# Output: generated/perf-runs/s3-speedtest/<utc-ts>-<label>/ plus one row per
# (operation, file size) appended to generated/perf-runs/s3-speedtest/runs.jsonl.
set -euo pipefail

# shellcheck source=perf-lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/perf-lib.sh"

SUITE=s3-speedtest
PROJECT="$(perf_project_dir)"
# Go-style layout: github.com/fil-forge/smelt and github.com/fil-one/s3-speedtests
# are checked out as <root>/fil-forge/smelt and <root>/fil-one/s3-speedtests.
S3_SPEEDTESTS_DIR="${S3_SPEEDTESTS_DIR:-$(dirname "$(dirname "$PROJECT")")/fil-one/s3-speedtests}"
TESTFILES_DIR="${TESTFILES_DIR:-$PROJECT/generated/perf/testfiles}"
FILE_SET="${FILE_SET:-quick}"
RUNS="${RUNS:-1}"
TENANT=perf
PROFILE=smelt-perf
# The bucket is named after the tenant the profile ended up on (see
# perf_bucket): ingot bucket names are global, and after a restart s3-key.sh
# may move to perf-2, which cannot reuse the bucket perf created.
BUCKET_PREFIX=perf-s3-speedtest
TARGETS_TEMPLATE="$PROJECT/generated/perf/s3_targets.ini"
# Containers whose CPU/memory and logs are captured around each run.
SERVICES=(ingot upload piri-0 hilt)
STATS_SERVICES=(ingot upload piri-0 piri-postgres ingot-postgres)

usage() { sed -n '2,/^set -euo/p' "$0" | sed '$d' | sed 's/^# \{0,1\}//'; }

setup() {
  perf_require aws jq docker python3
  [ -d "$S3_SPEEDTESTS_DIR/scripts" ] || perf_die "s3-speedtests checkout not found at $S3_SPEEDTESTS_DIR (set S3_SPEEDTESTS_DIR)"

  perf_wait_healthy hilt
  perf_wait_healthy ingot
  (cd "$PROJECT" && TENANT="$TENANT" PROFILE="$PROFILE" ./scripts/s3-key.sh)
  local region bucket
  region="$(aws configure get --profile "$PROFILE" region)"
  bucket="$(perf_bucket)"

  if ! aws --profile "$PROFILE" s3api head-bucket --bucket "$bucket" >/dev/null 2>&1; then
    aws --profile "$PROFILE" s3api create-bucket --bucket "$bucket" >/dev/null
    echo "created bucket $bucket"
  fi

  mkdir -p "$(dirname "$TARGETS_TEMPLATE")"
  cat > "$TARGETS_TEMPLATE" <<EOF
# Written by scripts/perf-s3-speedtest.sh setup. The run step copies this
# file with a per-run prefix so the download phase only sees that run's
# objects. Credentials live in the AWS CLI profile, not here.
[smelt-ingot]
enabled = true
provider = smelt-ingot
display_name = Smelt Ingot
bucket = $bucket
region = $region
location = localhost
endpoint_url = http://localhost:15130
prefix = perf
profile = $PROFILE
auth_mode = profile
request_checksum_calculation = when_required
response_checksum_validation = when_required
aws_max_attempts = 11
extra_args =
EOF
  echo "wrote $TARGETS_TEMPLATE"

  "$S3_SPEEDTESTS_DIR/scripts/generate_test_files.sh" "$TESTFILES_DIR" "$FILE_SET"
}

run() {
  perf_require aws jq docker python3 go
  [ -n "${LABEL:-}" ] || perf_die "LABEL is required, e.g. LABEL=before $0 run"
  [ -f "$TARGETS_TEMPLATE" ] || perf_die "no $TARGETS_TEMPLATE; run '$0 setup' first"
  [ -d "$S3_SPEEDTESTS_DIR/scripts" ] || perf_die "s3-speedtests checkout not found at $S3_SPEEDTESTS_DIR (set S3_SPEEDTESTS_DIR)"
  # setup generated the file set it was given; this run's FILE_SET may be a
  # bigger one. The generator keeps files that already exist, so this is cheap.
  "$S3_SPEEDTESTS_DIR/scripts/generate_test_files.sh" "$TESTFILES_DIR" "$FILE_SET"

  if [ -n "${SNAPSHOT:-}" ]; then
    # `make up` drops the workspace override unless SMELT_WORKSPACE=1 is set,
    # which would silently benchmark the published images instead of the
    # local build.
    if [ -f "$PROJECT/generated/compose/workspace.override.yml" ] && [ "${SMELT_WORKSPACE:-0}" != "1" ]; then
      perf_die "the stack runs workspace binaries but SMELT_WORKSPACE is not 1; export SMELT_WORKSPACE=1 so the snapshot restore keeps them"
    fi
    check_snapshot_manifest
    (cd "$PROJECT" && make down && make up SNAPSHOT="$SNAPSHOT")
    setup
  fi

  local run_dir bucket
  bucket="$(perf_bucket)"
  run_dir="$(perf_run_dir "$SUITE" "$LABEL")"
  local run_prefix="perf/$(basename "$run_dir")"
  sed "s|^prefix = .*|prefix = $run_prefix|" "$TARGETS_TEMPLATE" > "$run_dir/s3_targets.ini"

  local chunksize concurrency
  chunksize="$(aws configure get --profile "$PROFILE" s3.multipart_chunksize 2>/dev/null || echo "default (8MB)")"
  concurrency="$(aws configure get --profile "$PROFILE" s3.max_concurrent_requests 2>/dev/null || echo "default (10)")"
  perf_metadata "$run_dir" "$LABEL" "$(jq -cn \
    --arg file_set "$FILE_SET" --arg runs "$RUNS" --arg prefix "$run_prefix" --arg bucket "$bucket" \
    --arg aws_version "$(aws --version 2>&1)" --arg chunksize "$chunksize" --arg concurrency "$concurrency" \
    --arg s3_speedtests "$(perf_git_info "$S3_SPEEDTESTS_DIR")" \
    '{file_set: $file_set, runs: ($runs|tonumber), prefix: $prefix, bucket: $bucket,
      aws_cli: {version: $aws_version, multipart_chunksize: $chunksize, max_concurrent_requests: $concurrency},
      s3_speedtests: ($s3_speedtests|fromjson)}')"
  echo "run dir: $run_dir"

  local downloads="$run_dir/downloads"
  mkdir -p "$downloads"
  perf_stats_start "$run_dir" "${STATS_SERVICES[@]}"
  trap 'perf_stats_stop' EXIT
  local started
  started="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

  local status=0
  python3 "$S3_SPEEDTESTS_DIR/scripts/s3_upload_speedtest.py" \
    --targets "$run_dir/s3_targets.ini" --output-dir "$run_dir/speedtest" \
    --testfiles-dir "$TESTFILES_DIR" --file-set "$FILE_SET" --runs "$RUNS" \
    2>&1 | tee "$run_dir/upload.out" || status=$?
  python3 "$S3_SPEEDTESTS_DIR/scripts/s3_download_speedtest.py" \
    --targets "$run_dir/s3_targets.ini" --output-dir "$run_dir/speedtest" \
    --downloads-dir "$downloads" --file-set "$FILE_SET" --runs "$RUNS" \
    2>&1 | tee "$run_dir/download.out" || status=$?

  local ended
  ended="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  perf_stats_stop
  trap - EXIT
  rm -rf "$downloads"
  perf_dump_logs "$run_dir" "$started" "$ended" "${SERVICES[@]}"

  "$PROJECT/scripts/perf-results.py" record "$SUITE" "$run_dir"
  [ "$status" -eq 0 ] || perf_die "a speedtest step failed (exit $status); see $run_dir/*.out"
}

# perf_bucket: the suite's bucket for the tenant the AWS profile belongs to
# (s3-key.sh records that tenant as `tenant_id` in the profile).
perf_bucket() {
  local tenant
  tenant="$(aws configure get --profile "$PROFILE" tenant_id 2>/dev/null)" \
    || perf_die "profile $PROFILE has no tenant_id; run '$0 setup' first"
  echo "$BUCKET_PREFIX-$tenant"
}

# check_snapshot_manifest: SMELT_MANIFEST takes precedence over the manifest a
# snapshot installs as its session, for the restore and for every make target
# after it. A mismatch would boot the override's services on the snapshot's
# volumes, keys and chain state, so refuse unless the two files agree.
check_snapshot_manifest() {
  [ -n "${SMELT_MANIFEST:-}" ] || return 0
  local override="$SMELT_MANIFEST" snap_dir="$SNAPSHOT"
  [[ "$override" = /* ]] || override="$PROJECT/$override"
  [[ "$snap_dir" = */* ]] || snap_dir="$PROJECT/generated/snapshots/$snap_dir"
  [ -f "$snap_dir/smelt.yml" ] || perf_die "no smelt.yml in snapshot $SNAPSHOT ($snap_dir)"
  cmp -s "$override" "$snap_dir/smelt.yml" \
    || perf_die "SMELT_MANIFEST ($SMELT_MANIFEST) differs from the manifest of snapshot $SNAPSHOT; unset it or pick a snapshot with the same topology"
}

case "${1:-}" in
  setup) setup ;;
  run)   run ;;
  -h|--help|help|"") usage ;;
  *) perf_die "unknown command '$1' (setup | run)" ;;
esac
