#!/bin/sh
# Piri entrypoint — STAGING.
#
# Adapted from systems/piri/entrypoint.sh for the split staging deployment. The
# key difference: piri registers via the delegator's registrar HTTP API
# (--registrar-url), NOT by writing directly to DynamoDB. The core bundle's
# DynamoDB is not reachable from the piri bundle, so the dev register-did.sh step
# is intentionally omitted.
set -e

KEY_FILE="/keys/piri.pem"
WALLET_FILE="/keys/owner-wallet.hex"
BASE_CONFIG="/config/piri-base-config.toml"
DATA_DIR="/data/piri"
TEMP_DIR="/tmp/piri"
CONFIG_FILE="${DATA_DIR}/piri-config.toml"

LOTUS_ENDPOINT="${LOTUS_ENDPOINT:?LOTUS_ENDPOINT must be set}"
PUBLIC_URL="${PUBLIC_URL:?PUBLIC_URL must be set}"
PORT="${PORT:-3000}"
HOST="${HOST:-0.0.0.0}"
OPERATOR_EMAIL="${OPERATOR_EMAIL:?OPERATOR_EMAIL must be set}"
REGISTRAR_URL="${REGISTRAR_URL:?REGISTRAR_URL must be set}"

DB_BACKEND="${PIRI_DB_BACKEND:-sqlite}"
BLOB_BACKEND="${PIRI_BLOB_BACKEND:-filesystem}"

echo "=== Piri Entrypoint (staging) ==="
echo "  Lotus:      $LOTUS_ENDPOINT"
echo "  Public URL: $PUBLIC_URL"
echo "  Registrar:  $REGISTRAR_URL"
echo "  Backends:   db=$DB_BACKEND blob=$BLOB_BACKEND"

mkdir -p "$DATA_DIR" "$TEMP_DIR"

echo "[1/3] Extracting piri DID..."
# Run the parse on its own line: capturing it inside a `PIRI_DID=$(... | grep)`
# assignment lets `set -e` abort on a grep no-match (or a parse failure) BEFORE
# the checks below run, swallowing the real error — e.g. an unreadable key file
# reports nothing but the silent crash-loop. Separating the steps surfaces the
# actual stderr (`|| true` on grep so we reach the explicit, informative checks).
if ! PARSE_OUTPUT=$(/usr/bin/piri identity parse "$KEY_FILE" 2>&1); then
    echo "ERROR: 'piri identity parse $KEY_FILE' failed:" >&2
    echo "$PARSE_OUTPUT" >&2
    exit 1
fi
PIRI_DID=$(printf '%s\n' "$PARSE_OUTPUT" | grep -oE 'did:key:z[a-zA-Z0-9]+' || true)
if [ -z "$PIRI_DID" ]; then
    echo "ERROR: no did:key found in 'piri identity parse $KEY_FILE' output:" >&2
    echo "$PARSE_OUTPUT" >&2
    exit 1
fi
echo "  DID: $PIRI_DID"

echo "[2/3] Initializing piri..."
if [ -f "$CONFIG_FILE" ] && grep -q "proof_set" "$CONFIG_FILE" 2>/dev/null; then
    echo "  Config exists, skipping init"
else
    [ -f "$CONFIG_FILE" ] && rm -f "$CONFIG_FILE"
    cd "$DATA_DIR"
    INIT_CMD="/usr/bin/piri init \
        --base-config=$BASE_CONFIG \
        --registrar-url=$REGISTRAR_URL \
        --data-dir=$DATA_DIR \
        --temp-dir=$TEMP_DIR \
        --key-file=$KEY_FILE \
        --wallet-file=$WALLET_FILE \
        --lotus-endpoint=$LOTUS_ENDPOINT \
        --public-url=$PUBLIC_URL \
        --port=$PORT \
        --host=$HOST \
        --operator-email=$OPERATOR_EMAIL"

    if [ "$DB_BACKEND" = "postgres" ]; then
        INIT_CMD="$INIT_CMD \
            --db-type=postgres \
            --db-postgres-url=${PIRI_DB_POSTGRES_URL:?PIRI_DB_POSTGRES_URL required for postgres backend}"
    fi
    if [ "$BLOB_BACKEND" = "s3" ]; then
        INIT_CMD="$INIT_CMD \
            --s3-endpoint=${PIRI_S3_ENDPOINT:?PIRI_S3_ENDPOINT required for s3 backend} \
            --s3-bucket-prefix=${PIRI_S3_BUCKET_PREFIX:-piri-0-} \
            --s3-access-key-id=${PIRI_S3_ACCESS_KEY_ID:?PIRI_S3_ACCESS_KEY_ID required} \
            --s3-secret-access-key=${PIRI_S3_SECRET_ACCESS_KEY:?PIRI_S3_SECRET_ACCESS_KEY required}"
    fi

    eval "$INIT_CMD"
    echo "  Init complete"
fi

echo "[3/3] Starting piri..."
exec /usr/bin/piri serve full --config "$CONFIG_FILE" "$@"
