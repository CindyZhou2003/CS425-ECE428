#!/usr/bin/env bash
#
# Drives the gossip nodes on the VMs from your laptop
#
#   NETID=your_netid ./vm.sh run|kill|fetch|clear [vm_number...]   (default: all VMs)
#
#   run    start the node in the background, with NODE_FLAGS appended
#   kill   SIGKILL the node, in parallel, stamping [KILL] into each victim's log
#   fetch  copy the VM logs into ./logs
#   clear  truncate the VM logs before a measurement run
#
# Line N of host.txt is VM N, which logs to ~/machine.N.log
set -uo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")"

NETID="${NETID:-$USER}"
BINARY="mp2-linux"
# Bracket keeps the pattern from matching the ssh shell that carries it
NODE_PAT="$BINARY[ ]node"
SSH_OPTS="-o ConnectTimeout=10 -o BatchMode=yes"

usage() {
	echo "usage: [NETID=your_netid] ./vm.sh run|kill|fetch|clear [vm_number...]" >&2
	exit 1
}

# Hosts from host.txt into NODES, NODES[0] is VM 1
read_nodes() {
	NODES=()
	while IFS= read -r line || [ -n "$line" ]; do
		line="${line%%#*}"
		line="$(printf '%s' "$line" | tr -d '[:space:]')"
		[ -n "$line" ] && NODES+=("${line%:*}")
	done <host.txt

	if [ "${#NODES[@]}" -eq 0 ]; then
		echo "error: no hosts listed in host.txt" >&2
		exit 1
	fi
}

vm_host() { echo "${NODES[$(($1 - 1))]}"; }

remote() { ssh -n $SSH_OPTS "$NETID@$(vm_host "$1")" "$2"; }

report() { printf 'VM %-3s %-40s %s\n' "$1" "$(vm_host "$1")" "$2"; }

# SIGKILL the node on VM $1, after marking the instant in its own log
kill_vm() {
	local n="$1" out
	if ! out=$(remote "$n" "
		pids=\$(pgrep -u \$(id -un) -f '$NODE_PAT')
		if [ -n \"\$pids\" ]; then
			echo \"\$(date '+%Y-%m-%d %H:%M:%S.%3N') [KILL] SIGKILL on VM $n\" >> ~/machine.$n.log
			kill -9 \$pids
		fi
		echo \$pids
	" 2>&1); then
		report "$n" "UNREACHABLE: $out"
		return 1
	fi
	if [ -z "$out" ]; then
		report "$n" "not running"
		return 0
	fi
	for pid in $out; do report "$n" "killed  PID $pid"; done
}

# Start the node on VM $1 in the background; -daemon since it has no terminal to read
start_vm() {
	local n="$1" out
	if ! out=$(remote "$n" "
		pids=\$(pgrep -u \$(id -un) -f '$NODE_PAT')
		if [ -n \"\$pids\" ]; then echo RUNNING \$pids; exit 0; fi
		nohup ~/$BINARY node -daemon ${NODE_FLAGS:-} > ~/node.out 2>&1 </dev/null &
		pid=\$!
		sleep 1
		if kill -0 \$pid 2>/dev/null; then echo STARTED \$pid; else echo DIED; head -5 ~/node.out; fi
	" 2>&1); then
		report "$n" "UNREACHABLE: $out"
		return 1
	fi

	case "$(echo "$out" | grep -E '^(RUNNING|STARTED|DIED)')" in
	STARTED*)
		report "$n" "started PID $(echo "$out" | awk '/^STARTED/{print $2}')"
		;;
	RUNNING*)
		report "$n" "already running PID $(echo "$out" | awk '/^RUNNING/{$1=""; print substr($0, 2)}'), ./vm.sh kill $n first"
		return 1
		;;
	*)
		report "$n" "FAILED to start:"
		echo "$out" | grep -v '^DIED$' | sed 's/^/    /'
		return 1
		;;
	esac
}

fetch_vm() {
	mkdir -p logs
	if scp $SSH_OPTS "$NETID@$(vm_host "$1"):~/machine.$1.log" "logs/machine.$1.log" >/dev/null; then
		report "$1" "-> logs/machine.$1.log"
	else
		report "$1" "fetch failed"
		return 1
	fi
}

clear_vm() {
	remote "$1" ": > ~/machine.$1.log" && report "$1" "cleared" || { report "$1" "clear failed"; return 1; }
}

cmd="${1-}"
shift || true
case "$cmd" in run | kill | fetch | clear) ;; *) usage ;; esac

read_nodes
[ "$#" -eq 0 ] && set -- $(seq 1 "${#NODES[@]}")
for n in "$@"; do
	if ! [[ "$n" =~ ^[0-9]+$ ]] || [ "$n" -lt 1 ] || [ "$n" -gt "${#NODES[@]}" ]; then
		echo "error: VM number must be 1-${#NODES[@]}, got '$n'" >&2
		exit 1
	fi
done

rc=0
if [ "$cmd" = "kill" ]; then
	# Killed in parallel so several VMs fail at (near) the same instant
	tmp="$(mktemp -d)"
	trap 'rm -rf "$tmp"' EXIT
	for n in "$@"; do
		(kill_vm "$n" >"$tmp/$n" 2>&1; echo "$?" >"$tmp/$n.rc") &
	done
	wait
	for n in "$@"; do
		cat "$tmp/$n"
		[ "$(cat "$tmp/$n.rc")" = "0" ] || rc=1
	done
else
	for n in "$@"; do
		case "$cmd" in
		run) start_vm "$n" || rc=1 ;;
		fetch) fetch_vm "$n" || rc=1 ;;
		clear) clear_vm "$n" || rc=1 ;;
		esac
	done
fi
exit "$rc"
