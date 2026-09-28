#!/bin/sh
# Piri Entrypoint - Initialize and start piri storage node
set -e

# Paths
KEY_FILE="/keys/piri.pem"
WALLET_FILE="/keys/owner-wallet.hex"
BASE_CONFIG_SRC="/config/piri-base-config.toml"
INDEXING_CONFIG="/config/piri-indexing.toml"
OVERRIDES_CONFIG="/config/piri-overrides.toml"
DATA_DIR="/data/piri"
TEMP_DIR="/tmp/piri"
CONFIG_FILE="${DATA_DIR}/piri-config.toml"
BASE_CONFIG="${TEMP_DIR}/piri-base-config.toml"

# Network settings (can be overridden via environment)
LOTUS_ENDPOINT="${LOTUS_ENDPOINT:-ws://blockchain:8545}"
PUBLIC_URL="${PUBLIC_URL:-http://piri:3000}"
PORT="${PORT:-3000}"
HOST="${HOST:-0.0.0.0}"
OPERATOR_EMAIL="${OPERATOR_EMAIL:-local@test.com}"
REGISTRAR_URL="${REGISTRAR_URL:-http://delegator:80}"

# Storage backend selection (independent axes)
DB_BACKEND="${PIRI_DB_BACKEND:-sqlite}"
BLOB_BACKEND="${PIRI_BLOB_BACKEND:-filesystem}"

# PostgreSQL settings (used when DB_BACKEND=postgres)
DB_POSTGRES_URL="${PIRI_DB_POSTGRES_URL:-postgres://piri:piri@piri-postgres:5432/piri?sslmode=disable}"
DB_POSTGRES_MAX_OPEN_CONNS="${PIRI_DB_POSTGRES_MAX_OPEN_CONNS:-10}"
DB_POSTGRES_MAX_IDLE_CONNS="${PIRI_DB_POSTGRES_MAX_IDLE_CONNS:-5}"
DB_POSTGRES_CONN_MAX_LIFETIME="${PIRI_DB_POSTGRES_CONN_MAX_LIFETIME:-30m}"

# S3 settings (used when BLOB_BACKEND=s3)
S3_ENDPOINT="${PIRI_S3_ENDPOINT:-piri-minio:9000}"
S3_BUCKET_PREFIX="${PIRI_S3_BUCKET_PREFIX:-piri-}"
S3_ACCESS_KEY_ID="${PIRI_S3_ACCESS_KEY_ID:-minioadmin}"
S3_SECRET_ACCESS_KEY="${PIRI_S3_SECRET_ACCESS_KEY:-minioadmin}"
S3_INSECURE="${PIRI_S3_INSECURE:-true}"

# An endpoint outside the stack needs its own credentials. Refuse to start
# rather than send the stack MinIO's root login to it.
if [ "$BLOB_BACKEND" = "s3" ] && [ "$S3_ENDPOINT" != "piri-minio:9000" ]; then
    if [ -z "${PIRI_S3_ACCESS_KEY_ID:-}" ] || [ -z "${PIRI_S3_SECRET_ACCESS_KEY:-}" ]; then
        echo "ERROR: S3 endpoint $S3_ENDPOINT is outside the stack but PIRI_S3_ACCESS_KEY_ID or PIRI_S3_SECRET_ACCESS_KEY is empty." >&2
        echo "       Set SMELT_PIRI_S3_ACCESS_KEY_ID and SMELT_PIRI_S3_SECRET_ACCESS_KEY in the shell that runs compose." >&2
        exit 1
    fi
fi

# Indexer claims and IPNI announce: on (default) or off, as on dev and
# staging. Read at init only; an initialized node keeps its config.
INDEXER="${PIRI_INDEXER:-on}"
case "$INDEXER" in
    on|off) ;;
    *)
        echo "ERROR: PIRI_INDEXER must be on or off, got '$INDEXER'"
        exit 1
        ;;
esac

