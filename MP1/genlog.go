package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
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
	scope  string // "ONE" / "SOME" / "ALL"
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

// Defaults shared by the genlog CLI and the distributed test, so a cluster
// deployed with `deploy.sh genlog` matches the ground truth the test computes.
const (
	defaultSeed     = 42
	defaultSizeMB   = 60
	defaultRareRate = 0.0002
	defaultMidRate  = 0.01
	defaultFreqRate = 0.08

	// 2026-01-01T00:00:00Z. Fixed so generation is fully reproducible.
	baseEpoch = 1767225600
)

// config is the cluster-wide generation plan. Every VM must use the same one,
// differing only in which file index it writes.
type config struct {
	numFiles    int
	targetBytes int64
	seed        int64
	rareRate    float64
	midRate     float64
	freqRate    float64
}

func (cfg config) validate() error {
	if cfg.numFiles < 1 {
		return fmt.Errorf("cluster size must be at least 1, got %d", cfg.numFiles)
	}
	if cfg.targetBytes <= 0 {
		return fmt.Errorf("target size must be positive, got %d bytes", cfg.targetBytes)
	}
	// A file can match all three scopes, so its rates sum to 3x. Past 1.0 the
	// last patterns would fall outside [0,1) and never fire.
	if total := 3 * (cfg.rareRate + cfg.midRate + cfg.freqRate); total > 1 {
		return fmt.Errorf("rates too high: 3*(rare+mid+freq) = %g, must be <= 1", total)
	}
	return nil
}

// fileStats is the ground truth for one generated log file.
type fileStats struct {
	lines  int
	bytes  int64
	counts map[string]int // pattern id -> planted occurrences in this file
}

// fileRNG derives an independent stream per log file. Seeding per file (rather
// than running one stream across all of them) is what lets each VM generate
// only its own machine.N.log while the test replays any file's stream to get
// its exact contents.
func fileRNG(seed int64, fileIdx int) *rand.Rand {
	s := uint64(seed)*0x9E3779B97F4A7C15 + uint64(fileIdx)*0xBF58476D1CE4E5B9
	s ^= s >> 31
	return rand.New(rand.NewSource(int64(s)))
}

func buildPatterns(cfg config) []pattern {
	all := make(map[int]bool, cfg.numFiles)
	one := map[int]bool{1: true}
	for i := 1; i <= cfg.numFiles; i++ {
		all[i] = true
	}

	// Its own stream, so the SOME set depends only on (seed, numFiles) and every
	// VM independently picks the same subset.
	some := make(map[int]bool)
	someRNG := rand.New(rand.NewSource(cfg.seed ^ 0x5DEECE66D))
	numSome := max(cfg.numFiles/2, 1)
	for _, idx := range someRNG.Perm(cfg.numFiles)[:numSome] {
		some[idx+1] = true
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
				scope:  s.label,
				files:  s.files,
			})
		}
	}
	return patterns
}

// runGenlog is run on each VM to write that VM's own log file.
func runGenlog(args []string) {
	fs := flag.NewFlagSet("genlog", flag.ExitOnError)
	var (
		numFiles = fs.Int("n", 10, "number of VMs in the cluster the logs are planned for")
		only     = fs.Int("only", 0, "index of this VM's log file, 1..n (required)")
		sizeMB   = fs.Int("mb", defaultSizeMB, "size of the log file in MiB")
		outDir   = fs.String("outdir", "", "directory to write the log file into (default: home directory)")
		prefix   = fs.String("prefix", "machine.", "filename prefix; the file is named <prefix>N.log")
		seed     = fs.Int64("seed", defaultSeed, "random seed; must be the same on every VM")
		rareRate = fs.Float64("rare-rate", defaultRareRate, "per-line probability for rare events (~1 per 5000 lines)")
		midRate  = fs.Float64("mid-rate", defaultMidRate, "per-line probability for somewhat-frequent events (~1 per 100 lines)")
		freqRate = fs.Float64("freq-rate", defaultFreqRate, "per-line probability for frequent events (~1 per 12 lines)")
	)
	fs.Parse(args)

	cfg := config{
		numFiles: *numFiles, targetBytes: int64(*sizeMB) << 20, seed: *seed,
		rareRate: *rareRate, midRate: *midRate, freqRate: *freqRate,
	}
	if err := cfg.validate(); err != nil {
		fatalf("%v", err)
	}
	if *only < 1 || *only > *numFiles {
		fatalf("-only must name this VM's index in 1..%d, got %d", *numFiles, *only)
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
	st, err := writeLogFile(path, cfg, patterns, *only)
	if err != nil {
		fatalf("writing %s: %v", path, err)
	}
	printSummary(path, patterns, st)
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

// generateLog writes log file fileIdx of the cluster plan to w: random lines
// with the planted patterns mixed in, until cfg.targetBytes is reached. The
// test calls it with io.Discard to get a VM's exact counts without the file.
func generateLog(w io.Writer, cfg config, patterns []pattern, fileIdx int) (fileStats, error) {
	st := fileStats{counts: make(map[string]int, len(patterns))}
	rng := fileRNG(cfg.seed, fileIdx)

	// Start at a fixed instant in UTC rather than time.Now(): the content must
	// not depend on when or where it is generated, and both the date and the
	// zone offset are part of every line's length. Timestamps advance by a
	// small random jitter per line, so entries stay ordered.
	ts := time.Unix(baseEpoch, 0).UTC().Add(time.Duration(rng.Intn(24*3600)) * time.Second)

	for st.bytes < cfg.targetBytes {
		ts = ts.Add(time.Duration(rng.Intn(500)) * time.Millisecond)

		level := levels[rng.Intn(len(levels))]
		component := components[rng.Intn(len(components))]
		verb := verbs[rng.Intn(len(verbs))]
		noun := nouns[rng.Intn(len(nouns))]
		pid := 1000 + rng.Intn(9000) // process ID

		// each line matches at most one pattern through a weighted random draw
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
		st.lines++
	}
	return st, nil
}

func printSummary(path string, patterns []pattern, st fileStats) {
	fmt.Printf("Wrote %s (%d lines, %.1f MiB)\n", path, st.lines, float64(st.bytes)/(1<<20))
	fmt.Println("\npattern occurrences in this file:")
	for _, p := range patterns {
		scope := "all"
		if p.scope != "ALL" {
			idxs := make([]int, 0, len(p.files))
			for idx := range p.files {
				idxs = append(idxs, idx)
			}
			sort.Ints(idxs)
			names := make([]string, len(idxs))
			for i, idx := range idxs {
				names[i] = fmt.Sprintf("%d", idx)
			}
			scope = strings.Join(names, ",")
		}
		fmt.Printf("%-10s rate=%-8g scope=%-12s occurrences=%-6d grep for: %q\n",
			p.id, p.rate, scope, st.counts[p.id], p.phrase)
	}
}
