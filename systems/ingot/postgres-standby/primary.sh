#!/bin/sh
# ingot-postgres's entrypoint under compose.pg-standby.yml. Arguments: the
# server command line, synchronous_standby_names included.
#
# The image initializes an empty data directory with a temporary server that
# gets the same arguments as the real one, and that server's CREATE DATABASE
# would wait for the synchronous standby. No standby can connect to it: it
# listens on no TCP port, and the standby is cloned only once this server is
# healthy. So a first boot initializes with the image's plain arguments,
# stops once the server is up, and starts again with the standby named.
set -eu

if [ ! -s "$PGDATA/PG_VERSION" ]; then
  echo "primary.sh: initializing $PGDATA without the synchronous standby"
  # Loopback and another port: neither the standby nor the healthcheck, which
  # asks port 5432, sees this server before it restarts with the standby
  # named, so the standby is not cloned from a server about to stop.
  docker-entrypoint.sh postgres -c listen_addresses=127.0.0.1 -p 5433 &
  pid=$!
  # The temporary init server listens on no TCP port, so this answers only
  # once initialization has finished and the server above has started.
  until pg_isready -q -h 127.0.0.1 -p 5433 -U "$POSTGRES_USER" -d "$POSTGRES_DB"; do
    if ! kill -0 "$pid" 2>/dev/null; then
      wait "$pid" || exit $?
      echo "primary.sh: initialization exited before the server started" >&2
      exit 1
    fi
    sleep 1
  done
  # SIGINT is postgres's fast shutdown.
  kill -INT "$pid"
  wait "$pid" || true
fi

exec docker-entrypoint.sh "$@"
