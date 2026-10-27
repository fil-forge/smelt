#!/bin/sh
# ingot-postgres-standby's entrypoint under compose.pg-standby.yml, run as the
# postgres user. Clones ingot-postgres with pg_basebackup when it has to, then
# runs a hot standby that streams from it through the physical replication
# slot ingot_standby, as application_name ingot_standby: the name the primary's
# synchronous_standby_names waits for.
set -eu

PRIMARY="host=ingot-postgres port=5432 user=ingot dbname=ingot"
SLOT=ingot_standby

on_primary() { psql "$PRIMARY" -v ON_ERROR_STOP=1 -Atc "$1"; }

# A clone is needed for an empty data directory, and also for one that can no
# longer follow the primary: a primary from another cluster (its volume was
# recreated, or restored from a snapshot, while this one stayed) or a slot the
# primary does not have (dropped, or invalidated past max_slot_wal_keep_size,
# which drops it below). Streaming from either would fail forever, and every
# commit on the primary would wait for it.
reason=""
if [ ! -s "$PGDATA/PG_VERSION" ]; then
  reason="no data directory yet"
else
  mine="$(pg_controldata "$PGDATA" | awk -F: '/Database system identifier/ { gsub(/ /, "", $2); print $2 }')"
  theirs="$(on_primary "SELECT system_identifier FROM pg_control_system()")"
  slot="$(on_primary "SELECT wal_status FROM pg_replication_slots WHERE slot_name = '$SLOT'")"
  if [ "$mine" != "$theirs" ]; then
    reason="the data directory belongs to cluster $mine, the primary is $theirs"
  elif [ -z "$slot" ] || [ "$slot" = lost ]; then
    reason="the primary has no usable slot $SLOT"
  fi
fi

if [ -n "$reason" ]; then
  echo "standby.sh: cloning ingot-postgres: $reason"
  find "$PGDATA" -mindepth 1 -delete
  # A slot left by an earlier standby would make --create-slot fail. Neither
  # this nor creating the slot commits a transaction, so neither waits for a
  # synchronous standby.
  on_primary "SELECT pg_drop_replication_slot(slot_name) FROM pg_replication_slots WHERE slot_name = '$SLOT' AND NOT active" >/dev/null
  # -R writes standby.signal, and primary_conninfo (application_name
  # included) and primary_slot_name into postgresql.auto.conf.
  pg_basebackup -d "$PRIMARY application_name=$SLOT" -D "$PGDATA" \
    --wal-method=stream --create-slot --slot="$SLOT" --write-recovery-conf \
    --checkpoint=fast --progress
fi

# The volume's mount point is world-writable in the image; postgres refuses
# to start on a data directory anyone else can read.
chmod 0700 "$PGDATA"
exec postgres -D "$PGDATA"
