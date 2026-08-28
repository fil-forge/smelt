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
# The provider DID is ingot's did:web service identity, did:web:ingot. It is
# fixed by the stack's did:web:<service> convention: the DID must resolve to
# the `ingot` container (http://ingot/.well-known/did.json), and the same
# literal is set as identity.service_id in systems/ingot/config/config.yaml
# and as the audience of the hilt → ingot proof (pkg/stack/proofs.go,
# generated/generate-proofs.sh). Change all of them together or not at all.
# The admin CLI signs with the service identity from this container's HILT_* env
# and derives the server URL from HILT_SERVER_* — the compose file sets
# HILT_SERVER_HOST=hilt so the CLI reaches the hilt container across the
# network instead of this one.

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

ingot_did="did:web:ingot"

region="${INGOT_REGION:-us-west-1}"
echo "hilt-init: registering ingot (${ingot_did}) as provider for ${region}"
# Tolerate "already registered" — expected if this re-runs against a hilt
# whose provider store still holds the record (e.g. a snapshot boot). Hilt
# reports the same when the *region* is held by a different DID, so a stack
# whose hilt-postgres predates the did:web:ingot identity looks registered
# here and then rejects every ingot invocation: start from a fresh stack
# (`make clean`) after changing ingot's DID. Any other failure is fatal (note:
# registration requires a hilt image with did:web resolver support; see
# systems/hilt/README.md).
if add_err=$(hilt client admin provider add "$ingot_did" "$region" 2>&1); then
    :
elif echo "$add_err" | grep -q "already registered"; then
    echo "hilt-init:   (provider already registered — continuing)"
else
    echo "$add_err" >&2
    exit 1
fi

echo "hilt-init: registered ingot as provider for ${region}"
