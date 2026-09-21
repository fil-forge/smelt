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
# Needs: a running stack, curl, jq, and AWS CLI v2 (2.13+, for the per-profile
# endpoint_url setting).
set -euo pipefail

TENANT="${TENANT:-dev}"
PROFILE="${PROFILE:-smelt}"
# Hilt requires key names to be unique per tenant, so each run mints a new one.
KEY_NAME="${KEY_NAME:-$PROFILE-$(date -u +%Y%m%dT%H%M%SZ)}"
HILT_URL="${HILT_URL:-http://localhost:15110}"
INGOT_URL="${INGOT_URL:-http://localhost:15130}"
# Same default as HILT_AUTH_PARTNER_KEY in systems/hilt/compose.yml.
PARTNER_KEY="${HILT_PARTNER_KEY:-dev-partner-key}"

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
  "${auth[@]}" -d "{\"name\":\"$KEY_NAME\",\"permissions\":$PERMISSIONS}")"
[ "$status" = "200" ] || [ "$status" = "201" ] \
  || die "create access key for $TENANT: HTTP $status: $(cat "$body")"

access_key_id="$(jq -r '.accessKeyId // empty' "$body")"
secret_access_key="$(jq -r '.secretAccessKey // empty' "$body")"
[ -n "$access_key_id" ] && [ -n "$secret_access_key" ] \
  || die "hilt returned incomplete credentials: $(jq -c 'del(.secretAccessKey)' "$body")"

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
