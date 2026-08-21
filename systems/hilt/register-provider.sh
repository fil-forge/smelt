#!/bin/sh
# Register ingot as hilt's regional provider for us-west-1.
#
# Runs as the one-shot `hilt-init` compose service, gated on hilt's
# healthcheck. Hilt requires a registered provider/region before any tenant
# can be created (PUT /tenants/{id} fails with "unknown region" otherwise),
# and it only accepts /s3/* invocations issued by the tenant's registered
# provider — which is ingot, the service that invokes them. Registration is
# idempotent: the provider record persists in hilt-postgres, so re-runs hit
# the tolerated "already registered" path below.
#
# The provider DID comes from /piri-keys/ingot.did, emitted by
# `smelt generate` (the hilt image has no DID tooling to derive it from the
# key itself). The admin CLI signs with the service identity from this
# container's HILT_* env and derives the server URL from HILT_SERVER_* —
# the compose file sets HILT_SERVER_HOST=hilt so the CLI reaches the hilt
# container across the network instead of this one.

set -e

# depends_on already gates this container on hilt's healthcheck; re-check
# anyway so a crash-and-restart in the gap between "healthy" and our first
# admin call produces a clear message instead of a connection error.
echo "hilt-init: waiting for hilt to serve on hilt:80..."
waited=0
until curl -sf http://hilt:80/health >/dev/null 2>&1; do
    if [ "$waited" -ge 120 ]; then
        echo "hilt-init: hilt never served after ${waited}s — aborting" >&2
        exit 1
    fi
    sleep 1
    waited=$((waited + 1))
done
echo "hilt-init: hilt is serving (took ${waited}s)"

did_file="/piri-keys/ingot.did"
if [ ! -f "$did_file" ]; then
    echo "hilt-init: ${did_file} not found — run 'make generate' to emit DID files" >&2
    exit 1
fi
ingot_did=$(cat "$did_file")
if [ -z "$ingot_did" ]; then
    echo "hilt-init: ${did_file} is empty — run 'make generate --force' to regenerate" >&2
    exit 1
fi

region="${INGOT_REGION:-us-west-1}"
echo "hilt-init: registering ingot (${ingot_did}) as provider for ${region}"
# Tolerate "already registered" — expected if this re-runs against a hilt
# whose provider store still holds the record (e.g. a snapshot boot). Any
# other failure is fatal (note: registration requires a hilt image with
# did:web resolver support; see systems/hilt/README.md).
if add_err=$(hilt client admin provider add "$ingot_did" "$region" 2>&1); then
    :
elif echo "$add_err" | grep -q "already registered"; then
    echo "hilt-init:   (provider already registered — continuing)"
else
    echo "$add_err" >&2
    exit 1
fi

echo "hilt-init: registered ingot as provider for ${region}"
