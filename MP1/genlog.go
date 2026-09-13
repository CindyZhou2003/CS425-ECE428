package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

var levels = []string{"TRACE", "DEBUG", "INFO", "WARN", "ERROR", "FATAL"}

var (
	components = []string{
		"auth", "network", "scheduler", "storage", "cache",
		"heartbeat", "membership", "replication", "rpc", "worker",
	}

	verbs = []string{
		"started", "completed", "failed", "retrying", "timed out",
		"connected to", "disconnected from", "received message from",
		"sent message to", "elected leader", "detected failure of",
		"joined group", "left group", "flushed buffer for",
		"acquired lock on", "released lock on",
	}

	nouns = []string{
		"node1", "node2", "node3", "node4", "node5", "node6", "node7", "node8",
		"vm1.cs.illinois.edu", "vm2.cs.illinois.edu", "vm3.cs.illinois.edu",
		"vm4.cs.illinois.edu", "vm5.cs.illinois.edu", "vm6.cs.illinois.edu",
		"vm7.cs.illinois.edu", "vm8.cs.illinois.edu", "vm9.cs.illinois.edu",
		"connection", "socket", "partition", "shard", "queue", "session",
		"replica", "peer", "buffer", "task-queue",
	}
)

type pattern struct {
	id     string
	phrase string
	rate   float64
	files  map[int]bool
}

var phrases = map[string]map[string]string{
	"RARE": {
		"ONE":  "unrecoverable disk corruption detected in log segment, node halting",
		"SOME": "gossip message dropped due to malformed header",
		"ALL":  "swap space usage exceeded 90% threshold",
	},
	"MID": {
		"ONE":  "RPC call to peer timed out after 3 retries",
		"SOME": "membership list out of sync with majority quorum",
		"ALL":  "leader election triggered due to missed heartbeats",
	},
	"FREQ": {
		"ONE":  "slow disk write detected, latency above 50ms",
		"SOME": "garbage collection pause completed",
		"ALL":  "heartbeat acknowledged by peer",
	},
}

// Shared by genlog and the test, so logs from deploy.sh match what the test expects
const (
	defaultSeed     = 42
	defaultSizeMB   = 60
	defaultRareRate = 0.0002
	defaultMidRate  = 0.01
	defaultFreqRate = 0.08

	// 2026-01-01 00:00 UTC
	baseEpoch = 1767225600
)

// Must be the same on every VM, only the file index differs
type config struct {
	members     []int // sorted VM indices; VM N writes machine.N.log
	targetBytes int64
	seed        int64
	rareRate    float64
	midRate     float64
	freqRate    float64
}

func (cfg config) validate() error {
	if len(cfg.members) < 1 {
		return fmt.Errorf("cluster must have at least 1 VM, got %v", cfg.members)
	}
	// sorted and unique, so every VM computes the same ONE/SOME sets
	for i, m := range cfg.members {
		if m < 1 || (i > 0 && m <= cfg.members[i-1]) {
			return fmt.Errorf("members must be distinct VM indices >= 1, got %v", cfg.members)
		}
	}
	if cfg.targetBytes <= 0 {
		return fmt.Errorf("target size must be positive, got %d bytes", cfg.targetBytes)
	}
	// A file can be in all 3 scopes, so rates add up 3 times; over 1 some
	// patterns never get picked
	if total := 3 * (cfg.rareRate + cfg.midRate + cfg.freqRate); total > 1 {
		return fmt.Errorf("rates too high: 3*(rare+mid+freq) = %g, must be <= 1", total)
	}
	return nil
}

// Expected size and per-pattern counts of one log file
type fileStats struct {
	bytes  int64
	counts map[string]int // pattern id -> count
}

// Seeded per file, so a VM can generate just its own log and the test can
// recompute any one file
func fileRNG(seed int64, fileIdx int) *rand.Rand {
	s := uint64(seed) + uint64(fileIdx)
	s ^= s >> 31
	return rand.New(rand.NewSource(int64(s)))
}

func buildPatterns(cfg config) []pattern {
	n := len(cfg.members)
	all := make(map[int]bool, n)
	one := map[int]bool{cfg.members[0]: true}
	for _, m := range cfg.members {
		all[m] = true
	}

	// separate RNG so every VM picks the same SOME set
	some := make(map[int]bool)
	someRNG := rand.New(rand.NewSource(cfg.seed))
	numSome := max(n/2, 1)
	for _, i := range someRNG.Perm(n)[:numSome] {
		some[cfg.members[i]] = true
	}

	tiers := []struct {
		label string
		rate  float64
	}{
		{"RARE", cfg.rareRate}, {"MID", cfg.midRate}, {"FREQ", cfg.freqRate},
	}
	scopes := []struct {
		label string
		files map[int]bool
	}{
		{"ONE", one}, {"SOME", some}, {"ALL", all},
	}

	var patterns []pattern
	for _, t := range tiers {
		for _, s := range scopes {
			patterns = append(patterns, pattern{
				id:     fmt.Sprintf("%s_%s", t.label, s.label),
				phrase: phrases[t.label][s.label],
				rate:   t.rate,
				files:  s.files,
			})
		}
	}
	return patterns
}

