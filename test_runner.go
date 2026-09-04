package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
)

var defaultServers = []string{
	"127.0.0.1:8001",
	"127.0.0.1:8002",
	"127.0.0.1:8003",
}

type TestCase struct {
	Name          string
	Args          []string
	ExpectedTotal int
}

func main() {
	testCases := []TestCase{
		{
			Name:          "Rare Pattern (Single Machine)",
			Args:          []string{"CRITICAL_RARE_ERROR_KEYWORD"},
			ExpectedTotal: 1,
		},
		{
			Name:          "Frequent Pattern (All Machines)",
			Args:          []string{"COMMON_FREQ_STATUS"},
			ExpectedTotal: 300,
		},
		{
			Name:          "Somewhat Frequent (Subset of Machines)",
			Args:          []string{"SOMEWHAT_FREQ_WARN"},
			ExpectedTotal: 40,
		},
		{
			Name:          "Regex Pattern (-E Flag)",
			Args:          []string{"-E", "(CRITICAL_RARE_ERROR_KEYWORD|SOMEWHAT_FREQ_WARN)"},
			ExpectedTotal: 41,
		},
		{
			Name:          "Zero Matches Query",
			Args:          []string{"NON_EXISTENT_KEYWORD_XYZ"},
			ExpectedTotal: 0,
		},
		{
			Name:          "Case Insensitive Query (-i Flag)",
			Args:          []string{"-i", "critical_rare_error_keyword"},
			ExpectedTotal: 1,
		},
	}

	fmt.Println("==========================================================")
	fmt.Println("       Running Distributed Log Querier Unit Tests         ")
	fmt.Println("==========================================================")

	passed := 0
	failed := 0

	for i, tc := range testCases {
		totalMatches, err := executeQuery(defaultServers, tc.Args)
		if err != nil {
			fmt.Printf("[FAIL] #%d %s\n       Error: %v\n", i+1, tc.Name, err)
			failed++
			continue
		}

		if totalMatches == tc.ExpectedTotal {
			fmt.Printf("[PASS] #%d %s\n       Expected: %d | Actual: %d\n", i+1, tc.Name, tc.ExpectedTotal, totalMatches)
			passed++
		} else {
			fmt.Printf("[FAIL] #%d %s\n       Expected: %d | Actual: %d (MISMATCH)\n", i+1, tc.Name, tc.ExpectedTotal, totalMatches)
			failed++
		}
	}

	fmt.Println("----------------------------------------------------------")
	fmt.Printf("Summary: %d Passed, %d Failed, %d Total\n", passed, failed, len(testCases))
	if failed == 0 {
		fmt.Println("Status: ALL UNIT TESTS PASSED SUCCESSFULLY.")
	} else {
		fmt.Println("Status: SOME UNIT TESTS FAILED.")
	}
	fmt.Println("==========================================================")
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
