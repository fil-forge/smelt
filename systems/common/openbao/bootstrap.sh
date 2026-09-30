#!/bin/sh
# Shared first half of an OpenBao init job, sourced by each system's
# openbao/init.sh. It brings a non-dev `bao server` with raft storage to a
# usable state, idempotently, so the init job can run on every `make up`:
#
#   1. first boot: `bao operator init` (one unseal share); the share and the
#      root token are kept on the init volume (/init). Dev-only custody:
#      production replaces the stored share with a transit seal against a
#      central OpenBao.
#   2. every boot: unseal if sealed, then wait for the node to go active
#      (raft serves nothing until it has a leader).
#
# On return BAO_TOKEN holds the root token, for the caller's provisioning.
# The caller sets BAO_ADDR and OPENBAO_NAME (the server's compose service
# name, used in log lines and error hints).
#
# Nothing secret is ever echoed: no `set -x`, and bao output that carries
# key material goes to files or /dev/null.

: "${BAO_ADDR:?BAO_ADDR must point at the OpenBao server}"
: "${OPENBAO_NAME:?OPENBAO_NAME (the compose service name of the server) must be set}"

INIT_DIR=/init
UNSEAL_KEY_FILE="$INIT_DIR/unseal-key"
ROOT_TOKEN_FILE="$INIT_DIR/root-token"

log() { echo "$OPENBAO_NAME-init: $*"; }
die() { echo "$OPENBAO_NAME-init: $*" >&2; exit 1; }

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
    # Stale material from a previous data volume is useless now; overwrite.
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
    die "server is initialized but $INIT_DIR holds no unseal material (init volume lost); run 'make clean' to reset both $OPENBAO_NAME volumes"
fi

if is_sealed; then
    log "unsealing"
    bao operator unseal "$(cat "$UNSEAL_KEY_FILE")" >/dev/null
fi
is_sealed && die "server is still sealed after unseal"
log "unsealed"

# An unsealed raft node is not yet serving: it has to win its leadership
# election first, and until it does every request is refused with 500
# "local node not active but active cluster node not found". sys/health's
# default codes settle it, since 200 means initialized, unsealed and active
# (a standby answers 429, a sealed node 503) — unlike the compose
# healthcheck, which passes sealedcode and uninitcode so it can gate the init
# job on a server that is merely listening.
# -T bounds each probe, so one hung connect cannot outlast the deadline, which
# is wall-clock rather than a count of attempts.
log "waiting for the node to become active..."
started=$(date +%s)
until wget -q -T 2 --spider "$BAO_ADDR/v1/sys/health" 2>/dev/null; do
    if [ "$(($(date +%s) - started))" -ge 60 ]; then
        die "node never became active after 60s; aborting"
    fi
    sleep 1
done
log "node is active (took $(($(date +%s) - started))s)"

BAO_TOKEN=$(cat "$ROOT_TOKEN_FILE")
export BAO_TOKEN
