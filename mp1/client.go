package main

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// List of target servers (locally simulated via different ports)
var serverList = []string{
	"127.0.0.1:8001",
	"127.0.0.1:8002",
	"127.0.0.1:8003",
}

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Usage: go run client.go <pattern>")
		return
	}
	pattern := os.Args[1]

	var wg sync.WaitGroup
	var mu sync.Mutex
	totalLines := 0

	fmt.Printf("--- Querying pattern: %q across %d nodes ---\n\n", pattern, len(serverList))

	for _, addr := range serverList {
		wg.Add(1)
		go func(target string) {
			defer wg.Done()
			lines := queryNode(target, pattern)

			mu.Lock()
			totalLines += lines
			mu.Unlock()
		}(addr)
	}

	wg.Wait()
	fmt.Printf("\n--- Query Complete: Total matches across all active nodes = %d ---\n", totalLines)
}

func queryNode(target string, pattern string) int {
	// Enforce a 2-second timeout to handle down/unresponsive machines gracefully
	conn, err := net.DialTimeout("tcp", target, 2*time.Second)
	if err != nil {
		fmt.Printf("[ERROR] Failed to reach %s (Node down or unreachable)\n", target)
		return 0
	}
	defer conn.Close()

	// Send pattern ending with newline
	_, err = conn.Write([]byte(pattern + "\n"))
	if err != nil {
		fmt.Printf("[ERROR] Failed to send query to %s\n", target)
		return 0
	}

	// Read full response
	rawOutput, err := io.ReadAll(conn)
	if err != nil {
		fmt.Printf("[ERROR] Failed reading response from %s\n", target)
		return 0
	}

	outputStr := string(rawOutput)
	if len(outputStr) == 0 {
		return 0
	}

	// Print node output directly to terminal
	fmt.Print(outputStr)

	// Extract match count from the header line: "[machine.x.log] Matches: N"
	scanner := bufio.NewScanner(strings.NewReader(outputStr))
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
