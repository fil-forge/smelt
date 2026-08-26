#!/bin/sh
# Initialize and provision ingot-openbao.
#
# Runs as the one-shot `ingot-openbao-init` compose service, gated on
# ingot-openbao's healthcheck (which only means "listening"). The server is
# sealed by central-openbao (`seal "transit"` in config.hcl), so unsealing is
# not this script's job: a started server either auto-unseals or, with
# central unreachable or its seal token revoked, does not start at all.
# Every step is idempotent so the script runs on each `make up`:
#
#   1. first boot: `bao operator init` (one recovery share); the share and
#      the root token are kept on the ingot-openbao-init volume (/init).
#      Dev-only custody.
#   2. once, for a volume that predates the transit seal (Shamir-sealed, its
#      unseal share on /init): `bao operator unseal -migrate` moves it to the
#      transit seal and the share becomes the recovery share.
#   3. every boot: make sure the transit engine, the region KEK
#      (aes256-gcm96, derived=true, exportable=false), the ingot policy, and
#      ingot's scoped token exist.
#
# Nothing secret is ever echoed: no `set -x`, and bao output that carries
# key material goes to files or /dev/null.

set -eu

INIT_DIR=/init
RECOVERY_KEY_FILE="$INIT_DIR/recovery-key"
LEGACY_UNSEAL_KEY_FILE="$INIT_DIR/unseal-key"   # Shamir share from before the transit seal
ROOT_TOKEN_FILE="$INIT_DIR/root-token"
KEK="${INGOT_REGION_KEK:-region-kek}"
POLICY_NAME=ingot-region-kek
: "${BAO_ADDR:?BAO_ADDR must point at ingot-openbao}"
: "${INGOT_OPENBAO_TOKEN:?INGOT_OPENBAO_TOKEN (the token id ingot uses) must be set}"

log() { echo "ingot-openbao-init: $*"; }
die() { echo "ingot-openbao-init: $*" >&2; exit 1; }

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
# A pending Shamir-to-transit seal migration is reported by `bao status` as
# "migration": true (api.SealStatusResponse); the match tolerates compact
# and pretty-printed JSON alike.
migration_pending() { bao status -format=json 2>/dev/null | grep -Eq '"migration": *true'; }

log "waiting for OpenBao at $BAO_ADDR..."
waited=0
while :; do
    rc=0
    bao status >/dev/null 2>&1 || rc=$?
    if [ "$rc" -ne 1 ]; then break; fi
    if [ "$waited" -ge 120 ]; then
        die "OpenBao never answered after ${waited}s; aborting (a transit-sealed server does not start while central-openbao is unreachable or its seal token is revoked)"
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
    log "server is uninitialized; running operator init (1 recovery share)"
    # Stale material from a previous data volume is useless now; overwrite.
    init_json=$(bao operator init -recovery-shares=1 -recovery-threshold=1 -format=json)
    recovery_key=$(json_string "$init_json" recovery_keys_b64)
    root_token=$(json_string "$init_json" root_token)
    [ -n "$recovery_key" ] || die "could not parse the recovery key from operator init output"
    [ -n "$root_token" ] || die "could not parse the root token from operator init output"
    umask 077
    printf '%s' "$recovery_key" > "$RECOVERY_KEY_FILE"
    printf '%s' "$root_token" > "$ROOT_TOKEN_FILE"
    rm -f "$LEGACY_UNSEAL_KEY_FILE"
    unset init_json recovery_key root_token
    log "initialized; recovery share and root token stored on the init volume"
elif [ ! -s "$ROOT_TOKEN_FILE" ]; then
    die "server is initialized but $INIT_DIR holds no root token (init volume lost); run 'make clean' to reset both ingot-openbao volumes"
fi

if is_sealed; then
    if migration_pending && [ -s "$LEGACY_UNSEAL_KEY_FILE" ]; then
        log "migrating the seal from shamir to transit (one-time)"
        bao operator unseal -migrate "$(cat "$LEGACY_UNSEAL_KEY_FILE")" >/dev/null
        mv "$LEGACY_UNSEAL_KEY_FILE" "$RECOVERY_KEY_FILE"
        log "seal migrated; the former unseal share is now the recovery share"
    fi
    # Auto-unseal runs right after start (and after init/migration); give it
    # a moment rather than racing it.
    waited=0
    while is_sealed; do
        if [ "$waited" -ge 60 ]; then
            die "server is still sealed after ${waited}s: central-openbao unreachable, or the seal token was revoked"
        fi
        sleep 1
        waited=$((waited + 1))
    done
fi
log "unsealed (transit seal via central-openbao)"

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
