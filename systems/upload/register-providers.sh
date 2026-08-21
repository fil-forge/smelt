#!/bin/sh
# Register every piri-N node declared in smelt.yml as a storage provider.
#
# Runs as the one-shot `upload-init` compose service, gated on upload's
# healthcheck. Loops over each /piri-keys/piri-{N}.pub emitted by
# `smelt generate` and registers the corresponding node as a provider with
# equal weight. The sprue admin CLI reads /etc/sprue/config.yaml for its
# identity and targets http://{server.host}:{server.port}; the compose file
# sets SPRUE_SERVER_HOST=upload so the CLI reaches the upload container
# across the network instead of this one.
#
# As of ucan1.0, provider registration no longer requires a delegation proof —
# the admin call takes DID + endpoint and that's it. We iterate public keys
# (which exist regardless of proof generation) rather than proof files.

set -e

# depends_on already gates this container on upload's healthcheck; re-check
# anyway so a crash-and-restart in the gap between "healthy" and our first
# admin call produces a clear message instead of a connection error.
echo "upload-init: waiting for sprue to serve on upload:80..."
waited=0
until curl -sf http://upload:80/health >/dev/null 2>&1; do
    if [ "$waited" -ge 120 ]; then
        echo "upload-init: sprue never served after ${waited}s — aborting" >&2
        exit 1
    fi
    sleep 1
    waited=$((waited + 1))
done
echo "upload-init: sprue is serving (took ${waited}s)"

registered=0
for pub_key in /piri-keys/piri-*.pub; do
    [ -f "$pub_key" ] || continue
    node_name=$(basename "$pub_key" .pub)  # piri-0, piri-1, ...
    # Accept only piri-<N>; skip anything else that might land in the keys dir.
    case "$node_name" in
        piri-[0-9]*) ;;
        *) continue ;;
    esac

    did=$(sprue identity parse "$pub_key")
    endpoint="http://${node_name}:3000"
    proofs="/proofs/${node_name}-proof.txt"

    echo "upload-init: registering ${node_name} (${did}) at ${endpoint}"
    # Tolerate "already registered" — expected when the stack booted from a
    # smelt snapshot that captured upload's dynamodb provider registry. Any
    # other failure is still fatal.
    if add_err=$(sprue client admin provider register "$did" "$endpoint" "$proofs" 2>&1); then
        :
    elif echo "$add_err" | grep -q "already registered"; then
        echo "upload-init:   (${node_name} already registered — continuing)"
    else
        echo "$add_err" >&2
        exit 1
    fi
    sprue client admin provider weight set "$did" 100 100

    registered=$((registered + 1))
done

if [ "$registered" -eq 0 ]; then
    echo "upload-init: WARNING — no piri-N nodes were registered"
    echo "upload-init:           check that 'smelt generate' populated /piri-keys"
    exit 1
fi

echo "upload-init: registered ${registered} piri provider(s)"
