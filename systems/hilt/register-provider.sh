#!/bin/sh
# Register this hilt's gateways as the providers for the regions they serve.
#
# Runs as the one-shot `<hilt>-init` compose service that pkg/generate writes
# into generated/compose/hilt.yml, gated on its hilt's healthcheck. Hilt
# resolves the provider serving a request from the region it is signed for,
# and accepts /s3/* invocations only from that provider — which is the
# gateway, the service that invokes them. So an unregistered region makes
# every S3 request fail, and registration has to happen before the stack
# takes any traffic. Registration is idempotent: the provider record persists
# in hilt-postgres, so re-runs hit the tolerated "already registered" path
# below.
#
# The topology comes from /networks.tsv, the `<hilt> <ingot> <region>` file
# `smelt generate` writes from smelt.yml. This container registers the lines
# naming its own hilt, in SMELT_HILT_NAME, and ignores the rest: a provider
# record belongs to the hilt holding that region's tenants. A line with an
# empty region takes INGOT_REGION, which is how a single-region stack has
# always been pointed at a region.
#
# Registration also carries the storage nodes the gateway operates: every
# piri-N DID listed in /piri-nodes.did, the one-DID-per-line file
# `smelt generate` writes from smelt.yml for scripts without DID tooling.
# Only that file is mounted here; the node keys never enter this container.
# Hilt creates the region's routing policy from the DIDs and pushes it to
# sprue, so sprue must already know the nodes: compose orders this after
# upload-init for that reason.
#
# A provider DID is the gateway's did:web service identity, did:web:<name>.
# It is fixed by the stack's did:web:<service> convention: the DID must
# resolve to that container (http://<name>/.well-known/did.json), and the
# same literal is set as identity.service_id in the gateway's rendered
# config (generated/ingot/<name>.yaml) and as the audience of the hilt →
# ingot proof (pkg/stack/proofs.go, generated/generate-proofs.sh). They are
# all written from the one manifest entry, so they move together.
#
# The admin CLI signs with the service identity from this container's HILT_*
# env and derives the server URL from HILT_SERVER_* — the generated compose
# sets HILT_SERVER_HOST to this hilt's name so the CLI reaches that container
# across the network instead of this one.

set -e

hilt="${SMELT_HILT_NAME:-hilt}"

# depends_on already gates this container on the hilt's healthcheck; re-check
# anyway so a crash-and-restart in the gap between "healthy" and our first
# admin call produces a clear message instead of a connection error.
echo "${hilt}-init: waiting for ${hilt} to serve on ${hilt}:80..."
waited=0
until curl -sf "http://${hilt}:80/health" >/dev/null 2>&1; do
    if [ "$waited" -ge 120 ]; then
        echo "${hilt}-init: ${hilt} never served after ${waited}s — aborting" >&2
        exit 1
    fi
    sleep 1
    waited=$((waited + 1))
done
echo "${hilt}-init: ${hilt} is serving (took ${waited}s)"

nodes_file=/piri-nodes.did
if [ ! -f "$nodes_file" ] || [ ! -s "$nodes_file" ]; then
    # `smelt generate` rewrites this file on every run, including for keys
    # directories created before it existed, so a missing or empty file means
    # generate has not run against the current checkout.
    echo "${hilt}-init: no piri node DIDs in generated/keys/piri-nodes.did — aborting" >&2
    echo "${hilt}-init: run 'make generate' to write it from smelt.yml" >&2
    exit 1
fi
nodes=$(tr -d '\r' < "$nodes_file" | tr '\n' ' ' | tr -s ' ')
nodes=${nodes# }
nodes=${nodes% }
if [ -z "$nodes" ]; then
    echo "${hilt}-init: piri node DID list in generated/keys/piri-nodes.did is empty — aborting" >&2
    echo "${hilt}-init: run 'make generate' to write it from smelt.yml" >&2
    exit 1
fi

networks_file=/networks.tsv
if [ ! -f "$networks_file" ] || [ ! -s "$networks_file" ]; then
    echo "${hilt}-init: no topology in generated/keys/networks.tsv — aborting" >&2
    echo "${hilt}-init: run 'make generate' to write it from smelt.yml" >&2
    exit 1
fi

registered=0
# IFS is a literal tab: the file's three fields, any of which may be empty.
while IFS="$(printf '\t')" read -r line_hilt ingot region; do
    [ "$line_hilt" = "$hilt" ] || continue
    [ -n "$ingot" ] || continue
    if [ -z "$region" ]; then
        region="${INGOT_REGION:-us-west-1}"
    fi
    ingot_did="did:web:${ingot}"

    echo "${hilt}-init: registering ${ingot} (${ingot_did}) as provider for ${region} with nodes:${nodes}"
    # Tolerate "already registered" — expected if this re-runs against a hilt
    # whose provider store still holds the record (e.g. a snapshot boot). Hilt
    # reports the same when the *region* is held by a different DID, so a
    # stack whose hilt-postgres predates the gateway's identity looks
    # registered here and then rejects every invocation from it: start from a
    # fresh stack (`make clean`) after changing a gateway's name. Any other
    # failure is fatal (note: registration requires a hilt image with did:web
    # resolver support; see systems/hilt/README.md).
    # shellcheck disable=SC2086  # $nodes is a space-separated list of DIDs
    if add_err=$(hilt client admin provider add "$ingot_did" "$region" $nodes 2>&1); then
        :
    elif echo "$add_err" | grep -q "already registered"; then
        echo "${hilt}-init:   (provider already registered — re-applying nodes)"
        # shellcheck disable=SC2086
        hilt client admin provider nodes set "$ingot_did" $nodes
    else
        echo "$add_err" >&2
        exit 1
    fi
    registered=$((registered + 1))
done < "$networks_file"

if [ "$registered" -eq 0 ]; then
    echo "${hilt}-init: no gateway in networks.tsv names hilt ${hilt} — aborting" >&2
    exit 1
fi

echo "${hilt}-init: registered ${registered} provider(s)"
