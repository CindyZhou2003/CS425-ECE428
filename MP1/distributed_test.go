package main

import (
	"fmt"
	"io"
	"math/rand"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	remoteBinary  = "mp1-linux"
	remoteLogFile = "machine.%d.log"
)

// clusterSizes are the rounds TestDistributedGrep runs: every round regenerates
// the logs for exactly that many VMs and repeats the full query matrix.
var clusterSizes = []int{6, 7, 8, 9, 10}

func requireNetID(t *testing.T) string {
	netid := os.Getenv("NETID")
	if netid == "" {
		t.Skip("NETID not set; run `NETID=<netid> ./deploy.sh all` then `NETID=<netid> go test -v -timeout 60m`")
	}
	return netid
}

func runSSH(netid, addr, command string) (string, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return "", err
	}
	out, err := exec.Command("ssh", "-o", "ConnectTimeout=10", "-o", "BatchMode=yes",
		netid+"@"+host, command).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("ssh %s: %v: %s", host, err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// pickMembers draws the k VMs a round runs on: a random subset of the n VMs in
// host.txt, as ascending 1-based indices. It is derived from the round's seed,
// so rerunning a failed round picks the same VMs.
func pickMembers(n, k int, seed int64) []int {
	rng := rand.New(rand.NewSource(seed ^ 0x2545F4914F6CDD1D))
	members := rng.Perm(n)[:k]
	for i := range members {
		members[i]++
	}
	sort.Ints(members)
	return members
}

// generateRemoteLogs has each of the given VMs generate its own log for the
// cluster cfg.members, in parallel, and returns each file's size as reported
// by the VM. addrs[i] is VM cfg.members[i]. Nothing but the command crosses
// the network.
func generateRemoteLogs(t *testing.T, netid string, addrs []string, cfg config) []int64 {
	t.Helper()
	sizes := make([]int64, len(addrs))
	errs := make([]error, len(addrs))

	list := make([]string, len(cfg.members))
	for i, m := range cfg.members {
		list[i] = strconv.Itoa(m)
	}
	members := strings.Join(list, ",")

	var wg sync.WaitGroup
	for i, addr := range addrs {
		wg.Add(1)
		go func(i int, addr string) {
			defer wg.Done()
			idx := cfg.members[i]
			out, err := runSSH(netid, addr, fmt.Sprintf(
				"~/%s genlog -members %s -only %d -seed %d -mb %d -rare-rate %g -mid-rate %g -freq-rate %g >/dev/null && wc -c < ~/%s",
				remoteBinary, members, idx, cfg.seed, cfg.targetBytes>>20,
				cfg.rareRate, cfg.midRate, cfg.freqRate, fmt.Sprintf(remoteLogFile, idx)))
			if err != nil {
				errs[i] = err
				return
			}
			sizes[i], errs[i] = strconv.ParseInt(out, 10, 64)
		}(i, addr)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("generating log on VM %d (%s): %v", cfg.members[i], addrs[i], err)
		}
	}
	return sizes
}

// stopServer kills the grep server on one VM, the way a fail-stop crash would.
func stopServer(netid, addr string) error {
	_, err := runSSH(netid, addr, fmt.Sprintf(
		"pkill -x %[1]s; while pgrep -x %[1]s >/dev/null; do sleep 0.1; done", remoteBinary))
	return err
}

// startServer brings a VM's server back exactly as deploy.sh starts it.
// </dev/null and the redirects let ssh return instead of waiting on it.
func startServer(netid, addr string, idx int) error {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return err
	}
	if _, err := runSSH(netid, addr, fmt.Sprintf(
		"nohup ~/%s server %s ~/%s > ~/server.log 2>&1 </dev/null &",
		remoteBinary, port, fmt.Sprintf(remoteLogFile, idx))); err != nil {
		return err
	}
	if !waitForServer(addr, 10*time.Second) {
		return fmt.Errorf("server on %s did not come back", addr)
	}
	return nil
}

