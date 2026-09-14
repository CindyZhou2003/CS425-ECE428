#!/usr/bin/env bash
#
# Upload the local logs to the VMs one at a time
# Line N of host.txt is VM N, which gets logs/machine.N.log as ~/machine.N.log
#
#   NETID=your_netid ./push_logs.sh
#
set -uo pipefail

cd "$(dirname "$0")"

NETID="${NETID:-$USER}"
HOSTS_FILE="host.txt"
LOG_DIR="logs"
SSH_OPTS="-o ConnectTimeout=10 -o BatchMode=yes"

if [ ! -f "$HOSTS_FILE" ]; then
	echo "error: $HOSTS_FILE not found" >&2
	exit 1
fi
if [ ! -d "$LOG_DIR" ]; then
	echo "error: $LOG_DIR/ not found" >&2
	exit 1
fi

echo "NETID=$NETID"

n=0
failed=0
while IFS= read -r line || [ -n "$line" ]; do
	line="${line%%#*}"
	line="$(printf '%s' "$line" | tr -d '[:space:]')"
	[ -n "$line" ] || continue

	n=$((n + 1))
	host="${line%:*}"
	file="$LOG_DIR/machine.$n.log"

	if [ ! -f "$file" ]; then
		echo "==> skip  $host  ($file missing)"
		failed=$((failed + 1))
		continue
	fi

	echo "==> push  $host  machine.$n.log  $(ls -lh "$file" | awk '{print $5}')"
	# stdin from /dev/null so scp doesn't eat the rest of host.txt
	if ! scp $SSH_OPTS "$file" "$NETID@$host:~/machine.$n.log" </dev/null; then
		echo "    FAILED"
		failed=$((failed + 1))
	fi
done <"$HOSTS_FILE"

echo
if [ "$failed" -eq 0 ]; then
	echo "all $n logs pushed"
else
	echo "$failed of $n node(s) did not get a log"
	exit 1
fi
