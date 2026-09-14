#!/usr/bin/env bash
#
# Start the MP server on the given VM(s) and print the VM, PID pairs
#
#   NETID=your_netid ./run_mp.sh <vm_number> [vm_number...]
#
set -uo pipefail
source "$(dirname "$0")/mp_common.sh"

if [ "$#" -eq 0 ]; then
	echo "usage: [NETID=your_netid] ./run_mp.sh <vm_number> [vm_number...]" >&2
	exit 1
fi

read_nodes
check_vms "$@"

rc=0
for n in "$@"; do
	start_vm "$n" || rc=1
done
exit "$rc"
