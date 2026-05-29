#!/bin/bash
# Initialize smelt local development environment
#
# This script prepares the environment for running the compose stack:
# 1. Creates the generated/ directory structure
# 2. Generates cryptographic keys for all services
# 3. Creates the Docker network if it doesn't exist
#
# Usage: ./init.sh [--force]
#   --force: Regenerate all keys even if they exist

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(dirname "$SCRIPT_DIR")"
GENERATED_DIR="$PROJECT_DIR/generated"
FORCE=${1:-""}

echo "========================================"
echo "Forge Compose - Environment Setup"
echo "========================================"
echo ""

# Step 1: Ensure generated directory exists
echo "Step 1: Creating generated/ directory..."
mkdir -p "$GENERATED_DIR/keys"
mkdir -p "$GENERATED_DIR/proofs"
# snapshot-scratch is bind-mounted into the blockchain container as /output;
# needs to exist before compose up so docker creates a bind, not an anon volume.
mkdir -p "$GENERATED_DIR/snapshot-scratch"
# snapshots/ holds named snapshot directories produced by `smelt snapshot save`.
mkdir -p "$GENERATED_DIR/snapshots"

# Note: Key generation is handled by 'smelt generate' (called via 'make generate').
# This script handles proof generation and Docker network creation.

# Step 2: Check for ucantool (needed for proof generation)
echo ""
echo "Step 3: Checking for ucantool..."
# Check for ucantool
UCANTOOL="${UCANTOOL:-ucantool}"
if command -v "$UCANTOOL" &> /dev/null; then
    echo "  ucantool found at: $(which "$UCANTOOL")"
else
    echo "  ucantool not found, installing..."
    if command -v go &> /dev/null; then
        go install github.com/fil-forge/ucantool@latest
        echo "  ucantool installed successfully"
    else
        echo "WARNING: Go not found. Cannot install ucantool."
        echo "         Proof generation will be skipped."
        echo "         Install manually: go install github.com/fil-forge/ucantool@latest"
    fi
fi

# Step 3: Generate delegation proofs
# TODO: migrate UCAN proof generation into pkg/generate/ (Go) so scripts/init.sh
# and generated/generate-proofs.sh can be removed. Key generation has already
# been migrated to 'smelt generate'; proofs are the last shell-based step.
echo ""
echo "Step 3: Generating delegation proofs..."
if [[ -x "$GENERATED_DIR/generate-proofs.sh" ]]; then
    if command -v "$UCANTOOL" &> /dev/null; then
        "$GENERATED_DIR/generate-proofs.sh" $FORCE
    else
        echo "  Skipping proof generation (ucantool not available)"
    fi
else
    echo "WARNING: generate-proofs.sh not found or not executable"
    echo "         Proof generation will be skipped."
fi

# Step 4: Create Docker network
echo ""
echo "Step 4: Creating Docker network..."
if docker network inspect forge-network >/dev/null 2>&1; then
    echo "  Network 'forge-network' already exists"
else
    docker network create forge-network
    echo "  Created network 'forge-network'"
fi

# Summary
echo ""
echo "========================================"
echo "Setup Complete!"
echo "========================================"
echo ""
echo "Keys generated in: $GENERATED_DIR/keys/"
echo "Proofs generated in: $GENERATED_DIR/proofs/"
echo ""
echo "Next steps:"
echo "  1. Start the full stack:  make up"
echo "  2. View logs:             make logs"
echo "  3. Access guppy shell:    make guppy"
echo ""
echo "Or start individual systems:"
echo "  cd systems/blockchain && docker compose up -d"
echo "  cd systems/indexing && docker compose up -d"
echo ""
