#!/bin/sh
# ingot-postgres-netem's entrypoint under compose.pg-standby.yml. Delays every
# packet the standby's network namespace sends by INGOT_PG_STANDBY_RTT, then
# stays up, since the namespace and its qdisc live as long as this container.
#
# netem shapes only what an interface sends, so the whole round trip goes on
# this side: the standby's flush acknowledgements (and its TCP ACKs) reach the
# primary one round trip later. Loopback is not shaped.
set -eu

rtt="${INGOT_PG_STANDBY_RTT:-0ms}"
ready=/tmp/netem-ready
rm -f "$ready"

if echo "$rtt" | grep -Eq '^0+(\.0*)?[a-z]*$'; then
  # Nothing to add, so no qdisc: a kernel without netem runs this too.
  echo "netem.sh: INGOT_PG_STANDBY_RTT=$rtt, no delay added"
else
  # The interface the default route leaves by: the forge-network one, the
  # only network this container joins.
  dev="$(ip route show default | awk '{ for (i = 1; i < NF; i++) if ($i == "dev") { print $(i + 1); exit } }')"
  [ -n "$dev" ] || { echo "netem.sh: no default route" >&2; exit 1; }
  # netem's default queue of 1000 packets can overflow in a burst at a
  # millisecond or two of delay, and drops would show up as retransmits.
  if ! err="$(tc qdisc replace dev "$dev" root netem delay "$rtt" limit 100000 2>&1)"; then
    case "$err" in
      *"qdisc kind is unknown"*)
        echo "netem.sh: this kernel has no netem qdisc (CONFIG_NET_SCH_NETEM, module sch_netem)," \
          "so INGOT_PG_STANDBY_RTT=$rtt cannot be added here; leave it unset or 0ms on this host" >&2 ;;
      *) echo "netem.sh: tc qdisc replace dev $dev root netem delay $rtt: $err" >&2 ;;
    esac
    exit 1
  fi
  echo "netem.sh: $(tc qdisc show dev "$dev")"
fi
touch "$ready"

trap 'exit 0' INT TERM
sleep infinity &
wait