# Trace export: an OTLP/HTTP collector URL, or empty for none. piri takes
# collectors only from [[telemetry.traces]], as host:port plus an insecure
# flag for plain HTTP; it posts to /v1/traces on that host. compose fills
# OTEL_ENDPOINT from OTEL_EXPORTER_OTLP_ENDPOINT when only that one is set, so
# errors name both. They never echo the value: it may carry credentials.
TRACES_URL="${OTEL_ENDPOINT:-}"
TRACES_VAR="OTEL_ENDPOINT (or OTEL_EXPORTER_OTLP_ENDPOINT in the shell that ran compose)"
TRACES_MARKER="# --- smelt trace export ---"
TRACES_HOST=""
TRACES_INSECURE=false
case "$TRACES_URL" in
    "") ;;
    http://*) TRACES_HOST="${TRACES_URL#http://}"; TRACES_INSECURE=true ;;
    https://*) TRACES_HOST="${TRACES_URL#https://}" ;;
    *)
        echo "ERROR: $TRACES_VAR must be an http:// or https:// URL"
        exit 1
        ;;
esac
if [ -n "$TRACES_URL" ]; then
    case "$TRACES_HOST" in
        */?*) echo "WARNING: piri ignores the path in $TRACES_VAR and posts to /v1/traces" ;;
    esac
    TRACES_HOST="${TRACES_HOST%%/*}"
    if [ -z "$TRACES_HOST" ]; then
        echo "ERROR: $TRACES_VAR names no host"
        exit 1
    fi
    # piri's endpoint is a bare host:port with no place for credentials, and
    # the host is logged below and written into the config.
    case "$TRACES_HOST" in
        *@*)
            echo "ERROR: $TRACES_VAR must not carry credentials; piri cannot send them"
            exit 1
            ;;
    esac
fi

echo "=== Piri Entrypoint ==="
echo "  Database backend: $DB_BACKEND"
echo "  Blob backend: $BLOB_BACKEND"
echo "  Indexer claims and IPNI announce: $INDEXER"
echo "  Trace export: ${TRACES_HOST:-off}"