// optionCases exercise -E and -i at every frequency and scope. ids lists the
// planted patterns each query should match; random lines never do.
var optionCases = []struct {
	name string
	args []string
	ids  []string
}{
	{"OPT_RARE_ONE_fixed_prefix", []string{"-F", "unrecoverable disk corruption"}, []string{"RARE_ONE"}},
	{"OPT_RARE_SOME_-E_group", []string{"-E", "gossip message dropped due to (malformed|corrupt) header"}, []string{"RARE_SOME"}},
	{"OPT_RARE_ALL_-i", []string{"-i", "SWAP SPACE USAGE exceeded 90% threshold"}, []string{"RARE_ALL"}},
	{"OPT_MID_ONE_-E_digits", []string{"-E", "RPC call to peer timed out after [0-9]+ retries"}, []string{"MID_ONE"}},
	{"OPT_MID_SOME_-i", []string{"-i", "Membership List Out Of Sync"}, []string{"MID_SOME"}},
	{"OPT_MID_ALL_SOME_-E_alternation", []string{"-E", "leader election triggered|membership list out of sync"}, []string{"MID_ALL", "MID_SOME"}},
	{"OPT_FREQ_ONE_-E_digits", []string{"-E", "slow disk write detected, latency above [0-9]+ms"}, []string{"FREQ_ONE"}},
	{"OPT_FREQ_SOME_-i", []string{"-i", "GARBAGE COLLECTION PAUSE"}, []string{"FREQ_SOME"}},
	{"OPT_FREQ_ALL_-E_group", []string{"-E", "heartbeat acknowledged by (peer|leader)"}, []string{"FREQ_ALL"}},
	{"OPT_FREQ_ALL_-i_-E", []string{"-i", "-E", "HEARTBEAT ACK[a-z]+ BY PEER"}, []string{"FREQ_ALL"}},
}

// randomLineRegex matches every generated line that is not a planted pattern:
// those all end in "]: <verb> <noun>". Inverting it therefore selects exactly
// the planted lines, which gives -v a known answer without shipping ~60 MB of
// non-matching lines back per node.
func randomLineRegex() string {
	quote := func(words []string) string {
		out := make([]string, len(words))
		for i, w := range words {
			out[i] = strings.ReplaceAll(w, ".", `\.`)
		}
		return strings.Join(out, "|")
	}
	return "]: (" + quote(verbs) + ") (" + quote(nouns) + ")$"
}

// TestDistributedGrep is the MP1 distributed unit test. For each cluster size
// in clusterSizes it:
//  1. has every VM generate its own ~60 MB log: random lines plus planted
//     known lines at three frequencies (rare / somewhat frequent / frequent)
//     scoped to one / some / all of the machines;
//  2. derives the exact ground truth by replaying the same seeded generator
//     into io.Discard;
//  3. runs a grep for every pattern, plus grep options (-E, -i, -v), through
//     the querying program and checks each node's file name and match count
//     and the cluster-wide total;
//  4. kills one VM's server and checks the others are still collected exactly.
//
// Each round uses its own seed, so the VMs picked, the "some" subset and every
// file's content differ between rounds.
func TestDistributedGrep(t *testing.T) {
	netid := requireNetID(t)

	allAddrs, err := loadServers()
	if err != nil {
		t.Fatalf("loading server addresses: %v", err)
	}
	if need := clusterSizes[len(clusterSizes)-1]; len(allAddrs) < need {
		t.Fatalf("need at least %d VMs in host.txt, got %d", need, len(allAddrs))
	}

	for _, k := range clusterSizes {
		t.Run(fmt.Sprintf("VMs=%d", k), func(t *testing.T) {
			seed := int64(defaultSeed + k)
			members := pickMembers(len(allAddrs), k, seed)
			addrs := make([]string, k) // addrs[i] is VM members[i]
			for i, m := range members {
				addrs[i] = allAddrs[m-1]
			}
			cfg := config{
				members:     members,
				targetBytes: defaultSizeMB << 20,
				seed:        seed,
				rareRate:    defaultRareRate,
				midRate:     defaultMidRate,
				freqRate:    defaultFreqRate,
			}
			if err := cfg.validate(); err != nil {
				t.Fatal(err)
			}

			patterns := buildPatterns(cfg)
			want := make([]fileStats, k) // save logs for validation
			for i := range want {
				st, err := generateLog(io.Discard, cfg, patterns, members[i])
				if err != nil {
					t.Fatalf("computing ground truth for VM %d: %v", members[i], err)
				}
				want[i] = st
			}

			start := time.Now()
			sizes := generateRemoteLogs(t, netid, addrs, cfg)
			t.Logf("generated %d logs on VMs %v in %v (seed %d)", k, members, time.Since(start).Round(time.Millisecond), cfg.seed)

			// Compare log lines
			for i, size := range sizes {
				if size != want[i].bytes {
					t.Fatalf("VM %d: log is %d bytes but ground truth expects %d; redeploy with ./deploy.sh build push",
						members[i], size, want[i].bytes)
				}
			}

			// expect returns, per VM, how many lines hold any of the given patterns.
			// Each line holds at most one pattern, so the counts simply add up.
			expect := func(ids ...string) []int {
				out := make([]int, k)
				for i := range out {
					for _, id := range ids {
						out[i] += want[i].counts[id]
					}
				}
				return out
			}
			byID := make(map[string]pattern, len(patterns))
			allIDs := make([]string, 0, len(patterns))
			for _, p := range patterns {
				byID[p.id] = p
				allIDs = append(allIDs, p.id)
			}

			for _, p := range patterns {
				t.Run(p.id, func(t *testing.T) {
					checkQuery(t, addrs, members, nil, []string{"-F", p.phrase}, expect(p.id))
				})
			}

			// Grep options must pass through untouched: -E and -i queries across
			// all three frequencies, each written to hit known planted patterns.
			for _, tc := range optionCases {
				t.Run(tc.name, func(t *testing.T) {
					checkQuery(t, addrs, members, nil, tc.args, expect(tc.ids...))
				})
			}
			t.Run("REGEX_ALTERNATION_ALL_TIERS", func(t *testing.T) {
				args := []string{"-E", byID["RARE_ONE"].phrase + "|" + byID["MID_SOME"].phrase + "|" + byID["FREQ_ALL"].phrase}
				checkQuery(t, addrs, members, nil, args, expect("RARE_ONE", "MID_SOME", "FREQ_ALL"))
			})
			t.Run("INVERT_MATCH", func(t *testing.T) {
				checkQuery(t, addrs, members, nil, []string{"-v", "-E", randomLineRegex()}, expect(allIDs...))
			})
			t.Run("ABSENT_PATTERN", func(t *testing.T) {
				checkQuery(t, addrs, members, nil, []string{"-F", "this phrase is never planted in any log"}, make([]int, k))
			})

			// Rotates through the 2nd..6th picked VM across rounds; the 1st picked
			// VM holds every ONE pattern.
			t.Run("FAIL_STOP", func(t *testing.T) {
				victim, addr := members[k-5], addrs[k-5]
				if err := stopServer(netid, addr); err != nil {
					t.Fatalf("stopping VM %d: %v", victim, err)
				}
				t.Cleanup(func() {
					if err := startServer(netid, addr, victim); err != nil {
						t.Errorf("restarting VM %d: %v; later rounds will fail until ./deploy.sh start", victim, err)
					}
				})

				down := map[int]bool{victim: true}
				checkQuery(t, addrs, members, down, []string{"-F", byID["FREQ_ALL"].phrase}, expect("FREQ_ALL"))
				checkQuery(t, addrs, members, down, []string{"-F", byID["RARE_SOME"].phrase}, expect("RARE_SOME"))
			})
		})
	}
}

