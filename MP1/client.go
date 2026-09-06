package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// hostsFile lets the VM list change without recompiling; the localhost ports
// below are the fallback for testing several servers on one machine.
const hostsFile = "host.txt"

var defaultServers = []string{
	"127.0.0.1:8001",
	"127.0.0.1:8002",
	"127.0.0.1:8003",
}

// loadServers reads one host:port per line, skipping blanks and # comments.
func loadServers() []string {
	data, err := os.ReadFile(hostsFile)
	if err != nil {
		return defaultServers
	}
	var servers []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "#") {
			servers = append(servers, line)
		}
	}
	if len(servers) == 0 {
		return defaultServers
	}
	return servers
}

type nodeResult struct {
	addr    string
	logFile string
	matches int
	lines   string
	err     error
}

func runClient(args []string) {
	// Counts only by default: a broad pattern matches six figures of lines, and
	// rendering those to a terminal takes far longer than the query itself.
	// --summary is kept as a no-op since it names what now happens anyway.
	showLines := false
	var grepArgs []string
	for _, a := range args {
		switch a {
		case "--lines":
			showLines = true
		case "--summary":
		default:
			grepArgs = append(grepArgs, a)
		}
	}
	args = grepArgs

	if len(args) < 1 {
		fmt.Println("Usage: mp1 client [--lines] [grep options...] <pattern>")
		fmt.Println("  --lines   also print each matching line, tagged with its log file")
		return
	}

	// The server derives its match count from how many lines grep printed. These
	// flags replace those lines with a count, a file name, or nothing, so the
	// count would come back as 1 or 0 with no sign anything went wrong.
	for _, a := range args {
		switch a {
		case "-c", "--count", "-l", "--files-with-matches",
			"-L", "--files-without-match", "-q", "--quiet", "--silent":
			fmt.Printf("error: %s suppresses grep's matching lines, which is how each node counts.\n", a)
			fmt.Println("Per-node counts are printed by default; drop this flag.")
			return
		}
	}

	serverList := loadServers()
	fmt.Printf("--- Querying grep %q across %d nodes ---\n\n", args, len(serverList))

	// Each goroutine owns one slot, so the report follows host.txt order rather
	// than whichever node answered first.
	results := make([]nodeResult, len(serverList))
	var wg sync.WaitGroup
	for i, addr := range serverList {
		wg.Add(1)
		go func(slot int, target string) {
			defer wg.Done()
			results[slot] = queryNode(target, args)
		}(i, addr)
	}
	wg.Wait()

	// Printing only after every node has answered: concurrent writes to stdout
	// get split apart once a response outgrows the pipe buffer, which shuffles
	// one node's matching lines into another's.
	if showLines {
		out := bufio.NewWriter(os.Stdout)
		for _, r := range results {
			if r.err != nil {
				continue
			}
			// Tag each line with its source, the way grep does when given more
			// than one file. Base name only: the server is started with an
			// absolute path, and repeating /home/<netid>/ on every line buries
			// the part that differs. The table below maps file back to host.
			name := filepath.Base(r.logFile)
			for _, line := range strings.Split(strings.TrimSuffix(r.lines, "\n"), "\n") {
				if line != "" {
					fmt.Fprintf(out, "%s: %s\n", name, line)
				}
			}
		}
		out.Flush()
	}

	total := 0
	fmt.Printf("\n--- Matches per node ---\n")
	for _, r := range results {
		if r.err != nil {
			fmt.Printf("%-38s %-16s %10s\n", r.addr, "-", "UNREACHABLE")
			continue
		}
		total += r.matches
		fmt.Printf("%-38s %-16s %10d\n", r.addr, filepath.Base(r.logFile), r.matches)
	}
	fmt.Printf("%-38s %-16s %10d\n", "TOTAL", "", total)
}

func queryNode(target string, grepArgs []string) nodeResult {
	res := nodeResult{addr: target}

	// Enforce a 2-second timeout to handle down/unresponsive machines gracefully
	conn, err := net.DialTimeout("tcp", target, 2*time.Second)
	if err != nil {
		res.err = err
		return res
	}
	defer conn.Close()

	// The server decodes one JSON array per connection, then execs grep with it.
	payload, err := json.Marshal(grepArgs)
	if err != nil {
		res.err = err
		return res
	}
	if _, err = conn.Write(append(payload, '\n')); err != nil {
		res.err = err
		return res
	}

	rawOutput, err := io.ReadAll(conn)
	if err != nil {
		res.err = err
		return res
	}
	if len(rawOutput) == 0 {
		res.err = fmt.Errorf("empty response")
		return res
	}

	// The response is "[<logfile>] Matches: <n>" then the matching lines.
	header, lines, _ := strings.Cut(string(rawOutput), "\n")
	res.lines = lines
	if name, count, ok := strings.Cut(header, "] Matches: "); ok {
		res.logFile = strings.TrimPrefix(name, "[")
		res.matches, _ = strconv.Atoi(strings.TrimSpace(count))
	}
	return res
}
