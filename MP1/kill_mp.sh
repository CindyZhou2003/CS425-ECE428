#!/usr/bin/env bash
#
# Forcibly kill (SIGKILL) the MP server on the given VM(s) and print the VM, PID pairs
#
#   NETID=your_netid ./kill_mp.sh <vm_number> [vm_number...]
#
set -uo pipefail
source "$(dirname "$0")/mp_common.sh"

if [ "$#" -eq 0 ]; then
	echo "usage: [NETID=your_netid] ./kill_mp.sh <vm_number> [vm_number...]" >&2
	exit 1
fi

read_nodes
check_vms "$@"

rc=0
for n in "$@"; do
	kill_vm "$n" || rc=1
done
exit "$rc"
