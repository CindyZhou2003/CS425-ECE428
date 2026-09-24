#!/usr/bin/env bash
#
# Runs the detection-time vs simultaneous-failures experiment for one protocol
#
#   NETID=your_netid ./measure_detection.sh gossip|suspect [trials] [max_failures]
#
# Starts all VMs fresh, then for k = 1..max_failures kills k VMs at once, `trials` times,
# restarting the victims between trials. Logs land in logs-detect-<mode>.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")"

mode="${1-}"
trials="${2:-5}"
max_k="${3:-3}"
case "$mode" in
gossip) export NODE_FLAGS="-nosuspect" ;;
suspect) export NODE_FLAGS="" ;;
*)
	echo "usage: [NETID=your_netid] ./measure_detection.sh gossip|suspect [trials] [max_failures]" >&2
	exit 1
	;;
esac

# Long enough for detection (6s bound) plus cleanup of the dead entries
settle=15
out="logs-detect-$mode"

./vm.sh kill >/dev/null
./vm.sh clear
./vm.sh run
echo "waiting ${settle}s for the group to converge"
sleep "$settle"

# Rotates victims over VMs 2-10 so no single VM carries every trial; VM 1 is the introducer
offset=0
for k in $(seq 1 "$max_k"); do
	for t in $(seq 1 "$trials"); do
		victims=()
		for i in $(seq 0 $((k - 1))); do
			victims+=($((2 + (offset + i) % 9)))
		done
		offset=$((offset + k))

		echo "== k=$k trial $t: killing VMs ${victims[*]} =="
		./vm.sh kill "${victims[@]}"
		sleep "$settle"
		./vm.sh run "${victims[@]}"
		sleep "$settle"
	done
done

./vm.sh fetch
rm -rf "$out"
mv logs "$out"
echo "logs saved to $out, now run: ./analyze.py $out"
