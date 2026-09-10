package main

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// getClusterAddresses resolves server endpoints dynamically.
// It supports two evaluation modes:
// 1. Local Simulation Mode (Default): When MP1_VM_ADDRS is unset, it starts N local
//    goroutines on distinct ports (18001..1800N) to simulate the VMs locally.
// 2. Real Cluster Mode: When MP1_VM_ADDRS is provided (comma-separated list of host:port),
//    it targets the actual provisioned CS cluster VMs directly.
func getClusterAddresses(t *testing.T, count int, tmpDir string, prefix string) []string {
	envAddrs := os.Getenv("MP1_VM_ADDRS")
	if envAddrs != "" {
		addrs := strings.Split(envAddrs, ",")
		t.Logf("Running in REAL CLUSTER mode with %d VMs: %v", len(addrs), addrs)
		return addrs
	}

	// Default: local multi-port simulation mode
	basePort := 18000
	addrs := make([]string, 0, count)
	for i := 1; i <= count; i++ {
		logFile := filepath.Join(tmpDir, fmt.Sprintf("%s%d.log", prefix, i))
		addr := fmt.Sprintf("127.0.0.1:%d", basePort+i)
		go runServer([]string{fmt.Sprintf("%d", basePort+i), logFile})
		addrs = append(addrs, addr)
	}

	for _, addr := range addrs {
		if !waitForServer(addr, 3*time.Second) {
			t.Fatalf("Local simulated server at %s never became reachable", addr)
		}
	}
	return addrs
}

// TestDistributedGrep satisfies the required MP1 specification for distributed unit tests.
// It generates deterministic log files with known planted lines across N (>5) machines,
// runs the distributed grep query path over TCP, and verifies that the aggregated count
// matches the exact ground truth without manual intervention.
//
// It evaluates all 9 required combinations:
// - Frequency: rare, somewhat-frequent, frequent
// - Distribution: occurring in one, some, or all machine log files.
func TestDistributedGrep(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := config{
		numFiles:   6,    // N > 5 as required by the specification
		minLines:   1,
		maxLines:   5000,
		exactLines: 5000, // Fixed line count guarantees deterministic ground truth
		outDir:     tmpDir,
		prefix:     "machine.",
		seed:       42,   // Fixed seed ensures repeatable tests
		rareRate:   0.0002,
		midRate:    0.01,
		freqRate:   0.08,
	}

	patterns, wantCounts, err := createLogs(cfg)
	if err != nil {
		t.Fatalf("createLogs failed: %v", err)
	}

	addrs := getClusterAddresses(t, cfg.numFiles, tmpDir, cfg.prefix)

	for _, p := range patterns {
		p := p // Pin range variable for subtest closure
		t.Run(p.id, func(t *testing.T) {
			total := 0
			for i, addr := range addrs {
				expectedFileName := fmt.Sprintf("%s%d.log", cfg.prefix, i+1)

				// Query the node using literal pattern matching (-F)
				res := queryNode(addr, []string{"-F", p.phrase})
				if res.err != nil {
					t.Fatalf("query to node %s failed: %v", addr, res.err)
				}

				// The spec requires matching file names and line counts to be verified.
				// If your node response struct exposes fileName, uncomment this check:
				
				if !strings.Contains(res.fileName, expectedFileName) {
					t.Errorf("Node %s: expected file name %q, got %q", addr, expectedFileName, res.fileName)
				}
				

				total += res.matches
			}

			if total != wantCounts[p.id] {
				t.Errorf("pattern %q (scope=%s): expected %d total matches across all machines, got %d",
					p.phrase, p.scope, wantCounts[p.id], total)
			}
		})
	}
}

