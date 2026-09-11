#!/usr/bin/env bash
#
# Deploy and control the distributed log querier across the course VMs.
#
# The node list comes from host.txt so there is only one place to edit: line N
# of that file is VM N, and it receives logs/machine.N.log. Another group only
# has to replace host.txt with their own hostnames.
#
#   NETID=your_netid ./deploy.sh all
#
set -uo pipefail

cd "$(dirname "$0")"

NETID="${NETID:-$USER}"
HOSTS_FILE="host.txt"
BINARY="mp1-linux"
SSH_OPTS="-o ConnectTimeout=10 -o BatchMode=yes"
SEED="${SEED:-42}"
MB="${MB:-60}"

usage() {
	cat <<'USAGE'
Usage: [NETID=your_netid] ./deploy.sh <command>

Commands:
  build     cross-compile mp1-linux for the VMs (linux/amd64)
  push      copy the binary and host.txt to every node
  genlog    have each node generate its own log file in place
  start     (re)start the server on every node
  stop      stop the server on every node
  status    report which nodes have a server running
  all       build + push + genlog + start + status

Environment:
  NETID     campus login used for ssh/scp (default: $USER)
  SEED      log generation seed, shared by every node (default: 42)
  MB        size of each node's log file in MiB (default: 60)

Node list is read from host.txt: line N is VM N and generates machine.N.log.
Each node runs genlog itself rather than receiving a copy, so deploying moves
a 5 MB binary instead of MB x N of logs. Generation is seeded per file, so a
node's log depends only on SEED, MB, the cluster size, and its own index.

Set up passwordless ssh first, otherwise every step prompts for a password:
  ssh-keygen -t ed25519
  ssh-copy-id your_netid@<each host>
USAGE
}

# read_nodes fills NODES with "host:port" entries, skipping blanks and comments.
read_nodes() {
	NODES=()
	if [ ! -f "$HOSTS_FILE" ]; then
		echo "error: $HOSTS_FILE not found" >&2
		exit 1
	fi
	while IFS= read -r line || [ -n "$line" ]; do
		line="${line%%#*}"
		line="$(printf '%s' "$line" | tr -d '[:space:]')"
		[ -n "$line" ] && NODES+=("$line")
	done <"$HOSTS_FILE"

	if [ "${#NODES[@]}" -eq 0 ]; then
		echo "error: no hosts listed in $HOSTS_FILE" >&2
		exit 1
	fi
}

cmd_build() {
	echo "==> building $BINARY (linux/amd64)"
	GOOS=linux GOARCH=amd64 go build -o "$BINARY" .
	echo "    $(ls -lh "$BINARY" | awk '{print $5}')"
}

cmd_push() {
	read_nodes
	if [ ! -f "$BINARY" ]; then
		echo "error: $BINARY missing, run './deploy.sh build' first" >&2
		exit 1
	fi

	for node in "${NODES[@]}"; do
		host="${node%:*}"
		echo "==> push  $host"
		scp $SSH_OPTS -q "$BINARY" "$HOSTS_FILE" "$NETID@$host:~/" &&
			ssh $SSH_OPTS "$NETID@$host" "chmod +x ~/$BINARY" ||
			echo "    FAILED"
	done
}

# cmd_genlog asks every node to generate its own log file. The -only flag picks
# that node's index out of the cluster-wide plan, so which patterns land in the
# file (and how often) still matches the plan the test verifies against.
cmd_genlog() {
	read_nodes
	local total="${#NODES[@]}"
	echo "==> generating machine.N.log on $total nodes (seed $SEED, ${MB} MiB each)"

	local n=0
	for node in "${NODES[@]}"; do
		n=$((n + 1))
		host="${node%:*}"
		# Backgrounded: each node generates independently, so the wall time is
		# one file's worth rather than the sum over the cluster.
		(
			out=$(ssh $SSH_OPTS "$NETID@$host" \
				"~/$BINARY genlog -n $total -only $n -seed $SEED -mb $MB -outdir ~ -prefix machine. >/dev/null &&
				 wc -c < ~/machine.$n.log" 2>&1)
			if [ $? -ne 0 ]; then
				printf '    %-40s FAILED: %s\n' "$host" "$out"
			else
				printf '    %-40s machine.%s.log  %s bytes\n' "$host" "$n" "$out"
			fi
		) &
	done
	wait
}

cmd_start() {
	read_nodes
	local n=0
	for node in "${NODES[@]}"; do
		n=$((n + 1))
		host="${node%:*}"
		port="${node##*:}"
		echo "==> start $host  port $port  machine.$n.log"
		# pkill -x matches the process name only. With -f it would also match the
		# shell running this very command (its command line contains "$BINARY
		# server") and kill the session out from under us.
		# </dev/null stops ssh from waiting on the backgrounded server.
		out=$(ssh $SSH_OPTS "$NETID@$host" "
			pkill -x $BINARY
			sleep 1
			nohup ~/$BINARY server $port ~/machine.$n.log > ~/server.log 2>&1 </dev/null &
			sleep 1
			if pgrep -x $BINARY >/dev/null; then echo STARTED; else echo NOT_RUNNING; tail -5 ~/server.log; fi
		" 2>&1)
		rc=$?
		if [ $rc -ne 0 ]; then
			echo "    FAILED: ssh exited $rc"
			[ -n "$out" ] && echo "$out" | sed 's/^/    /'
		elif [ "${out%%
*}" != "STARTED" ]; then
			echo "    FAILED: server did not stay up"
			echo "$out" | sed 's/^/    /'
		fi
	done
}

cmd_stop() {
	read_nodes
	for node in "${NODES[@]}"; do
		host="${node%:*}"
		echo "==> stop  $host"
		ssh $SSH_OPTS "$NETID@$host" "pkill -x $BINARY" >/dev/null 2>&1
	done
}

cmd_status() {
	read_nodes
	printf '%-40s %-8s %s\n' "HOST" "PORT" "STATE"
	local down=0
	for node in "${NODES[@]}"; do
		host="${node%:*}"
		port="${node##*:}"
		# -x, not -f: see the note in cmd_start.
		state=$(ssh $SSH_OPTS "$NETID@$host" \
			"pgrep -x $BINARY >/dev/null && echo RUNNING || echo DOWN" 2>/dev/null) ||
			state="UNREACHABLE"
		[ "$state" = "RUNNING" ] || down=$((down + 1))
		printf '%-40s %-8s %s\n' "$host" "$port" "$state"
	done
	if [ "$down" -gt 0 ]; then
		echo
		echo "$down node(s) not running. Check one with:"
		echo "  ssh $NETID@${NODES[0]%:*} cat ~/server.log"
	fi
}

case "${1:-}" in
build) cmd_build ;;
push)
	echo "NETID=$NETID"
	cmd_push
	;;
genlog)
	echo "NETID=$NETID"
	cmd_genlog
	;;
start)
	echo "NETID=$NETID"
	cmd_start
	;;
stop)
	echo "NETID=$NETID"
	cmd_stop
	;;
status)
	echo "NETID=$NETID"
	cmd_status
	;;
all)
	echo "NETID=$NETID"
	cmd_build
	cmd_push
	cmd_genlog
	cmd_start
	echo
	cmd_status
	;;
*)
	usage
	exit 1
	;;
esac
