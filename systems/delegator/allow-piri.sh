#!/bin/sh
# Put every piri-N node on the delegator's allow list.
#
# Runs as the one-shot `delegator-init` compose service, gated on the
# delegator's healthcheck. The delegator registers only allow-listed DIDs,
# and each piri-N registers itself during its own init, so every generated
# piri service waits for this container to finish.
#
# The DIDs come from /piri-nodes.did, the one-DID-per-line file
# `smelt generate` writes from smelt.yml. The delegator's own
# `store allow-did` command writes them through the same config file the
# server reads, so the allow list lives wherever the delegator's store does.
# The command is idempotent, so re-runs (every `make up`, a snapshot boot,
# a node added to smelt.yml) only add what is missing.

set -eu

nodes_file=/piri-nodes.did
if [ ! -s "$nodes_file" ]; then
    # `smelt generate` rewrites this file on every run, so a missing or empty
    # file means generate has not run against the current checkout.
    echo "delegator-init: no piri node DIDs in generated/keys/piri-nodes.did — aborting" >&2
    echo "delegator-init: run 'make generate' to write it from smelt.yml" >&2
    exit 1
fi

allowed=0
for did in $(tr -d '\r' < "$nodes_file"); do
    echo "delegator-init: allowing ${did}"
    /usr/bin/registrar --config /.delegator.yaml store allow-did "$did"
    allowed=$((allowed + 1))
done

if [ "$allowed" -eq 0 ]; then
    echo "delegator-init: piri node DID list in generated/keys/piri-nodes.did is empty — aborting" >&2
    exit 1
fi

echo "delegator-init: ${allowed} piri node(s) on the allow list"
