#!/bin/bash
# Generate UCAN delegation proofs for service communication
#
# This script generates the delegation proofs needed for services to communicate:
# - indexing-service-proof: indexer delegates claim/cache to delegator
# - egress-tracking-proof: etracker delegates egress/track to delegator
# - piri-N-proof: each piri node delegates blob/* + pdp/info to upload
# - hilt-customer-add-proof: upload delegates customer/add to hilt
# - hilt-ingot-s3-proof: hilt delegates the /s3/* commands to ingot
#
# Prerequisites:
# - Keys must exist in generated/keys/ (run 'make generate' first)
# - ucantool CLI must be installed (go install github.com/fil-forge/ucantool@latest)
#
# Usage: ./generate-proofs.sh [--force]
#   --force: Regenerate all proofs even if they exist

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
KEYS_DIR="$SCRIPT_DIR/keys"
PROOFS_DIR="$SCRIPT_DIR/proofs"
FORCE=${1:-""}

# Check for ucantool
UCANTOOL="${UCANTOOL:-ucantool}"
if ! command -v "$UCANTOOL" &> /dev/null; then
    echo "Error: ucantool not found in PATH"
    echo "Install with: go install github.com/fil-forge/ucantool@latest"
    exit 1
fi

# Check for required keys
check_key() {
    local key_file="$1"
    if [[ ! -f "$key_file" ]]; then
        echo "Error: Required key file not found: $key_file"
        echo "Run 'make generate' first"
        exit 1
    fi
}

echo "Generating delegation proofs in $PROOFS_DIR..."
echo ""

mkdir -p "$PROOFS_DIR"

# Check required keys exist
check_key "$KEYS_DIR/indexer.pem"
check_key "$KEYS_DIR/delegator.pem"
check_key "$KEYS_DIR/etracker.pem"
check_key "$KEYS_DIR/upload.pem"
# Per-node piri keys (piri-0.pem, piri-1.pem, ...) are checked by the loop below.

# The delegator identifies as did:web:delegator when signing delegations to providers.
# The proofs must have audience=did:web:delegator so the UCAN chain is valid.
DELEGATOR_WEB_DID="did:web:delegator"
echo "Using delegator DID: $DELEGATOR_WEB_DID"

UPLOAD_WEB_DID="did:web:upload"
echo "Using upload DID: $UPLOAD_WEB_DID"

# Generate indexing service proof (indexer → delegator, claim/cache capability)
INDEXING_PROOF_FILE="$PROOFS_DIR/indexing-service-proof.txt"
if [[ -f "$INDEXING_PROOF_FILE" && "$FORCE" != "--force" ]]; then
    echo ""
    echo "[skip] indexing-service-proof.txt already exists"
else
    echo ""
    echo "Generating indexing service proof..."
    echo "  Issuer: did:web:indexer (key: indexer.pem)"
    echo "  Audience: $DELEGATOR_WEB_DID"
    echo "  Commands: /claim/cache"

    "$UCANTOOL" delegate \
        --issuer-private-key-file "$KEYS_DIR/indexer.pem" \
        --issuer-did-web "did:web:indexer" \
        --audience "$DELEGATOR_WEB_DID" \
        --subject "did:web:indexer" \
        --command "/claim/cache" \
        > "$INDEXING_PROOF_FILE"

    echo "  [new] indexing-service-proof.txt"
fi

# Generate egress tracking service proof (etracker → delegator, egress/track capability)
EGRESS_PROOF_FILE="$PROOFS_DIR/egress-tracking-proof.txt"
if [[ -f "$EGRESS_PROOF_FILE" && "$FORCE" != "--force" ]]; then
    echo ""
    echo "[skip] egress-tracking-proof.txt already exists"
else
    echo ""
    echo "Generating egress tracking service proof..."
    echo "  Issuer: did:web:etracker (key: etracker.pem)"
    echo "  Audience: $DELEGATOR_WEB_DID"
    echo "  Commands: /egress/track"

    "$UCANTOOL" delegate \
        --issuer-private-key-file "$KEYS_DIR/etracker.pem" \
        --issuer-did-web "did:web:etracker" \
        --audience "$DELEGATOR_WEB_DID" \
        --subject "did:web:etracker" \
        --command "/egress/track" \
        > "$EGRESS_PROOF_FILE"

    echo "  [new] egress-tracking-proof.txt"
fi