# config_has_indexer FILE: true when FILE's [ucan.services.indexer] table
# sets a url.
config_has_indexer() {
    awk '
        /^[[:space:]]*\[/ { t = ($0 ~ /^[[:space:]]*\[ucan\.services\.indexer\][[:space:]]*$/) }
        t && /^[[:space:]]*url[[:space:]]*=/ { found = 1 }
        END { exit !found }
    ' "$1"
}

# Ensure directories exist
mkdir -p "$DATA_DIR" "$TEMP_DIR"

# Step 1: Extract piri's DID from key file
echo "[1/4] Extracting piri DID..."
PIRI_DID=$(/usr/bin/piri identity parse "$KEY_FILE" 2>&1 | grep -oE 'did:key:z[a-zA-Z0-9]+')
if [ -z "$PIRI_DID" ]; then
    echo "ERROR: Failed to extract DID from $KEY_FILE"
    exit 1
fi
echo "  DID: $PIRI_DID"

# Step 2: Register DID with delegator allow list
echo "[2/4] Registering DID with allow list..."
/scripts/register-did.sh "$PIRI_DID" || echo "Warning: Registration failed, continuing..."

# Step 3: Initialize piri (if not already initialized)
echo "[3/4] Initializing piri..."
if [ -f "$CONFIG_FILE" ] && grep -q "proof_set" "$CONFIG_FILE" 2>/dev/null; then
    echo "  Config exists, skipping init"
    if [ "$INDEXER" = "off" ] && config_has_indexer "$CONFIG_FILE"; then
        echo "WARNING: PIRI_INDEXER=off applies at init only, and this node's config"
        echo "  still sends claims to the indexer. Run 'make clean' to re-initialize."
    fi
else
    [ -f "$CONFIG_FILE" ] && rm -f "$CONFIG_FILE"

    # Assemble the base config: the indexer tables are appended unless off.
    cp "$BASE_CONFIG_SRC" "$BASE_CONFIG"
    if [ "$INDEXER" = "on" ]; then
        cat "$INDEXING_CONFIG" >> "$BASE_CONFIG"
    fi

    cd "$DATA_DIR"

    # Build init command with base flags. --plc-directory points did:plc
    # resolution at the in-network PLC container (hilt publishes tenant
    # did:plc genesis ops there); without it piri defaults to the public
    # https://plc.directory and can't resolve local tenants, so the
    # ingot->piri /content/retrieve read path fails chain validation.
    INIT_CMD="/usr/bin/piri init \
        --base-config=$BASE_CONFIG \
        --registrar-url=$REGISTRAR_URL \
        --plc-directory=${PIRI_PLC_DIRECTORY:-http://plc:3000} \
        --data-dir=$DATA_DIR \
        --temp-dir=$TEMP_DIR \
        --key-file=$KEY_FILE \
        --wallet-file=$WALLET_FILE \
        --lotus-endpoint=$LOTUS_ENDPOINT \
        --public-url=$PUBLIC_URL \
        --port=$PORT \
        --host=$HOST \
        --operator-email=$OPERATOR_EMAIL"

    # Add PostgreSQL flags if postgres backend selected
    if [ "$DB_BACKEND" = "postgres" ]; then
        INIT_CMD="$INIT_CMD \
            --db-type=postgres \
            --db-postgres-url=$DB_POSTGRES_URL \
            --db-postgres-max-open-conns=$DB_POSTGRES_MAX_OPEN_CONNS \
            --db-postgres-max-idle-conns=$DB_POSTGRES_MAX_IDLE_CONNS \
            --db-postgres-conn-max-lifetime=$DB_POSTGRES_CONN_MAX_LIFETIME"
    fi

    # Add S3 flags if s3 backend selected
    if [ "$BLOB_BACKEND" = "s3" ]; then
        INIT_CMD="$INIT_CMD \
            --s3-endpoint=$S3_ENDPOINT \
            --s3-bucket-prefix=$S3_BUCKET_PREFIX \
            --s3-access-key-id=$S3_ACCESS_KEY_ID \
            --s3-secret-access-key=$S3_SECRET_ACCESS_KEY"
        if [ "$S3_INSECURE" = "true" ]; then
            INIT_CMD="$INIT_CMD --s3-insecure"
        fi
    fi

    # Execute the init command
    eval "$INIT_CMD"

    # Config created as piri-config.toml in DATA_DIR (current dir)
    echo "  Init complete"
fi

# Append overrides config if present and not already applied
if [ -f "$OVERRIDES_CONFIG" ]; then
    # Check if overrides already appended (look for marker comment)
    if ! grep -q "# --- piri-overrides.toml ---" "$CONFIG_FILE" 2>/dev/null; then
        echo "  Applying config overrides..."
        {
            echo ""
            echo "# --- piri-overrides.toml ---"
            cat "$OVERRIDES_CONFIG"
        } >> "$CONFIG_FILE"
    fi
fi

# Trace collector: rewritten on every boot, so it follows OTEL_ENDPOINT on an
# initialized node or one loaded from a snapshot. The block always ends the
# file. A config without the marker is never rewritten, so with OTEL_ENDPOINT
# empty it stays byte for byte what init and the overrides produced.
if grep -qxF "$TRACES_MARKER" "$CONFIG_FILE" 2>/dev/null; then
    # Drop the marker, everything after it and the blank line before it.
    awk -v m="$TRACES_MARKER" '
        $0 == m { exit }
        { lines[n++] = $0 }
        END {
            if (n && lines[n-1] == "") n--
            for (i = 0; i < n; i++) print lines[i]
        }
    ' "$CONFIG_FILE" > "$CONFIG_FILE.tmp"
    mv "$CONFIG_FILE.tmp" "$CONFIG_FILE"
fi
if [ -n "$TRACES_HOST" ]; then
    {
        echo ""
        echo "$TRACES_MARKER"
        echo "[[telemetry.traces]]"
        echo "endpoint = \"$TRACES_HOST\""
        echo "insecure = $TRACES_INSECURE"
    } >> "$CONFIG_FILE"
fi

# Step 4: Start piri server
echo "[4/4] Starting piri..."
exec /usr/bin/piri serve full --config "$CONFIG_FILE" "$@"
