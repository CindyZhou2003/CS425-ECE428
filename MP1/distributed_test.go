package main

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
	"bufio"
	"encoding/json"
	"io"
	"sync"

)

const (
	testBasePort = 9100
	testNumFiles = 10
)

var testServers []string

func testLogPath(n int) string {
	return fmt.Sprintf("logs/machine.%d.log", n)
}

func TestMain(m *testing.M) {
	for n := 1; n <= testNumFiles; n++ {
		if _, err := os.Stat(testLogPath(n)); err != nil {
			fmt.Fprintf(os.Stderr, "missing %s -- run: go run . genlog -n %d\n", testLogPath(n), testNumFiles)
			os.Exit(1)
		}
	}
	for n := 1; n <= testNumFiles; n++ {
		port := strconv.Itoa(testBasePort + n)
		go runServer([]string{port, testLogPath(n)})
		testServers = append(testServers, "127.0.0.1:"+port)
	}
	for _, addr := range testServers {
		if err := waitForListener(addr); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
	os.Exit(m.Run())
}

func waitForListener(addr string) error {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			conn.Close()
			return nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	return fmt.Errorf("server at %s never came up", addr)
}

// localGrepCount is the oracle: `grep -c` reports grep's own count, arrived at
// without the server's line-counting code, so agreement is real evidence.
func localGrepCount(t *testing.T, grepArgs []string, n int) int {
	t.Helper()
	args := append([]string{"-c"}, grepArgs...)
	args = append(args, testLogPath(n))
	out, err := exec.Command("grep", args...).Output()
	// grep exits 1 on no match but still prints "0".
	if err != nil && len(out) == 0 {
		return 0
	}
	count, convErr := strconv.Atoi(strings.TrimSpace(string(out)))
	if convErr != nil {
		t.Fatalf("grep -c %v %s: unparsable output %q (err %v)", grepArgs, testLogPath(n), out, err)
	}
	return count
}

func localGrepTotal(t *testing.T, grepArgs []string) int {
	t.Helper()
	total := 0
	for n := 1; n <= testNumFiles; n++ {
		total += localGrepCount(t, grepArgs, n)
	}
	return total
}

func TestDistributedGrepMatchesLocalGrep(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"plain word", []string{"heartbeat"}},
		{"rare phrase", []string{"unrecoverable disk corruption"}},
		{"frequent phrase", []string{"heartbeat acknowledged by peer"}},
		{"case insensitive", []string{"-i", "HEARTBEAT"}},
		{"regex alternation", []string{"-E", "(FATAL|unrecoverable)"}},
		{"whole word", []string{"-w", "rpc"}},
		{"no matches", []string{"NO_SUCH_PATTERN_XYZ"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			want := localGrepTotal(t, tc.args)
			got, err := executeQuery(testServers, tc.args)
			if err != nil {
				t.Fatalf("executeQuery(%v): %v", tc.args, err)
			}
			if got != want {
				t.Errorf("grep %v: distributed total = %d, local grep -c total = %d", tc.args, got, want)
			}
			t.Logf("grep %v -> %d matches across %d nodes", tc.args, got, testNumFiles)
		})
	}
}

// genlog places ONE-scope phrases in a single file, SOME in half of them, and
// ALL in every file. If that spread is wrong the log set is not coordinated and
// the distributed results stop being meaningful, even when the totals agree.
//
// These use the FREQ phrases on purpose: at rate 0.08 even the shortest file
// expects hundreds of hits, so an absence is a real defect. A RARE phrase
// (0.0002) can legitimately miss a short file and would make this flaky.
func TestPatternScopeDistribution(t *testing.T) {
	cases := []struct {
		scope     string
		phrase    string
		wantFiles int
	}{
		{"ONE", "slow disk write detected, latency above 50ms", 1},
		{"SOME", "garbage collection pause completed", max(testNumFiles/2, 1)},
		{"ALL", "heartbeat acknowledged by peer", testNumFiles},
	}

	for _, tc := range cases {
		t.Run(tc.scope, func(t *testing.T) {
			var hits []int
			for n := 1; n <= testNumFiles; n++ {
				if localGrepCount(t, []string{tc.phrase}, n) > 0 {
					hits = append(hits, n)
				}
			}
			if len(hits) != tc.wantFiles {
				t.Errorf("%s phrase appears in %d files %v, want %d", tc.scope, len(hits), hits, tc.wantFiles)
			}
			t.Logf("%s phrase found in files %v", tc.scope, hits)
		})
	}
}

// A correct grand total can still hide a wrong per-node split, so check that
// each node reports the count for its own file and names that file.
func TestPerNodeCountsMatchEachFile(t *testing.T) {
	args := []string{"heartbeat"}
	for n := 1; n <= testNumFiles; n++ {
		addr := testServers[n-1]
		res := queryNode(addr, args)
		if res.err != nil {
			t.Fatalf("queryNode(%s): %v", addr, res.err)
		}
		if want := testLogPath(n); res.logFile != want {
			t.Errorf("%s reported log file %q, want %q", addr, res.logFile, want)
		}
		if want := localGrepCount(t, args, n); res.matches != want {
			t.Errorf("%s reported %d matches, local grep -c = %d", addr, res.matches, want)
		}
	}
}

func TestUnreachableNodeReportsError(t *testing.T) {
	res := queryNode("127.0.0.1:9999", []string{"heartbeat"})
	if res.err == nil {
		t.Fatal("querying a dead port returned no error")
	}
	if res.matches != 0 {
		t.Errorf("dead node reported %d matches, want 0", res.matches)
	}
}

// A node that is down must not fail the whole query; the client drops it and
// still returns the matches from every node that answered.
func TestDownNodeIsSkipped(t *testing.T) {
	args := []string{"heartbeat acknowledged by peer"}

	full, err := executeQuery(testServers, args)
	if err != nil {
		t.Fatalf("executeQuery: %v", err)
	}

	withDead := append([]string{"127.0.0.1:9999"}, testServers...)
	got, err := executeQuery(withDead, args)
	if err != nil {
		t.Fatalf("executeQuery with dead node: %v", err)
	}
	if got != full {
		t.Errorf("total with an unreachable node = %d, want %d", got, full)
	}
}

func executeQuery(servers []string, grepArgs []string) (int, error) {
	reqPayload, err := json.Marshal(grepArgs)
	if err != nil {
		return 0, err
	}
	reqData := append(reqPayload, '\n')

	var wg sync.WaitGroup
	var mu sync.Mutex
	totalLines := 0

	for _, addr := range servers {
		wg.Add(1)
		go func(target string) {
			defer wg.Done()
			count := querySingleServer(target, reqData)
			mu.Lock()
			totalLines += count
			mu.Unlock()
		}(addr)
	}

	wg.Wait()
	return totalLines, nil
}

func querySingleServer(target string, reqData []byte) int {
	conn, err := net.DialTimeout("tcp", target, 2*time.Second)
	if err != nil {
		return 0
	}
	defer conn.Close()

	_, err = conn.Write(reqData)
	if err != nil {
		return 0
	}

	rawOutput, err := io.ReadAll(conn)
	if err != nil {
		return 0
	}

	scanner := bufio.NewScanner(strings.NewReader(string(rawOutput)))
	if scanner.Scan() {
		header := scanner.Text()
		parts := strings.Split(header, "Matches: ")
		if len(parts) == 2 {
			count, _ := strconv.Atoi(strings.TrimSpace(parts[1]))
			return count
		}
	}
	return 0
}