// checkQuery runs one grep across the cluster and verifies that every node
// answered from its own log file with exactly the expected number of matches,
// and that the aggregate is right. addrs[i] is VM members[i]. VMs in down (by
// VM index) must report an error instead, and are left out of the expected total.
func checkQuery(t *testing.T, addrs []string, members []int, down map[int]bool, grepArgs []string, expected []int) {
	t.Helper()
	start := time.Now()
	results := queryAll(addrs, grepArgs)
	elapsed := time.Since(start)

	got, wantTotal := 0, 0
	for i, res := range results {
		idx := members[i]
		if down[idx] {
			if res.err == nil {
				t.Errorf("VM %d (%s) is down but answered with %d matches", idx, res.addr, res.matches)
			}
			continue
		}
		wantTotal += expected[i]
		if res.err != nil {
			t.Errorf("VM %d (%s): query failed: %v", idx, res.addr, res.err)
			continue
		}
		if wantFile := fmt.Sprintf(remoteLogFile, idx); filepath.Base(res.logFile) != wantFile {
			t.Errorf("VM %d (%s): answered from %q, want %q", idx, res.addr, res.logFile, wantFile)
		}
		if res.matches != expected[i] {
			t.Errorf("VM %d (%s): %d matches, want %d", idx, res.addr, res.matches, expected[i])
		}
		// The count comes from the server's header; the lines are what the user
		// actually sees, so both must agree.
		if lines := strings.Count(res.lines, "\n"); lines != res.matches {
			t.Errorf("VM %d (%s): header says %d matches but %d lines were returned", idx, res.addr, res.matches, lines)
		}
		got += res.matches
	}
	if got != wantTotal {
		t.Errorf("grep %q: %d total matches across %d VMs, want %d", grepArgs, got, len(addrs)-len(down), wantTotal)
	}
	t.Logf("%d matches across %d VMs in %v", got, len(addrs)-len(down), elapsed.Round(time.Millisecond))
}

// waitForServer polls an address until a TCP connection is established
// or the timeout expires.
func waitForServer(addr string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
		if err == nil {
			conn.Close()
			return true
		}
		time.Sleep(200 * time.Millisecond)
	}
	return false
}
