# MP1
## Config
Directory: `MP1`
VM host: `fa26-cs425-23NN.cs.illinois.edu` ,see `host.txt`

## Deploy

1. Generate logs
```bash
go run . genlog -n 10
```

2. Generate key and push to VMs for quick deployment
``` bash
ssh-keygen -t ed25519
for i in $(seq -w 1 10); do
  ssh-copy-id xinyiz22@fa26-cs425-23$i.cs.illinois.edu
done
```

3. Redeploy after changing code
``` bash
cd MP1
NETID=YOUR_NETID ./deploy.sh all # compile+push+start+check
```
4. Execute grep
``` bash
# log in one VM and try grep command
ssh YOUR_NETID@fa26-cs425-2301.cs.illinois.edu
./mp1-linux client "heartbeat"

```

Other commands in `deploy.sh`
``` bash
./deploy.sh build
./deploy.sh push
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
Spawns background servers on local loopback ports to simulate cluster nodes automatically:

```bash
# Run the entire test suite
go test -v ./...

# Run specific tests
go test -v -run TestDistributedGrep
go test -v -run TestGrepOptions_RegexpFlags
go test -v -run TestQueryToleratesDownServer
