#!/bin/sh
# Create the per-node postgres databases the piri nodes expect.
#
# Runs as the one-shot `piri-postgres-init` compose service, which
# pkg/generate writes into generated/compose/piri.yml, gated on
# piri-postgres's healthcheck. PIRI_POSTGRES_DATABASES carries the
# space-separated database list, one entry per postgres-backed node in
# smelt.yml. Creating a database is idempotent, so the script re-runs on
# every `make up`.

set -eu

: "${PIRI_POSTGRES_DATABASES:?PIRI_POSTGRES_DATABASES must list the databases to create}"

die() { echo "piri-postgres-init: $*" >&2; exit 1; }

PGHOST=piri-postgres
PGUSER=piri
# Bounds each probe below, so one hung connect cannot outlast the deadline.
PGCONNECT_TIMEOUT=2
export PGHOST PGUSER PGCONNECT_TIMEOUT

# depends_on gates this container on piri-postgres's healthcheck, but
# pg_isready reports the server up while it still refuses sessions with
# "the database system is starting up": the postgres image runs a temporary
# server for initdb and restarts it before serving for real. psql reports a
# refused session as exit 2, so wait for a query to land before creating
# anything. The deadline is wall-clock rather than a count of attempts.
echo "piri-postgres-init: waiting for postgres to accept connections..."
started=$(date +%s)
until psql -d postgres -c 'SELECT 1' >/dev/null 2>&1; do
    if [ "$(($(date +%s) - started))" -ge 120 ]; then
        die "postgres never accepted a connection after 120s — aborting"
    fi
    sleep 1
done
echo "piri-postgres-init: postgres is accepting connections (took $(($(date +%s) - started))s)"

# shellcheck disable=SC2086  # the database list is space-separated on purpose
for db in $PIRI_POSTGRES_DATABASES; do
    # The generator emits piri_<N>. Refuse anything else rather than
    # interpolate an unexpected name into SQL.
    case "$db" in
        piri_*) ;;
        *) die "refusing unexpected database name: $db" ;;
    esac
    case "${db#piri_}" in
        '' | *[!0-9]*) die "refusing unexpected database name: $db" ;;
    esac

    if psql -d postgres -tAc "SELECT 1 FROM pg_database WHERE datname = '$db'" | grep -q 1; then
        echo "piri-postgres-init:   (${db} already exists)"
    else
        echo "piri-postgres-init: creating ${db}"
        psql -d postgres -c "CREATE DATABASE ${db};"
    fi
done

echo "piri-postgres-init: databases ready: ${PIRI_POSTGRES_DATABASES}"
