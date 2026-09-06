package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os/exec"
	"strings"
)

func runServer(args []string) {
	port := "8001"
	logFile := "machine.1.log"

	if len(args) >= 2 {
		port = args[0]
		logFile = args[1]
	}

	listener, err := net.Listen("tcp", ":"+port)
	if err != nil {
		panic(err)
	}
	defer listener.Close()
	fmt.Printf("Server listening on port %s for file %s ...\n", port, logFile)

	for {
		conn, err := listener.Accept()
		if err != nil {
			continue
		}
		go handleConnection(conn, logFile)
	}
}

func handleConnection(conn net.Conn, logFile string) {
	defer conn.Close()

	reader := bufio.NewReader(conn)
	reqLine, err := reader.ReadString('\n')
	if err != nil {
		return
	}

	var grepArgs []string
	if err := json.Unmarshal([]byte(reqLine), &grepArgs); err != nil {
		return
	}

	finalArgs := append(grepArgs, logFile)

	cmd := exec.Command("grep", finalArgs...)
	output, err := cmd.Output()

	outputStr := string(output)
	var lineCount int
	if err != nil || len(strings.TrimSpace(outputStr)) == 0 {
		lineCount = 0
		outputStr = ""
	} else {
		lineCount = strings.Count(outputStr, "\n")
		if !strings.HasSuffix(outputStr, "\n") {
			lineCount++
		}
	}

	response := fmt.Sprintf("[%s] Matches: %d\n%s", logFile, lineCount, outputStr)
	conn.Write([]byte(response))
}
