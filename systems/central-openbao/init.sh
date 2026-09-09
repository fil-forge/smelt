#!/bin/sh
# Initialize, unseal, and provision central-openbao.
#
# Runs as the one-shot `central-openbao-init` compose service, gated on
# central-openbao's healthcheck (which only means "listening"). Every step is
# idempotent so the script runs on each `make up`:
#
#   1. first boot: `bao operator init` (one unseal share); the share and the
#      root token are kept on the central-openbao-init volume (/init).
#      Dev-only custody: production roots this instance in an HSM/KMS.
#   2. every boot: unseal if sealed, then make sure the `seal/` transit
#      engine, the appliance seal key, the appliance policy, and the
#      appliance's seal token exist. The token is what ingot-openbao presents
#      to auto-unseal; revoking it is the "revoke the appliance" action.
#
# Nothing secret is ever echoed: no `set -x`, and bao output that carries
# key material goes to files or /dev/null.

set -eu

INIT_DIR=/init
UNSEAL_KEY_FILE="$INIT_DIR/unseal-key"
ROOT_TOKEN_FILE="$INIT_DIR/root-token"
SEAL_MOUNT=seal
APPLIANCE="${APPLIANCE_NAME:-ingot-openbao}"   # seal key and policy are named after the appliance
POLICY_NAME="appliance-$APPLIANCE"
: "${BAO_ADDR:?BAO_ADDR must point at central-openbao}"
: "${APPLIANCE_SEAL_TOKEN:?APPLIANCE_SEAL_TOKEN (the token id the appliance presents) must be set}"

log() { echo "central-openbao-init: $*"; }
die() { echo "central-openbao-init: $*" >&2; exit 1; }

# Server state comes from exit codes, which are stable across output formats.
# Documented codes:
#   bao status                  0 unsealed, 1 error, 2 sealed
#                               https://openbao.org/docs/commands/status/
#   bao operator init -status   0 initialized, 1 error, 2 not initialized
#                               https://openbao.org/docs/commands/operator/init/
# Two cases the docs leave implicit, confirmed against openbao/openbao:2.6:
# an uninitialized server is also sealed, so `bao status` exits 2 for it, and
# `operator init -status` exits 2 (not 1) when the server is unreachable. So
# reachability is settled first via `bao status`, then initialization, then
# the seal.
is_initialized() { bao operator init -status >/dev/null 2>&1; }
is_sealed() {
    rc=0
    bao status >/dev/null 2>&1 || rc=$?
    [ "$rc" -eq 2 ]
}

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

# json_string JSON KEY prints the string value of KEY from JSON, for a value
# that is either a plain string or the first element of an array. Whitespace
# is stripped first so pretty-printed and compact output parse the same;
# base64 and token values contain no whitespace or quotes.
json_string() {
    printf '%s' "$1" | tr -d ' \n\r\t' | sed -n "s/.*\"$2\":\[\{0,1\}\"\([^\"]*\)\".*/\1/p"
}

mkdir -p "$INIT_DIR"

if ! is_initialized; then
    log "server is uninitialized; running operator init (1 share)"
    init_json=$(bao operator init -key-shares=1 -key-threshold=1 -format=json)
    unseal_key=$(json_string "$init_json" unseal_keys_b64)
    root_token=$(json_string "$init_json" root_token)
    [ -n "$unseal_key" ] || die "could not parse the unseal key from operator init output"
    [ -n "$root_token" ] || die "could not parse the root token from operator init output"
    umask 077
    printf '%s' "$unseal_key" > "$UNSEAL_KEY_FILE"
    printf '%s' "$root_token" > "$ROOT_TOKEN_FILE"
    unset init_json unseal_key root_token
    log "initialized; unseal share and root token stored on the init volume"
elif [ ! -s "$UNSEAL_KEY_FILE" ] || [ ! -s "$ROOT_TOKEN_FILE" ]; then
    die "server is initialized but $INIT_DIR holds no unseal material (init volume lost); run 'make clean' to reset both central-openbao volumes"
fi

if is_sealed; then
    log "unsealing"
    bao operator unseal "$(cat "$UNSEAL_KEY_FILE")" >/dev/null
fi
is_sealed && die "server is still sealed after unseal"
log "unsealed"

BAO_TOKEN=$(cat "$ROOT_TOKEN_FILE")
export BAO_TOKEN

if bao secrets list -format=json | grep -q "\"$SEAL_MOUNT/\""; then
    :
else
    log "enabling the transit engine at $SEAL_MOUNT/"
    bao secrets enable -path="$SEAL_MOUNT" transit >/dev/null
fi

# One seal key per appliance. Transit defaults apply: aes256-gcm96,
# non-derived (the seal wraps with no context), exportable=false.
if bao read "$SEAL_MOUNT/keys/$APPLIANCE" >/dev/null 2>&1; then
    log "seal key $APPLIANCE already exists"
else
    log "creating seal key $APPLIANCE (aes256-gcm96)"
    bao write -f "$SEAL_MOUNT/keys/$APPLIANCE" type=aes256-gcm96 >/dev/null
fi

# The appliance may wrap and unwrap its own barrier key, nothing else.
bao policy write "$POLICY_NAME" - >/dev/null <<POLICY
path "$SEAL_MOUNT/encrypt/$APPLIANCE" { capabilities = ["update"] }
path "$SEAL_MOUNT/decrypt/$APPLIANCE" { capabilities = ["update"] }
POLICY

# The appliance's seal token: orphan (no parent to expire it) and periodic
# (the appliance's seal renews it every period, so it lives as long as the
# appliance keeps booting). Compose fixes its id so ingot-openbao can be
# configured statically; -id, -orphan and -period need the root token. This
# token is the startup-kill lever: `bao token revoke <id>` here and the
# appliance never unseals again. Production binds it to the region's egress
# CIDR as well. Recreated every boot; the create output (token + OpenBao's
# SHA1-lookup warning for custom ids) is swallowed and only shown on failure.
bao token revoke "$APPLIANCE_SEAL_TOKEN" >/dev/null 2>&1 || true
if ! out=$(bao token create -id="$APPLIANCE_SEAL_TOKEN" -policy="$POLICY_NAME" -no-default-policy \
    -orphan -period=24h -display-name="$APPLIANCE" 2>&1); then
    echo "$out" >&2
    die "creating the appliance seal token failed"
fi
unset out BAO_TOKEN

log "seal key $APPLIANCE ready at $SEAL_MOUNT/; appliance seal token provisioned"
