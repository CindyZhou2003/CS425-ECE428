# MP1
## Config
Directory: `MP1`
VM host: `fa26-cs425-23NN.cs.illinois.edu` ,see `host.txt`

## Deploy

1. Generate key and push to VMs for quick deployment
``` bash
ssh-keygen -t ed25519
for i in $(seq -w 1 10); do
  ssh-copy-id YOUR_NETID@fa26-cs425-23$i.cs.illinois.edu
done
```

2. Deploy (and redeploy after changing code)
``` bash
cd MP1
NETID=YOUR_NETID ./deploy.sh all # compile + push binary + each VM generates its own 60MB log + start + check
```
Each VM runs `genlog -only N` to create its own `machine.N.log`, so only the
5 MB binary crosses the network. `SEED` and `MB` env vars change the seed and
size (defaults 42 and 60). To regenerate one VM's log by hand, on that VM:
`./mp1-linux genlog -n 10 -only N`.

3. Execute grep
``` bash
# log in one VM and try grep command
ssh YOUR_NETID@fa26-cs425-2301.cs.illinois.edu
./mp1-linux client "heartbeat"

```

Other commands in `deploy.sh`
``` bash
./deploy.sh build
./deploy.sh push
./deploy.sh genlog
./deploy.sh start
./deploy.sh stop
./deploy.sh status
```

Go Format Commands
``` bash
gofmt -l .      # list files
gofmt -d a.go   # show diff
gofmt -w a.go   # format and write back
gofmt -w .      # format all the files in the directory
```

## Test
`TestDistributedGrep` runs against the real VMs (from `host.txt`, or
`MP1_VM_ADDRS=host:port,...`). It runs 5 rounds, for 6, 7, 8, 9 and 10 VMs. In each round:
1. over ssh, each of the K VMs generates its own ~60 MB log for a K-machine cluster
   (random lines plus planted known lines, with a different seed each round);
2. the test replays the same seeded generator in memory (writing nothing) to get each VM's exact counts,
   and checks that each VM's file size matches;
3. it greps 9 patterns (rare / somewhat frequent / frequent × one / some / all logs),
   then 10 `-E` / `-i` queries across all three frequencies (`OPT_*`, e.g. `-E "RPC call to peer timed out after [0-9]+ retries"`),
   `-v`, and a pattern that never occurs. For each node it checks the
   file name, the match count, and that the returned line count matches the header, then checks the cluster total;
4. `FAIL_STOP`: it kills the server on one VM (VM 2 to 6, a different one each round), checks the client
   reports that VM as unreachable while the other VMs' counts stay exact, then restarts it.

It is skipped when `NETID` is unset. Deploy first so every VM has the current binary and a running server:

```bash
NETID=YOUR_NETID ./deploy.sh all
NETID=YOUR_NETID go test -v -timeout 60m ./...

# Run one round, or one check within a round
NETID=YOUR_NETID go test -v -run 'TestDistributedGrep/VMs=6'
NETID=YOUR_NETID go test -v -run 'TestDistributedGrep/VMs=10/FAIL_STOP'
NETID=YOUR_NETID go test -v -run 'TestDistributedGrep/VMs=8/OPT_'
```

## Query latency
`bench` measures the time from sending a grep to every VM until all results are received.
Printing is excluded, since it would dominate for frequent patterns. It queries the
frequent / infrequent / rare phrases planted in every log, plus a `none` baseline that matches
nothing (grep still scans the whole file, so it shows the fixed cost of a query). It runs warm-up queries first
so the logs are in the page cache, and reports mean, sample standard deviation, min and max.

Run it **on a VM** (e.g. VM 1) after `./deploy.sh all`. From a laptop over VPN you would
mostly be measuring your own bandwidth:

```bash
ssh YOUR_NETID@fa26-cs425-2301.cs.illinois.edu
./mp1-linux bench -vms 4,6,8,10 -trials 20 -csv latency.csv  # one row per (VMs, pattern); CSV for plots
./mp1-linux bench -warmup 0 -trials 5                        # cold-ish: include the first read from disk
```
