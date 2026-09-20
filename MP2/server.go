package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"os/exec"
	"strings"
)

func runServer(args []string) {
	fs := flag.NewFlagSet("server", flag.ExitOnError)
	port := fs.Int("port", grepPort, "TCP port to serve grep on")
	logFile := fs.String("log", defaultLogFile(), "log file to grep")
	fs.Parse(args)

	addr := fmt.Sprintf(":%d", *port)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		panic(err)
	}
	defer listener.Close()
	fmt.Printf("Server listening on port %d for file %s ...\n", *port, *logFile)

	for {
		conn, err := listener.Accept()
		if err != nil {
			continue
		}
		go handleConnection(conn, *logFile)
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

	// grep exits 1 when nothing matches, so any error counts as 0
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
