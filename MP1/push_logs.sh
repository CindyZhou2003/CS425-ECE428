#!/usr/bin/env bash
#
# Upload logs/machine.N.log to VM N as ~/machine.N.log, one at a time
#
#   NETID=your_netid ./push_logs.sh                  # every VM
#   NETID=your_netid ./push_logs.sh <vm_number>...   # only these VMs
#
set -uo pipefail
source "$(dirname "$0")/mp_common.sh"

read_nodes
if [ "$#" -eq 0 ]; then
	set -- $(seq 1 "${#NODES[@]}")
fi
check_vms "$@"

echo "NETID=$NETID"
failed=0
for n in "$@"; do
	if ! push_log "$n"; then
		echo "    FAILED"
		failed=$((failed + 1))
	fi
done

echo
if [ "$failed" -eq 0 ]; then
	echo "all $# logs pushed"
else
	echo "$failed of $# VM(s) did not get a log"
	exit 1
fi