// Writes this VM's log file
func runGenlog(args []string) {
	fs := flag.NewFlagSet("genlog", flag.ExitOnError)
	var (
		numFiles = fs.Int("n", 10, "number of VMs in the cluster the logs are planned for, VMs 1..n")
		members  = fs.String("members", "", "comma-separated VM indices in the cluster, e.g. 2,5,7 (overrides -n)")
		only     = fs.Int("only", 0, "index of this VM's log file, one of the cluster's VMs (required)")
		sizeMB   = fs.Int("mb", defaultSizeMB, "size of the log file in MiB")
		outDir   = fs.String("outdir", "", "directory to write the log file into (default: home directory)")
		prefix   = fs.String("prefix", "machine.", "filename prefix; the file is named <prefix>N.log")
		seed     = fs.Int64("seed", defaultSeed, "random seed; must be the same on every VM")
		rareRate = fs.Float64("rare-rate", defaultRareRate, "per-line probability for rare events (~1 per 5000 lines)")
		midRate  = fs.Float64("mid-rate", defaultMidRate, "per-line probability for somewhat-frequent events (~1 per 100 lines)")
		freqRate = fs.Float64("freq-rate", defaultFreqRate, "per-line probability for frequent events (~1 per 12 lines)")
	)
	fs.Parse(args)

	vms, err := parseMembers(*members, *numFiles)
	if err != nil {
		fatalf("%v", err)
	}
	cfg := config{
		members: vms, targetBytes: int64(*sizeMB) << 20, seed: *seed,
		rareRate: *rareRate, midRate: *midRate, freqRate: *freqRate,
	}
	if err := cfg.validate(); err != nil {
		fatalf("%v", err)
	}
	if !slices.Contains(cfg.members, *only) {
		fatalf("-only must name this VM's index, one of %v, got %d", cfg.members, *only)
	}
	if *outDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			fatalf("finding home directory: %v", err)
		}
		*outDir = home
	}

	path := filepath.Join(*outDir, fmt.Sprintf("%s%d.log", *prefix, *only))
	patterns := buildPatterns(cfg)
	if _, err := writeLogFile(path, cfg, patterns, *only); err != nil {
		fatalf("writing %s: %v", path, err)
	}
}

// Sorted VM indices from -members, or 1..n if empty
func parseMembers(list string, n int) ([]int, error) {
	var vms []int
	if list == "" {
		for i := 1; i <= n; i++ {
			vms = append(vms, i)
		}
		return vms, nil
	}
	for _, f := range strings.Split(list, ",") {
		m, err := strconv.Atoi(strings.TrimSpace(f))
		if err != nil {
			return nil, fmt.Errorf("-members: %v", err)
		}
		vms = append(vms, m)
	}
	sort.Ints(vms)
	return vms, nil
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "error: "+format+"\n", args...)
	os.Exit(1)
}

func writeLogFile(path string, cfg config, patterns []pattern, fileIdx int) (fileStats, error) {
	f, err := os.Create(path)
	if err != nil {
		return fileStats{}, err
	}
	w := bufio.NewWriterSize(f, 1<<20)
	st, err := generateLog(w, cfg, patterns, fileIdx)
	if err == nil {
		err = w.Flush()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return st, err
}

// Random lines mixed with planted patterns until the target size;
// the test also uses it to get the counts without writing a file
func generateLog(w io.Writer, cfg config, patterns []pattern, fileIdx int) (fileStats, error) {
	st := fileStats{counts: make(map[string]int, len(patterns))}
	rng := fileRNG(cfg.seed, fileIdx)

	// Fixed start time so every run gives the same bytes (the test checks file size)
	ts := time.Unix(baseEpoch, 0).UTC().Add(time.Duration(rng.Intn(24*3600)) * time.Second)

	for st.bytes < cfg.targetBytes {
		ts = ts.Add(time.Duration(rng.Intn(500)) * time.Millisecond)

		level := levels[rng.Intn(len(levels))]
		component := components[rng.Intn(len(components))]
		verb := verbs[rng.Intn(len(verbs))]
		noun := nouns[rng.Intn(len(nouns))]
		pid := 1000 + rng.Intn(9000)

		// at most one planted pattern per line
		msg := fmt.Sprintf("%s %s", verb, noun)
		u, cum := rng.Float64(), 0.0
		for _, p := range patterns {
			if !p.files[fileIdx] {
				continue
			}
			cum += p.rate
			if u < cum {
				msg = p.phrase
				st.counts[p.id]++
				break
			}
		}

		n, err := fmt.Fprintf(w, "%s [%s] %s[%d]: %s\n",
			ts.Format("2006-01-02T15:04:05.000-07:00"), level, component, pid, msg)
		if err != nil {
			return st, err
		}
		st.bytes += int64(n)
	}
	return st, nil
}
