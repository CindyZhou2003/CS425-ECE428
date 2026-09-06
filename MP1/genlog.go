package main

import (
	"bufio"
	"flag"
	"fmt"
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

func buildPatterns(numFiles int, rareRate, midRate, freqRate float64, rng *rand.Rand) []pattern {
	all := make(map[int]bool, numFiles)
	one := map[int]bool{1: true}
	for i := 1; i <= numFiles; i++ {
		all[i] = true
	}

	some := make(map[int]bool)
	numSome := max(numFiles/2, 1)
	for _, idx := range rng.Perm(numFiles)[:numSome] {
		some[idx+1] = true
	}

	tiers := []struct {
		label string
		rate  float64
	}{
		{"RARE", rareRate}, {"MID", midRate}, {"FREQ", freqRate},
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

func runGenlog(args []string) {
	fs := flag.NewFlagSet("genlog", flag.ExitOnError)
	var (
		numFiles   = fs.Int("n", 10, "number of log files to generate")
		minLines   = fs.Int("min", 100, "minimum number of lines per file")
		maxLines   = fs.Int("max", 100000, "maximum number of lines per file")
		exactLines = fs.Int("lines", 0, "if > 0, every file gets exactly this many lines (overrides -min/-max)")
		outDir     = fs.String("outdir", "logs", "output directory for generated log files")
		prefix     = fs.String("prefix", "machine.", "filename prefix; files are named <prefix>N.log")
		seed       = fs.Int64("seed", 0, "random seed (0 = derive from current time)")
		rareRate   = fs.Float64("rare-rate", 0.0002, "per-line probability for rare events (~1 per 5000 lines)")
		midRate    = fs.Float64("mid-rate", 0.01, "per-line probability for somewhat-frequent events (~1 per 100 lines)")
		freqRate   = fs.Float64("freq-rate", 0.08, "per-line probability for frequent events (~1 per 12 lines)")
	)
	fs.Parse(args)

	if err := createLogs(config{
		numFiles: *numFiles, minLines: *minLines, maxLines: *maxLines,
		exactLines: *exactLines, outDir: *outDir, prefix: *prefix, seed: *seed,
		rareRate: *rareRate, midRate: *midRate, freqRate: *freqRate,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

type config struct {
	numFiles   int
	minLines   int
	maxLines   int
	exactLines int
	outDir     string
	prefix     string
	seed       int64
	rareRate   float64
	midRate    float64
	freqRate   float64
}

func createLogs(cfg config) error {
	if cfg.minLines < 1 || cfg.maxLines < cfg.minLines {
		return fmt.Errorf("require 1 <= min <= max lines")
	}
	// A file can match all three scopes, so its rates sum to 3x. Past 1.0 the
	// last patterns would fall outside [0,1) and never fire.
	if total := 3 * (cfg.rareRate + cfg.midRate + cfg.freqRate); total > 1 {
		return fmt.Errorf("rates too high: 3*(rare+mid+freq) = %g, must be <= 1", total)
	}
	if cfg.seed == 0 {
		cfg.seed = time.Now().UnixNano()
	}
	rng := rand.New(rand.NewSource(cfg.seed))
	fmt.Printf("using seed %d (pass -seed %d to reproduce this exact run)\n", cfg.seed, cfg.seed)

	if err := os.MkdirAll(cfg.outDir, 0o755); err != nil {
		return fmt.Errorf("creating output dir: %w", err)
	}

	patterns := buildPatterns(cfg.numFiles, cfg.rareRate, cfg.midRate, cfg.freqRate, rng)
	counts := make(map[string]int, len(patterns))

	for i := 1; i <= cfg.numFiles; i++ {
		lineCount := cfg.exactLines
		if lineCount <= 0 {
			lineCount = cfg.minLines + rng.Intn(cfg.maxLines-cfg.minLines+1)
		}

		path := filepath.Join(cfg.outDir, fmt.Sprintf("%s%d.log", cfg.prefix, i))
		if err := writeLogFile(path, i, lineCount, patterns, counts, rng); err != nil {
			return fmt.Errorf("writing %s: %w", path, err)
		}
		fmt.Printf("Wrote %s (%d lines)\n", path, lineCount)
	}

	fmt.Println("\npattern occurrence counts for verifying:")
	for _, p := range patterns {
		var scope string
		if p.scope == "ALL" {
			scope = "all"
		} else {
			fileIdxs := make([]int, 0, len(p.files))
			for idx := range p.files {
				fileIdxs = append(fileIdxs, idx)
			}
			sort.Ints(fileIdxs)
			names := make([]string, len(fileIdxs))
			for i, idx := range fileIdxs {
				names[i] = fmt.Sprintf("%d", idx)
			}
			scope = strings.Join(names, ",")
		}
		fmt.Printf("%-10s rate=%-8g scope=%s occurrences=%-6d grep for: %q\n",
			p.id, p.rate, scope, counts[p.id], p.phrase)
	}
	return nil
}

func writeLogFile(path string, fileIdx, lines int, patterns []pattern,
	counts map[string]int, rng *rand.Rand) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	w := bufio.NewWriter(f)
	defer w.Flush()

	// Start timestamps at a random point in the last 24h and advance
	// forward by a small random jitter per line, so entries stay ordered.
	ts := time.Now().Add(-time.Duration(rng.Intn(24*3600)) * time.Second)

	for i := 1; i <= lines; i++ {
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
				counts[p.id]++
				break
			}
		}
		if _, err := fmt.Fprintf(w, "%s [%s] %s[%d]: %s\n",
			ts.Format("2006-01-02T15:04:05.000Z07:00"), level, component, pid, msg); err != nil {
			return err
		}
	}
	return nil
}
