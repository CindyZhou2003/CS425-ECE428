# MP1
## Overview
Distributed grep in Go: query the logs on all VMs from any one VM.

```
               mp1-linux client -F "pattern"   (on any VM)
                              │
           grep args as JSON over TCP, one goroutine per VM
          ┌───────────────────┼───────────────────┐
          ▼                   ▼                   ▼
    VM 1: server        VM 2: server   ...  VM N: server
    grep machine.1.log  grep machine.2.log  grep machine.N.log
          │                   │                   │
          └───────────────────┼───────────────────┘
                              │
           "[machine.N.log] Matches: n" + matching lines
                              ▼
           matching lines + per-VM counts + TOTAL  (UNREACHABLE if no connection in 2s)
```

```
MP1/
├── main.go               entry point: mp1 <server|client|genlog|bench>
├── server.go             TCP server, runs grep on its local log, returns count + lines
├── client.go             sends the query to all VMs in parallel, prints lines + counts
├── genlog.go             generates reproducible test logs with planted lines
├── bench.go              measures query latency (mean / stddev)
├── distributed_test.go   end-to-end test on the real VMs
├── deploy_mp.sh          kill + build + push + start on every VM (--logs also uploads logs/)
├── run_mp.sh             start the MP on given VM(s)
├── kill_mp.sh            SIGKILL the MP on given VM(s)
├── push_logs.sh          upload logs/machine.N.log to VM N
├── mp_common.sh          shared helpers for the scripts above
├── deploy.sh             older flow with genlog, used by the Test section
└── host.txt              VM list, one host:port per line (line N = VM N, uses machine.N.log)
```
VM host: `fa26-cs425-23NN.cs.illinois.edu`

## Deploy

1. Generate key and push to VMs for quick deployment
``` bash
ssh-keygen -t ed25519
for i in $(seq -w 1 10); do
  ssh-copy-id YOUR_NETID@fa26-cs425-23$i.cs.illinois.edu
done
# Input your password for 10 times
```

2. Deploy (and redeploy after changing code). VM N serves `~/machine.N.log`, uploaded from local `logs/`
``` bash
cd MP1
export NETID=YOUR_NETID
./deploy_mp.sh --logs  # first time: kill + build + push binary/host.txt + upload logs + start
./deploy_mp.sh         # after a code change: same, logs already on the VMs
```

Other scripts, every one prints `VM, PID` pairs
``` bash
./run_mp.sh 3          # start the MP on VM 3 (several: ./run_mp.sh 3 5 7)
./kill_mp.sh 3         # SIGKILL the MP on VM 3
./push_logs.sh         # upload logs/machine.N.log to VM N, one by one (or ./push_logs.sh 3)
```
Env var: `NETID` (default `$USER`).
`deploy.sh` still generates synthetic logs with `genlog`, and the Test section below uses it.

3. Execute grep
``` bash
ssh YOUR_NETID@fa26-cs425-2301.cs.illinois.edu # connect to one VM
./mp1-linux client "heartbeat" # matching lines + per-VM counts + TOTAL
```

## Self-test queries
``` bash
# Frequency: planted in every log
./mp1-linux client -F "heartbeat acknowledged by peer" # frequent (~8%)   TOTAL 632730
./mp1-linux client -F "leader election triggered due to missed heartbeats" # infrequent (~1%)         TOTAL 78936
./mp1-linux client -F "swap space usage exceeded 90% threshold" # rare (~0.02%)            TOTAL 1631

# Scope: one log (VM 1) or some logs (VMs 3,6,8,9,10)
./mp1-linux client -F "unrecoverable disk corruption" # rare, VM 1 only          TOTAL 160
./mp1-linux client -F "RPC call to peer timed out after 3 retries" # infrequent, VM 1 only    TOTAL 7660
./mp1-linux client -F "gossip message dropped due to malformed header" # rare, VMs 3,6,8,9,10     TOTAL 763
./mp1-linux client -F "garbage collection pause completed" # frequent, VMs 3,6,8,9,10 TOTAL 314682

# -i
./mp1-linux client -i "SWAP SPACE USAGE exceeded 90% THRESHOLD" # rare                     TOTAL 1631
./mp1-linux client -i "Garbage Collection Pause" # frequent, VMs 3,6,8,9,10 TOTAL 314682

# -E
./mp1-linux client -E 'RPC call to peer timed out after [0-9]+ retries' # infrequent, VM 1 only   TOTAL 7660
./mp1-linux client -E 'leader election triggered|membership list out of sync'# infrequent, all + some  TOTAL 118512
./mp1-linux client -E 'heartbeat acknowledged by (peer|leader)' # frequent                TOTAL 632730
./mp1-linux client -E '\[FATAL\] rpc\[[0-9]+\]: failed (node|vm)[0-9]' # random lines            TOTAL 4350

# -i and -E together
./mp1-linux client -i -E 'HEARTBEAT ACK[a-z]+ BY PEER' # frequent                TOTAL 632730
./mp1-linux client -i -E '\[fatal\] rpc\[[0-9]+\]: FAILED (node|vm)[0-9]' # random lines            TOTAL 4350

# Edge cases
./mp1-linux client -F "this phrase is never planted in any log" # 0 on every VM
./mp1-linux client -F "unrecoverable disk corruption" # 160 lines, all from machine.1.log
./mp1-linux client -c "heartbeat"  # each VM shows 1 (grep prints one count line)
```
Fail-stop: `./kill_mp.sh N`, the client shows VM N as `UNREACHABLE` and still counts the rest.
`./run_mp.sh N` brings it back.

## Test
``` bash
NETID=YOUR_NETID ./deploy.sh all
NETID=YOUR_NETID go test -v -timeout 60m ./...

# one round, or one check in a round
NETID=YOUR_NETID go test -v -run 'TestDistributedGrep/VMs=6'
NETID=YOUR_NETID go test -v -run 'TestDistributedGrep/VMs=10/FAIL_STOP'
NETID=YOUR_NETID go test -v -run 'TestDistributedGrep/VMs=8/OPT_'
```
5 rounds on 6-10 VMs (random subset of `host.txt`). Each round:
1. each VM generates a ~60 MB log with planted lines (rare / mid / frequent × one / some / all VMs)
2. expected counts are recomputed locally from the same seed, and file sizes are checked
3. 9 planted patterns, 10 `-E` / `-i` queries, `-v`, and an absent pattern: checks each VM's file name and count, plus TOTAL
4. `FAIL_STOP`: kill one VM, the others must still be exact

Skipped if `NETID` is not set.

## Query latency
Run on a VM, not from a laptop over VPN.
``` bash
./mp1-linux bench -vms 4,6,8,10 -trials 5  # mean / stddev / min / max per (VMs, pattern)
```
Time is from sending the query to the last VM's reply.
Patterns: frequent / infrequent / rare, plus `none` (no match, just the cost of scanning the file).

## Go format
``` bash
gofmt -l .      # list files
gofmt -d a.go   # show diff
gofmt -w a.go   # format and write back
gofmt -w .      # format all the files in the directory
```