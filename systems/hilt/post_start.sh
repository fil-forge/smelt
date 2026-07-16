#!/bin/sh
# Register ingot as hilt's regional provider for us-west-1.
#
# Runs as a Docker Compose post_start hook after the hilt container starts.
# Hilt requires a registered provider/region before any tenant can be created
# (PUT /tenants/{id} fails with "unknown region" otherwise), and it only
# accepts /s3/* invocations issued by the tenant's registered provider — which
# is ingot, the service that invokes them. Registration is idempotent: the
# provider record persists in hilt-postgres, so re-runs hit the tolerated
# "already registered" path below.
#
# The provider DID comes from /piri-keys/ingot.did, emitted by
# `smelt generate` (the hilt image has no DID tooling to derive it from the
# key itself). The admin CLI signs with the service identity from the
# container's HILT_* env and derives the server URL from HILT_SERVER_*.

set -e

# Compose routes post_start stdout/stderr somewhere that neither `docker logs`
# nor testcontainers-go's compose client surfaces. Redirect to PID 1's
# stdout/stderr so everything this script prints shows up in `docker logs
# <hilt>` — including the captured error before `exit 1`, which is otherwise
# lost and makes failures opaque.
exec > /proc/1/fd/1 2> /proc/1/fd/2

# Wait for hilt's HTTP server to actually be listening before making admin
# calls. post_start fires when the container's main process starts, not when
# it's ready.
echo "post_start: waiting for hilt to start serving on :80..."
waited=0
until curl -sf http://localhost:80/health >/dev/null 2>&1; do
    if [ "$waited" -ge 120 ]; then
        echo "post_start: hilt never started serving after ${waited}s — aborting" >&2
        exit 1
    fi
    sleep 1
    waited=$((waited + 1))
done
echo "post_start: hilt is serving (took ${waited}s)"

did_file="/piri-keys/ingot.did"
if [ ! -f "$did_file" ]; then
    echo "post_start: ${did_file} not found — run 'make generate' to emit DID files" >&2
    exit 1
fi
ingot_did=$(cat "$did_file")
if [ -z "$ingot_did" ]; then
    echo "post_start: ${did_file} is empty — run 'make generate --force' to regenerate" >&2
    exit 1
fi

region="${INGOT_REGION:-us-west-1}"
echo "post_start: registering ingot (${ingot_did}) as provider for ${region}"
# Tolerate "already registered" — expected if the hook re-fires against a
# hilt whose provider store still holds the record. Any other failure is
# fatal (note: registration requires a hilt image with did:web resolver
# support; see systems/hilt/README.md).
if add_err=$(hilt client admin provider add "$ingot_did" "$region" 2>&1); then
    :
elif echo "$add_err" | grep -q "already registered"; then
    echo "post_start:   (provider already registered — continuing)"
else
    echo "$add_err" >&2
    exit 1
fi

echo "post_start: registered ingot as provider for ${region}"
