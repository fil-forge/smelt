#!/bin/sh
# Initialize, unseal, and provision hilt-vault.
#
# Runs as the one-shot `hilt-vault-init` compose service, gated on
# hilt-vault's healthcheck (which only means "listening": a fresh server is
# uninitialized and a restarted one is sealed). The shared bootstrap
# (systems/common/openbao/bootstrap.sh) initializes on first boot, unseals,
# and waits for the node to go active; this script then makes sure the KV v2
# engine at `secret` (hilt's default mount), the hilt policy, and hilt's
# scoped token exist. Every step is idempotent so the job runs on each
# `make up`.

set -eu

MOUNT=secret
POLICY_NAME=hilt-keys
: "${HILT_VAULT_TOKEN:?HILT_VAULT_TOKEN (the token id hilt uses) must be set}"

OPENBAO_NAME=hilt-vault
. /openbao-bootstrap.sh

if bao secrets list -format=json | grep -q "\"$MOUNT/\""; then
    :
else
    log "enabling KV v2 at $MOUNT"
    bao secrets enable -path="$MOUNT" kv-v2 >/dev/null
fi

# Hilt's policy: read and write key material, and delete a key outright
# (hilt removes a key's metadata, which drops every version).
bao policy write "$POLICY_NAME" - >/dev/null <<POLICY
path "$MOUNT/data/*"     { capabilities = ["create", "read", "update"] }
path "$MOUNT/metadata/*" { capabilities = ["delete"] }
POLICY

# Recreate hilt's token every boot. Compose fixes its id so hilt can be
# configured statically; -id and -orphan need the root token. The token
# carries OpenBao's default TTL (768h) and nothing renews it, so a stack left
# up longer than that needs a down/up to mint a fresh one. OpenBao warns that
# a custom id is hashed with SHA1 for lookups; fine for a local-dev token, so
# the output (token + warning) is swallowed and only shown when the call fails.
bao token revoke "$HILT_VAULT_TOKEN" >/dev/null 2>&1 || true
if ! out=$(bao token create -id="$HILT_VAULT_TOKEN" -policy="$POLICY_NAME" -no-default-policy \
    -orphan -display-name=hilt 2>&1); then
    echo "$out" >&2
    die "creating hilt's token failed"
fi
unset out BAO_TOKEN

log "KV v2 at $MOUNT ready; hilt token provisioned"
