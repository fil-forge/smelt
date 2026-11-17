#!/bin/sh
# Create the extra databases a multi-instance service needs.
#
# Runs as the one-shot `<kind>-postgres-init` compose service, which
# pkg/generate writes into generated/compose/hilt.yml and ingot.yml, gated on
# that server's healthcheck. The leading instance's database is the one
# POSTGRES_DB creates, so this only ever creates the rest; a stack that runs
# one hilt and one ingot gets no sidecar at all. Creating a database is
# idempotent, so the script re-runs on every `make up`.

set -eu

: "${SMELT_POSTGRES_HOST:?SMELT_POSTGRES_HOST must name the postgres service}"
: "${SMELT_POSTGRES_USER:?SMELT_POSTGRES_USER must name the postgres role}"
: "${SMELT_POSTGRES_DATABASES:?SMELT_POSTGRES_DATABASES must list the databases to create}"

die() { echo "${SMELT_POSTGRES_HOST}-init: $*" >&2; exit 1; }

PGHOST=$SMELT_POSTGRES_HOST
PGUSER=$SMELT_POSTGRES_USER
# Bounds each probe below, so one hung connect cannot outlast the deadline.
PGCONNECT_TIMEOUT=2
export PGHOST PGUSER PGCONNECT_TIMEOUT

# depends_on gates this container on the server's healthcheck, but pg_isready
# reports the server up while it still refuses sessions with "the database
# system is starting up": the postgres image runs a temporary server for
# initdb and restarts it before serving for real. psql reports a refused
# session as exit 2, so wait for a query to land before creating anything.
# The deadline is wall-clock rather than a count of attempts.
echo "${SMELT_POSTGRES_HOST}-init: waiting for postgres to accept connections..."
started=$(date +%s)
until psql -d postgres -c 'SELECT 1' >/dev/null 2>&1; do
    if [ "$(($(date +%s) - started))" -ge 120 ]; then
        die "postgres never accepted a connection after 120s — aborting"
    fi
    sleep 1
done
echo "${SMELT_POSTGRES_HOST}-init: postgres is accepting connections (took $(($(date +%s) - started))s)"

# shellcheck disable=SC2086  # the database list is space-separated on purpose
for db in $SMELT_POSTGRES_DATABASES; do
    # The generator emits <user>_<N>. Refuse anything else rather than
    # interpolate an unexpected name into SQL.
    case "$db" in
        "${PGUSER}_"*) ;;
        *) die "refusing unexpected database name: $db" ;;
    esac
    case "${db#"${PGUSER}_"}" in
        '' | *[!0-9]*) die "refusing unexpected database name: $db" ;;
    esac

    if psql -d postgres -tAc "SELECT 1 FROM pg_database WHERE datname = '$db'" | grep -q 1; then
        echo "${SMELT_POSTGRES_HOST}-init:   (${db} already exists)"
    else
        echo "${SMELT_POSTGRES_HOST}-init: creating ${db}"
        psql -d postgres -c "CREATE DATABASE ${db};"
    fi
done

echo "${SMELT_POSTGRES_HOST}-init: databases ready: ${SMELT_POSTGRES_DATABASES}"