// TestGrepOptions_RegexpFlags validates grep feature passthrough as mandated
// by the specification, specifically regular expression handling via -E,
// case-insensitivity (-i), and inverted matching (-v).
func TestGrepOptions_RegexpFlags(t *testing.T) {
	tmpDir := t.TempDir()

	// Construct deterministic log content for regex evaluation
	logPath := filepath.Join(tmpDir, "machine.1.log")
	content := []byte(
		"2026-09-10 INFO  User login success: UID=1001\n" +
			"2026-09-10 ERROR Failed connection to DB: timeout after 30s\n" +
			"2026-09-10 WARN  Disk space low: 95% full\n" +
			"2026-09-10 info  user logout: UID=1001\n" +
			"2026-09-10 FATAL System out of memory\n",
	)
	if err := os.WriteFile(logPath, content, 0644); err != nil {
		t.Fatalf("failed to write test log: %v", err)
	}

	addr := "127.0.0.1:18099"
	go runServer([]string{"18099", logPath})
	if !waitForServer(addr, 2*time.Second) {
		t.Fatalf("server failed to start at %s", addr)
	}

	testCases := []struct {
		name        string
		args        []string
		wantMatches int
	}{
		{
			name:        "Regex OR condition with -E",
			args:        []string{"-E", "(ERROR|FATAL)"},
			wantMatches: 2,
		},
		{
			name:        "Regex digit pattern with -E",
			args:        []string{"-E", "UID=[0-9]{4}"},
			wantMatches: 2,
		},
		{
			name:        "Case-insensitive matching with -i",
			args:        []string{"-i", "info"},
			wantMatches: 2, // Matches both "INFO" and "info"
		},
		{
			name:        "Invert match with -v",
			args:        []string{"-v", "2026-09-10"},
			wantMatches: 0, // All lines have the timestamp header
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			res := queryNode(addr, tc.args)
			if res.err != nil {
				t.Fatalf("query with args %v failed: %v", tc.args, res.err)
			}
			if res.matches != tc.wantMatches {
				t.Errorf("args %v: expected %d matches, got %d", tc.args, tc.wantMatches, res.matches)
			}
		})
	}
}

// TestQueryToleratesDownServer validates the fault-tolerance requirement:
// If one or more machines fail or become unreachable, the querying machine must
// not crash and must continue collecting valid responses from all reachable machines.
func TestQueryToleratesDownServer(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := config{
		numFiles:   2,
		minLines:   1,
		maxLines:   500,
		exactLines: 500,
		outDir:     tmpDir,
		prefix:     "machine.",
		seed:       7,
		rareRate:   0.0002,
		midRate:    0.01,
		freqRate:   0.08,
	}
	patterns, wantCounts, err := createLogs(cfg)
	if err != nil {
		t.Fatalf("createLogs failed: %v", err)
	}

	upAddr := "127.0.0.1:18100"
	downAddr := "127.0.0.1:18101" // Intentionally not started to simulate fail-stop failure

	go runServer([]string{"18100", filepath.Join(tmpDir, "machine.1.log")})
	if !waitForServer(upAddr, 2*time.Second) {
		t.Fatalf("server at %s never became reachable", upAddr)
	}

	// Locate the frequent pattern that occurs across all machines
	var allPattern *pattern
	for i := range patterns {
		if patterns[i].id == "FREQ_ALL" {
			allPattern = &patterns[i]
			break
		}
	}
	if allPattern == nil {
		t.Fatal("test setup error: FREQ_ALL pattern not found")
	}
	clusterAddrs := []string{upAddr, downAddr}
	totalMatches := 0
	failedCount := 0

	for _, addr := range clusterAddrs {
		res := queryNode(addr, []string{"-F", allPattern.phrase})
		if res.err != nil {
			// A down server must be captured as an error and must not cause a fatal crash
			failedCount++
			t.Logf("Expected failure observed for down server %s: %v", addr, res.err)
			continue
		}
		totalMatches += res.matches
	}

	// 1. Verify that exactly one failed node is detected
	if failedCount != 1 {
		t.Errorf("expected exactly 1 failed node, got %d", failedCount)
	}

	// 2. Verify that matching counts from surviving nodes are aggregated properly (must be positive and within bounds)
	if totalMatches <= 0 {
		t.Errorf("expected survivor node to yield matches, got %d", totalMatches)
	}
	if totalMatches > wantCounts[allPattern.id] {
		t.Errorf("survivor matches %d exceeded expected upper bound %d",
			totalMatches, wantCounts[allPattern.id])
	}
}

// waitForServer polls an address until a TCP connection is established
// or the designated timeout period expires.
func waitForServer(addr string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err == nil {
			conn.Close()
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}
