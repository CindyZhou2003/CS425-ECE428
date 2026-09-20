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

type nodeResult struct {
	addr    string
	logFile string
	matches int
	lines   string
	err     error
}

func runClient(args []string) {
	if len(args) < 1 {
		fmt.Println("usage: mp2 client [grep options...] <pattern>")
		return
	}

	serverList := grepServers()
	fmt.Printf("--- Querying grep %q across %d nodes ---\n\n", args, len(serverList))

	results := queryAll(serverList, args)

	// Print after all nodes finish so lines from different nodes don't interleave
	out := bufio.NewWriter(os.Stdout)
	for _, r := range results {
		if r.err != nil {
			continue
		}
		// prefix with the file name, like grep does with multiple files
		name := filepath.Base(r.logFile)
		for _, line := range strings.Split(strings.TrimSuffix(r.lines, "\n"), "\n") {
			if line != "" {
				fmt.Fprintf(out, "%s: %s\n", name, line)
			}
		}
	}
	out.Flush()

	total := 0
	fmt.Printf("\n--- Matches %q per node ---\n", args)
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

// Queries all servers in parallel
func queryAll(servers []string, grepArgs []string) []nodeResult {
	results := make([]nodeResult, len(servers))
	var wg sync.WaitGroup
	for i, addr := range servers {
		wg.Add(1)
		go func(slot int, target string) {
			defer wg.Done()
			results[slot] = queryNode(target, grepArgs)
		}(i, addr)
	}
	wg.Wait()
	return results
}

func queryNode(target string, grepArgs []string) nodeResult {
	res := nodeResult{addr: target}

	conn, err := net.DialTimeout("tcp", target, 2*time.Second)
	if err != nil {
		res.err = err
		return res
	}
	defer conn.Close()

	// send grep args as one JSON line
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

	// first line has the file name and match count, the rest are matching lines
	header, lines, _ := strings.Cut(string(rawOutput), "\n")
	res.lines = lines
	if name, count, ok := strings.Cut(header, "] Matches: "); ok {
		res.logFile = strings.TrimPrefix(name, "[")
		res.matches, _ = strconv.Atoi(strings.TrimSpace(count))
	}
	return res
}
