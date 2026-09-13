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

// one test round per cluster size
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

func pickMembers(n, k int, seed int64) []int {
	rng := rand.New(rand.NewSource(seed))
	members := rng.Perm(n)[:k]
	for i := range members {
		members[i]++
	}
	sort.Ints(members)
	return members
}

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

// Kills the server on a VM to simulate a crash
func stopServer(netid, addr string) error {
	_, err := runSSH(netid, addr, fmt.Sprintf(
		"pkill -x %[1]s; while pgrep -x %[1]s >/dev/null; do sleep 0.1; done", remoteBinary))
	return err
}

// Restarts the server the same way deploy.sh does; without the redirects
// ssh would hang on the background process
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

// -E / -i queries at each frequency and scope, ids are the patterns each should match
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

// Matches only the random lines, so -v returns just the planted ones
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

// For each cluster size:
//  1. each VM generates a ~60 MB log with planted lines
//     (rare/mid/frequent x one/some/all VMs)
//  2. expected counts are recomputed locally from the same seed
//  3. run queries (with -E, -i, -v), check per-VM and total counts
//  4. kill one VM, check the others still return correct results
//
// Each round uses a different seed
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
			want := make([]fileStats, k) // expected stats per VM
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

			// file sizes must match
			for i, size := range sizes {
				if size != want[i].bytes {
					t.Fatalf("VM %d: log is %d bytes but ground truth expects %d; redeploy with ./deploy.sh build push",
						members[i], size, want[i].bytes)
				}
			}

			// per-VM sum of the given patterns' counts (a line has at most one)
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

			// grep flags should pass through unchanged
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

			// victim is the 2nd-6th VM depending on round; never the 1st, which
			// has all the ONE patterns
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

// Runs one query and checks each VM's file name and count plus the total;
// VMs in down should fail and are left out of the total
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
		// header count should match the lines actually returned
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

// Retries a TCP connect until it works or times out
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
