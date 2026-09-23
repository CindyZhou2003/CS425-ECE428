# MP2
Gossip-style failure detection, with an optional Suspicion mechanism (Gossip+S).
One daemon per VM keeps a full membership list and gossips it over UDP.

## Build
``` bash
cd MP2
go build -o mp2 .                                # local
GOOS=linux GOARCH=amd64 go build -o mp2-linux .  # for the VMs
```

## Deploy and connect
``` bash
export NETID=your_netid # enter you net id
# upload binary to VMs
for i in $(seq -w 1 10); do
  scp mp2-linux $NETID@fa26-cs425-23$i.cs.illinois.edu:~/
done
ssh $NETID@fa26-cs425-2301.cs.illinois.edu   # VM 1 = introducer, start it first
./mp2-linux node

ssh $NETID@fa26-cs425-2302.cs.illinois.edu   # VM i
./mp2-linux node
```

## Commands
Type them at the `>` prompt.

``` bash
list_mem # membership list: ID, status, heartbeat, incarnation, last update
list_self # this node's ID, e.g. 10.193.185.2:8000:1758300001456; run on two VMs to show the IDs differ
join  # automatic at startup; after a leave: rejoins with a NEW ID (fresh timestamp)
leave # voluntary leave; peers log "Communicated leave", not a failure; then list_mem on another VM: the entry disappears, no [FAILURE]
display_suspects # every node suspected since launch, with the time; after switch nosuspect: still lists the earlier suspicions
switch suspect # enable suspicion (Gossip+S), spreads to the group by gossip
switch nosuspect # disable it; membership list is kept, no restart needed
display_protocol  # -> protocol: Gossip+S (suspicion enabled);after switch nosuspect -> protocol: Gossip (suspicion disabled)
set_drop_rate 30 # drop 30% of INCOMING messages group-wide, spreads by gossip like switch; each drop is logged as [DROP]
set_drop_rate 0 # back to no loss (the default)

help  # reprint this list
```

## Driving the VMs from your laptop
`host.txt` lists the VMs, line N is VM N. `vm.sh` ssh's in as `$NETID` (falling back to your
local user name, which is why an unset `NETID` fails with `Permission denied`) and uses
`BatchMode`, so set `NETID` and install your key on all 10 VMs first:
``` bash
export NETID=your_netid
ssh-copy-id $NETID@fa26-cs425-2301.cs.illinois.edu   # once per VM
```
``` bash
./vm.sh run 3           # start the node on VM 3 (no VM numbers = all 10)
./vm.sh kill 3 4 5      # SIGKILL those nodes in parallel, for failure detection
./vm.sh fetch           # copy the VM logs into ./logs
./vm.sh clear           # truncate all nodes
```
`run` starts the node with `-daemon`, so it gossips but has no prompt: ssh in and run it by
hand on the VMs where you need to type commands. `kill` stamps a `[KILL]` line into each
victim's log first, so simultaneous-failure runs have a start time in the VMs' own clock
domain.

## Logs
Every membership change, detection, suspicion, protocol switch and drop goes to the
log file and the terminal with a local timestamp. Grep across VMs with MP1:
``` bash
./mp2-linux server                              # on every VM
./mp2-linux client -E '\[FAILURE\]|\[SUSPECT\]'
```
Tags: `[MEMBERSHIP]` `[FAILURE]` `[SUSPECT]` `[REFUTE]` `[PROTOCOL]` `[JOIN]` `[DROP]` `[BW]` `[KILL]`

## Measurements
Every number comes out of the logs: each node writes `[BW] sent=N recv=M bytes over 1s` once
a second, and `./vm.sh kill` writes `[KILL]` at the instant it kills a node. `analyze.py`
turns a directory of logs into the three metrics.
``` bash
export NETID=your_netid
./vm.sh kill # kill all VMs
./vm.sh clear  # clear logs before each run
# drop rate = 0,1,5,10,20%
NODE_FLAGS="-drop 5" ./vm.sh run # start all VMs with 5% drop rate with Gossip+S
NODE_FLAGS="-nosuspect -drop 5" ./vm.sh run # with pure Gossip(another run)
sleep 2000 # wait for 2000s
./vm.sh fetch  # save VM logs into ./logs
./analyze.py logs  # analyze bandwidth, false positive rate, detection times
mv logs logs-fp-suspect-05 # rename and save logs
./analyze.py logs --since 14:05:00 --until 14:10:00   # one trial out of a longer run
```

1. **Bandwidth vs group size**: no failures, N = 2, 4, 6, 8, 10 (`./vm.sh run 1 2 3 4`),
   ~60s each. Read `per node: mean ... B/s`, skipping the first ~10s of join traffic
   with `--since`.
2. **False positives vs drop rate**: N = 10, no kills, `NODE_FLAGS="-drop $D"` for
   D = 0, 1, 5, 10, 20, 30, **at least 5 minutes each**. Read `failures ... /s group`;
   under Gossip+S also report `suspicions`, since the ones that got `[REFUTE]`d never
   became false positives.
3. **Detection time vs simultaneous failures**: N = 10, `./vm.sh kill 3 4 5` for 1, 2, 3, 4
   victims, 5 trials each. `analyze.py` reports first/mean/last per `[KILL]`. Never kill
   VM 1: nothing can rejoin once the introducer is gone.

Spot-check a run across the VMs with MP1 grep instead of pulling the logs:
``` bash
./mp2-linux server                      # on every VM
./mp2-linux client -c -E '\[FAILURE\]'
```
