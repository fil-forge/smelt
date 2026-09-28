#!/bin/sh
# Initialize, unseal, and provision ingot-openbao.
#
# Runs as the one-shot `ingot-openbao-init` compose service, gated on
# ingot-openbao's healthcheck (which only means "listening": a fresh server
# is uninitialized and a restarted one is sealed). The shared bootstrap
# (systems/common/openbao/bootstrap.sh) initializes on first boot, unseals,
# and waits for the node to go active; this script then makes sure the
# transit engine, the region KEK (aes256-gcm96, derived=true,
# exportable=false), the ingot policy, and ingot's scoped token exist. Every
# step is idempotent so the job runs on each `make up`.

set -eu

KEK="${INGOT_REGION_KEK:-region-kek}"
POLICY_NAME=ingot-region-kek
: "${INGOT_OPENBAO_TOKEN:?INGOT_OPENBAO_TOKEN (the token id ingot uses) must be set}"

OPENBAO_NAME=ingot-openbao
. /openbao-bootstrap.sh

if bao secrets list -format=json | grep -q '"transit/"'; then
    :
else
    log "enabling the transit engine"
    bao secrets enable transit >/dev/null
fi

if bao read transit/keys/"$KEK" >/dev/null 2>&1; then
    log "region KEK $KEK already exists"
else
    log "creating region KEK $KEK (aes256-gcm96, derived=true, exportable=false)"
    bao write -f transit/keys/"$KEK" type=aes256-gcm96 derived=true exportable=false >/dev/null
fi

# Ingot's policy: wrap, unwrap, and rewrap under the region KEK, nothing
# else. Generated here so it follows the configured key name.
bao policy write "$POLICY_NAME" - >/dev/null <<POLICY
path "transit/encrypt/$KEK" { capabilities = ["update"] }
path "transit/decrypt/$KEK" { capabilities = ["update"] }
path "transit/rewrap/$KEK"  { capabilities = ["update"] }
POLICY

# Recreate ingot's token every boot. Compose fixes its id so ingot can be
# configured statically; -id and -orphan need the root token. The token
# carries OpenBao's default TTL (768h) and nothing renews it, so a stack left
# up longer than that needs a down/up to mint a fresh one. OpenBao warns that
# a custom id is hashed with SHA1 for lookups; fine for a local-dev token, so
# the output (token + warning) is swallowed and only shown when the call fails.
bao token revoke "$INGOT_OPENBAO_TOKEN" >/dev/null 2>&1 || true
if ! out=$(bao token create -id="$INGOT_OPENBAO_TOKEN" -policy="$POLICY_NAME" -no-default-policy \
    -orphan -display-name=ingot 2>&1); then
    echo "$out" >&2
    die "creating ingot's token failed"
fi
unset out BAO_TOKEN

log "region KEK $KEK ready (transit aes256-gcm96, derived); ingot token provisioned"
