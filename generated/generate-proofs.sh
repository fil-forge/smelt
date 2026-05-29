#!/bin/bash
# Generate UCAN delegation proofs for service communication
#
# This script generates the delegation proofs needed for services to communicate:
# - indexing-service-proof: indexer delegates claim/cache to delegator
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
    echo "  Capabilities: claim/cache"

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
    echo "  Capabilities: egress/track"

    "$UCANTOOL" delegate \
        --issuer-private-key-file "$KEYS_DIR/etracker.pem" \
        --issuer-did-web "did:web:etracker" \
        --audience "$DELEGATOR_WEB_DID" \
        --subject "did:web:etracker" \
        --command "/space/egress/track" \
        > "$EGRESS_PROOF_FILE"

    echo "  [new] egress-tracking-proof.txt"
fi

echo ""
echo "Proofs generated in: $PROOFS_DIR"
echo ""
echo "Generated files:"
ls -la "$PROOFS_DIR"/*.txt 2>/dev/null | awk '{print "  " $NF}' || echo "  (none)"