# Generate per-node piri proofs (piri-N → upload, blob/* + pdp/info).
# Loops over every piri-{N}.pem emitted by `smelt generate`, producing one
# delegation per node at $PROOFS_DIR/piri-{N}-proof.txt. Upload's post_start.sh
# consumes these to register each node as a separate storage provider.
PIRI_KEYS_FOUND=0
for PIRI_KEY in "$KEYS_DIR"/piri-*.pem; do
    [[ -f "$PIRI_KEY" ]] || continue
    NODE_NAME=$(basename "$PIRI_KEY" .pem)
    # Accept only piri-<N> (skip things like piri-signing-service.pem).
    [[ "$NODE_NAME" =~ ^piri-[0-9]+$ ]] || continue
    PIRI_KEYS_FOUND=1

    PIRI_PROOF_FILE="$PROOFS_DIR/${NODE_NAME}-proof.txt"
    if [[ -f "$PIRI_PROOF_FILE" && "$FORCE" != "--force" ]]; then
        echo ""
        echo "[skip] ${NODE_NAME}-proof.txt already exists"
        continue
    fi

    echo ""
    echo "Generating ${NODE_NAME} proof..."
    echo "  Issuer: ${NODE_NAME}.pem"
    echo "  Audience: $UPLOAD_WEB_DID"
    echo "  Commands: /blob/allocate, /blob/accept, /blob/remove, /blob/reject, /blob/replica/allocate, /pdp/info"

    "$UCANTOOL" delegate \
        --issuer-private-key-file "$PIRI_KEY" \
        --audience "$UPLOAD_WEB_DID" \
        --command "/blob/allocate" \
        --command "/blob/accept" \
        --command "/blob/remove" \
        --command "/blob/reject" \
        --command "/blob/replica/allocate" \
        --command "/pdp/info" \
        --container "base64+gzip" \
        > "$PIRI_PROOF_FILE"

    echo "  [new] ${NODE_NAME}-proof.txt"
done

if [[ "$PIRI_KEYS_FOUND" -eq 0 ]]; then
    echo ""
    echo "WARNING: No piri-N.pem keys found in $KEYS_DIR — skipping piri proofs."
    echo "         Run 'make generate' to create them."
fi

# Generate hilt customer-add proof (upload → hilt, /customer/add).
# Hilt presents this to the upload service when registering tenants as
# customers. NOTE: hilt's upload.proofs loader parses a UCAN *container*
# (hilt pkg/fx/upload.go), so --container is required here — the bare
# envelope emitted for the indexer/etracker proofs will not parse.
HILT_PROOF_FILE="$PROOFS_DIR/hilt-customer-add-proof.txt"
if [[ -f "$HILT_PROOF_FILE" && "$FORCE" != "--force" ]]; then
    echo ""
    echo "[skip] hilt-customer-add-proof.txt already exists"
else
    echo ""
    echo "Generating hilt customer-add proof..."
    echo "  Issuer: $UPLOAD_WEB_DID (key: upload.pem)"
    echo "  Audience: did:web:hilt"
    echo "  Commands: /customer/add"

    "$UCANTOOL" delegate \
        --issuer-private-key-file "$KEYS_DIR/upload.pem" \
        --issuer-did-web "$UPLOAD_WEB_DID" \
        --audience "did:web:hilt" \
        --subject "$UPLOAD_WEB_DID" \
        --command "/customer/add" \
        --container "base64+gzip" \
        > "$HILT_PROOF_FILE"

    echo "  [new] hilt-customer-add-proof.txt"
fi

# Generate hilt → ingot S3 proof (all /s3/* commands ingot invokes on hilt).
# Ingot presents these when calling hilt's UCAN RPC API. The audience is
# ingot's did:web service identity (systems/ingot/config/config.yaml), which
# hilt resolves to ingot's key via http://ingot/.well-known/did.json.
INGOT_PROOF_FILE="$PROOFS_DIR/hilt-ingot-s3-proof.txt"
if [[ -f "$INGOT_PROOF_FILE" && "$FORCE" != "--force" ]]; then
    echo ""
    echo "[skip] hilt-ingot-s3-proof.txt already exists"
else
    check_key "$KEYS_DIR/hilt.pem"
    INGOT_DID="did:web:ingot"

    echo ""
    echo "Generating hilt → ingot S3 proof..."
    echo "  Issuer: did:web:hilt (key: hilt.pem)"
    echo "  Audience: $INGOT_DID"
    echo "  Commands: /s3/request/authorize, /s3/bucket/{create,delete,info,list}"

    "$UCANTOOL" delegate \
        --issuer-private-key-file "$KEYS_DIR/hilt.pem" \
        --issuer-did-web "did:web:hilt" \
        --audience "$INGOT_DID" \
        --subject "did:web:hilt" \
        --command "/s3/request/authorize" \
        --command "/s3/bucket/create" \
        --command "/s3/bucket/delete" \
        --command "/s3/bucket/info" \
        --command "/s3/bucket/list" \
        --container "base64+gzip" \
        > "$INGOT_PROOF_FILE"

    echo "  [new] hilt-ingot-s3-proof.txt"
fi

echo ""
echo "Proofs generated in: $PROOFS_DIR"
echo ""
echo "Generated files:"
ls -la "$PROOFS_DIR"/*.txt 2>/dev/null | awk '{print "  " $NF}' || echo "  (none)"
