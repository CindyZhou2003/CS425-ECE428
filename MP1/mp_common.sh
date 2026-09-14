# Shared helpers for run_mp.sh, kill_mp.sh, deploy_mp.sh and push_logs.sh
# Line N of host.txt is VM N, which serves ~/machine.N.log

cd "$(dirname "${BASH_SOURCE[0]}")"

NETID="${NETID:-$USER}"
HOSTS_FILE="host.txt"
BINARY="mp1-linux"
LOG_DIR="logs"
SSH_OPTS="-o ConnectTimeout=10 -o BatchMode=yes"

# read host:port lines from host.txt into NODES, NODES[0] is VM 1
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

# check every argument is a VM number in host.txt
check_vms() {
	if [ "$#" -eq 0 ]; then
		echo "error: no VM number given" >&2
		exit 1
	fi
	for n in "$@"; do
		if ! [[ "$n" =~ ^[0-9]+$ ]] || [ "$n" -lt 1 ] || [ "$n" -gt "${#NODES[@]}" ]; then
			echo "error: VM number must be 1-${#NODES[@]}, got '$n'" >&2
			exit 1
		fi
	done
}

vm_host() {
	local node="${NODES[$(($1 - 1))]}"
	echo "${node%:*}"
}

vm_port() {
	local node="${NODES[$(($1 - 1))]}"
	echo "${node##*:}"
}

remote() {
	ssh -n $SSH_OPTS "$NETID@$(vm_host "$1")" "$2"
}

# SIGKILL our server on VM $1, print a VM, PID line per killed process
kill_vm() {
	local n="$1" out
	# -x matches the exact name, -f would also match this ssh shell
	if ! out=$(remote "$n" "
		pids=\$(pgrep -u \$(id -un) -x $BINARY)
		[ -n \"\$pids\" ] && kill -9 \$pids
		echo \$pids
	" 2>&1); then
		printf 'VM %-3s %-40s UNREACHABLE: %s\n' "$n" "$(vm_host "$n")" "$out"
		return 1
	fi
	if [ -z "$out" ]; then
		printf 'VM %-3s %-40s not running\n' "$n" "$(vm_host "$n")"
		return 0
	fi
	for pid in $out; do
		printf 'VM %-3s %-40s killed  PID %s\n' "$n" "$(vm_host "$n")" "$pid"
	done
}

# start the server on VM $1, print its VM, PID line
start_vm() {
	local n="$1" port out
	port="$(vm_port "$n")"
	# redirect stdin so ssh doesn't wait on the server
	if ! out=$(remote "$n" "
		pids=\$(pgrep -u \$(id -un) -x $BINARY)
		if [ -n \"\$pids\" ]; then echo RUNNING \$pids; exit 0; fi
		[ -f ~/machine.$n.log ] || echo NOLOG
		nohup ~/$BINARY server $port ~/machine.$n.log > ~/server.log 2>&1 </dev/null &
		pid=\$!
		sleep 1
		if kill -0 \$pid 2>/dev/null; then echo STARTED \$pid; else echo DIED; head -5 ~/server.log; fi
	" 2>&1); then
		printf 'VM %-3s %-40s UNREACHABLE: %s\n' "$n" "$(vm_host "$n")" "$out"
		return 1
	fi

	local host
	host="$(vm_host "$n")"
	if [[ "$out" == *NOLOG* ]]; then
		printf 'VM %-3s %-40s warning: ~/machine.%s.log missing, run ./push_logs.sh\n' "$n" "$host" "$n"
	fi
	case "$(echo "$out" | grep -E '^(RUNNING|STARTED|DIED)')" in
	STARTED*)
		printf 'VM %-3s %-40s started PID %s  port %s\n' "$n" "$host" "$(echo "$out" | awk '/^STARTED/{print $2}')" "$port"
		;;
	RUNNING*)
		printf 'VM %-3s %-40s already running PID %s, kill_mp.sh %s first\n' "$n" "$host" \
			"$(echo "$out" | awk '/^RUNNING/{$1=""; print substr($0, 2)}')" "$n"
		return 1
		;;
	*)
		printf 'VM %-3s %-40s FAILED to start:\n' "$n" "$host"
		echo "$out" | grep -v -E '^(NOLOG|DIED)$' | sed 's/^/    /'
		return 1
		;;
	esac
}

# upload logs/machine.N.log to VM N
push_log() {
	local n="$1" file="$LOG_DIR/machine.$1.log"
	if [ ! -f "$file" ]; then
		printf 'VM %-3s %-40s skip, %s missing\n' "$n" "$(vm_host "$n")" "$file"
		return 1
	fi
	printf 'VM %-3s %-40s machine.%s.log  %s\n' "$n" "$(vm_host "$n")" "$n" "$(ls -lh "$file" | awk '{print $5}')"
	scp $SSH_OPTS "$file" "$NETID@$(vm_host "$n"):~/machine.$n.log" </dev/null
}
