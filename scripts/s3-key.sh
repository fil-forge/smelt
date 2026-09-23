#!/usr/bin/env bash
# Mint an S3 access key for the local Forge network and store it as an AWS
# CLI profile, so `aws --profile smelt s3 ...` talks to the ingot in this
# stack. Creates the hilt tenant when it does not exist yet.
#
#   make s3-key                       # tenant "dev", profile "smelt"
#   make s3-key TENANT=perf PROFILE=smelt-perf
#
# The region is read from the running ingot container (INGOT_REGION): it is
# the region hilt registered ingot for and the region clients must sign with.
# The secret access key is written only to the AWS CLI credentials file (hilt
# returns it once); this script never prints it.
#
# Needs: a running stack, docker, curl, jq, and AWS CLI v2 (2.13+, for the per-profile
# endpoint_url setting).
set -euo pipefail

TENANT="${TENANT:-dev}"
PROFILE="${PROFILE:-smelt}"
# Hilt requires key names to be unique per tenant, so each run mints a new one.
KEY_NAME="${KEY_NAME:-$PROFILE-$(date -u +%Y%m%dT%H%M%SZ)}"
PERMISSIONS='[
  "s3:GetObject", "s3:GetObjectVersion", "s3:GetObjectRetention", "s3:GetObjectLegalHold",
  "s3:ListBucket", "s3:ListBucketVersions",
  "s3:PutObject", "s3:PutObjectRetention", "s3:PutObjectLegalHold",
  "s3:DeleteObject", "s3:DeleteObjectVersion",
  "s3:CreateBucket", "s3:ListAllMyBuckets", "s3:DeleteBucket",
  "s3:AbortMultipartUpload", "s3:ListMultipartUploadParts", "s3:ListBucketMultipartUploads"
]'

die() { echo "ERROR: $*" >&2; exit 1; }

for tool in curl jq aws docker; do
  command -v "$tool" >/dev/null 2>&1 || die "$tool is required"
done

cd "$(dirname "${BASH_SOURCE[0]}")/.."

region="$(docker compose exec -T ingot printenv INGOT_REGION 2>/dev/null)" \
  || die "cannot read INGOT_REGION from the ingot container; is the stack up? (make up)"
[ -n "$region" ] || die "ingot container has no INGOT_REGION set"

# The partner key and the two URLs come from the running stack, so the
# HILT_PARTNER_KEY and SMELT_*_PORT overrides given to `make up` carry over.
# Setting HILT_PARTNER_KEY, HILT_URL or INGOT_URL for this script bypasses that.
PARTNER_KEY="${HILT_PARTNER_KEY:-}"
if [ -z "$PARTNER_KEY" ]; then
  PARTNER_KEY="$(docker compose exec -T hilt printenv HILT_AUTH_PARTNER_KEY 2>/dev/null)" \
    || die "cannot read HILT_AUTH_PARTNER_KEY from the hilt container; is the stack up? (make up)"
fi

# published_url <service> <container-port>: http://localhost:<host-port>
published_url() {
  local port
  port="$(docker compose port "$1" "$2" 2>/dev/null)" \
    || die "$1 does not publish port $2; is the stack up? (make up)"
  echo "http://localhost:${port##*:}"
}
HILT_URL="${HILT_URL:-}"
if [ -z "$HILT_URL" ]; then HILT_URL="$(published_url hilt 80)"; fi
INGOT_URL="${INGOT_URL:-}"
if [ -z "$INGOT_URL" ]; then INGOT_URL="$(published_url ingot 80)"; fi

auth=(-H "Authorization: Bearer $PARTNER_KEY" -H "Content-Type: application/json")

body="$(mktemp)"
trap 'rm -f "$body"' EXIT

status="$(curl -sS -o "$body" -w '%{http_code}' -X PUT "$HILT_URL/tenants/$TENANT" \
  "${auth[@]}" -d "{\"region\":\"$region\"}")" \
  || die "hilt unreachable at $HILT_URL"
# PUT is idempotent on the tenant id (200 when it already exists, 201 when created).
case "$status" in
  200|201) echo "tenant $TENANT ready (region $region)" ;;
  *)       die "create tenant $TENANT: HTTP $status: $(cat "$body")" ;;
esac

status="$(curl -sS -o "$body" -w '%{http_code}' -X POST "$HILT_URL/tenants/$TENANT/access-keys" \
  "${auth[@]}" -d "{\"name\":\"$KEY_NAME\",\"permissions\":$PERMISSIONS}")" \
  || die "hilt unreachable at $HILT_URL"
[ "$status" = "200" ] || [ "$status" = "201" ] \
  || die "create access key for $TENANT: HTTP $status: $(cat "$body")"

access_key_id="$(jq -r '.accessKeyId // empty' "$body")"
secret_access_key="$(jq -r '.secretAccessKey // empty' "$body")"
[ -n "$access_key_id" ] && [ -n "$secret_access_key" ] \
  || die "hilt returned incomplete credentials: $(jq -c 'del(.secretAccessKey)' "$body")"

# The secret passes through argv here, visible to other local users in the
# process list for the instant the command runs. This is a single-user dev
# box minting a key for a local stack, so that is accepted over the
# alternative of staging a credentials CSV on disk for `aws configure import`.
aws configure set --profile "$PROFILE" aws_access_key_id "$access_key_id"
aws configure set --profile "$PROFILE" aws_secret_access_key "$secret_access_key"
aws configure set --profile "$PROFILE" region "$region"
aws configure set --profile "$PROFILE" endpoint_url "$INGOT_URL"
# Ingot serves path-style requests only.
aws configure set --profile "$PROFILE" s3.addressing_style path

cat <<EOF

Access key $access_key_id (tenant $TENANT, region $region) saved to AWS CLI profile "$PROFILE".
Try it:

  aws --profile $PROFILE s3 mb s3://my-bucket
  aws --profile $PROFILE s3 cp README.md s3://my-bucket/
  aws --profile $PROFILE s3 ls s3://my-bucket/
EOF
