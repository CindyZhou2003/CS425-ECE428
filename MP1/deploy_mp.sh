#!/usr/bin/env bash
#
# For every VM in host.txt: kill the MP, deploy the new binary, start the MP
#
#   NETID=your_netid ./deploy_mp.sh           # binary + host.txt
#   NETID=your_netid ./deploy_mp.sh --logs    # also upload logs/machine.N.log
#
set -uo pipefail
source "$(dirname "$0")/mp_common.sh"

with_logs=0
for arg in "$@"; do
	case "$arg" in
	--logs) with_logs=1 ;;
	*)
		echo "usage: [NETID=your_netid] ./deploy_mp.sh [--logs]" >&2
		exit 1
		;;
	esac
done

read_nodes
total="${#NODES[@]}"
echo "NETID=$NETID  $total VMs"

echo
echo "==> build $BINARY (linux/amd64)"
if ! GOOS=linux GOARCH=amd64 go build -o "$BINARY" .; then
	echo "build failed, nothing deployed" >&2
	exit 1
fi

echo
echo "==> kill"
for n in $(seq 1 "$total"); do
	kill_vm "$n"
done

echo
echo "==> deploy $BINARY + $HOSTS_FILE"
for n in $(seq 1 "$total"); do
	host="$(vm_host "$n")"
	if scp $SSH_OPTS -q "$BINARY" "$HOSTS_FILE" "$NETID@$host:~/" </dev/null &&
		remote "$n" "chmod +x ~/$BINARY"; then
		printf 'VM %-3s %-40s ok\n' "$n" "$host"
	else
		printf 'VM %-3s %-40s FAILED\n' "$n" "$host"
	fi
done

if [ "$with_logs" -eq 1 ]; then
	echo
	echo "==> upload logs"
	for n in $(seq 1 "$total"); do
		push_log "$n" || echo "    FAILED"
	done
fi

echo
echo "==> start"
rc=0
for n in $(seq 1 "$total"); do
	start_vm "$n" || rc=1
done
exit "$rc"
