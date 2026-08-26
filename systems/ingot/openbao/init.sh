#!/bin/sh
# Initialize, unseal, and provision ingot-openbao.
#
# Runs as the one-shot `ingot-openbao-init` compose service, gated on
# ingot-openbao's healthcheck (which only means "listening": a fresh server
# is uninitialized and a restarted one is sealed). Every step is idempotent
# so the script runs on each `make up`:
#
#   1. first boot: `bao operator init` (one unseal share); the share and the
#      root token are kept on the ingot-openbao-init volume (/init). Dev-only
#      custody: production replaces the stored share with a transit seal
#      against a central OpenBao.
#   2. every boot: unseal if sealed, then make sure the transit engine, the
#      region KEK (aes256-gcm96, derived=true, exportable=false), the ingot
#      policy, and ingot's scoped token exist.
#
# Nothing secret is ever echoed: no `set -x`, and bao output that carries
# key material goes to files or /dev/null.

set -eu

INIT_DIR=/init
UNSEAL_KEY_FILE="$INIT_DIR/unseal-key"
ROOT_TOKEN_FILE="$INIT_DIR/root-token"
KEK="${INGOT_REGION_KEK:-region-kek}"
POLICY_NAME=ingot-region-kek
POLICY_FILE=/ingot-policy.hcl
: "${BAO_ADDR:?BAO_ADDR must point at ingot-openbao}"
: "${INGOT_OPENBAO_TOKEN:?INGOT_OPENBAO_TOKEN (the token id ingot uses) must be set}"

log() { echo "ingot-openbao-init: $*"; }
die() { echo "ingot-openbao-init: $*" >&2; exit 1; }

# status_field KEY prints the JSON boolean KEY from `bao status`.
status_field() {
    bao status -format=json 2>/dev/null | awk -v key="\"$1\":" '$1 == key { gsub(/[,]/, "", $2); print $2; exit }'
}

# `bao status` exits 0 (initialized), 2 (not initialized), or 1 (unreachable
# or error). Wait for anything but 1.
log "waiting for OpenBao at $BAO_ADDR..."
waited=0
while :; do
    rc=0
    bao status >/dev/null 2>&1 || rc=$?
    if [ "$rc" -ne 1 ]; then break; fi
    if [ "$waited" -ge 120 ]; then
        die "OpenBao never answered after ${waited}s; aborting"
    fi
    sleep 1
    waited=$((waited + 1))
done
log "OpenBao is answering (took ${waited}s)"

mkdir -p "$INIT_DIR"

if [ "$(status_field initialized)" != "true" ]; then
    log "server is uninitialized; running operator init (1 share)"
    # Stale material from a previous data volume is useless now; overwrite.
    init_json=$(bao operator init -key-shares=1 -key-threshold=1 -format=json)
    # unseal_keys_b64 is a one-element array: take the line after its key.
    unseal_key=$(printf '%s\n' "$init_json" | awk '/"unseal_keys_b64"/ { getline; gsub(/[ ",]/, ""); print; exit }')
    root_token=$(printf '%s\n' "$init_json" | awk -F'"' '/"root_token"/ { print $4; exit }')
    [ -n "$unseal_key" ] || die "could not parse the unseal key from operator init output"
    [ -n "$root_token" ] || die "could not parse the root token from operator init output"
    umask 077
    printf '%s' "$unseal_key" > "$UNSEAL_KEY_FILE"
    printf '%s' "$root_token" > "$ROOT_TOKEN_FILE"
    unset init_json unseal_key root_token
    log "initialized; unseal share and root token stored on the init volume"
elif [ ! -s "$UNSEAL_KEY_FILE" ] || [ ! -s "$ROOT_TOKEN_FILE" ]; then
    die "server is initialized but $INIT_DIR holds no unseal material (init volume lost); run 'make clean' to reset both ingot-openbao volumes"
fi

if [ "$(status_field sealed)" = "true" ]; then
    log "unsealing"
    bao operator unseal "$(cat "$UNSEAL_KEY_FILE")" >/dev/null
fi
[ "$(status_field sealed)" = "false" ] || die "server is still sealed after unseal"
log "unsealed"

BAO_TOKEN=$(cat "$ROOT_TOKEN_FILE")
export BAO_TOKEN

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

bao policy write "$POLICY_NAME" "$POLICY_FILE" >/dev/null

# Recreate ingot's token every boot: compose fixes its id so ingot can be
# configured statically, and recreating it keeps the default TTL from
# expiring under a long-lived stack. -id and -orphan need the root token.
# OpenBao warns that a custom token id is hashed with SHA1 for lookups; fine
# for a fixed local-dev token, so the output (token + warning) is swallowed
# and only shown when the call fails.
bao token revoke "$INGOT_OPENBAO_TOKEN" >/dev/null 2>&1 || true
if ! out=$(bao token create -id="$INGOT_OPENBAO_TOKEN" -policy="$POLICY_NAME" -no-default-policy \
    -orphan -display-name=ingot 2>&1); then
    echo "$out" >&2
    die "creating ingot's token failed"
fi
unset out BAO_TOKEN

log "region KEK $KEK ready (transit aes256-gcm96, derived); ingot token provisioned"
