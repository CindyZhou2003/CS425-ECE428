package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

const usage = "usage: mp2 <node|server|client> [args...]"

// The course cluster: fa26-cs425-2301 ... fa26-cs425-2310, VM 1 is the introducer
const (
	vmCount    = 10
	vmHostFmt  = "fa26-cs425-23%02d.cs.illinois.edu"
	gossipPort = 8000
	grepPort   = 8001
)

// The introducer is always VM 1
func introducerAddr() string { return fmt.Sprintf(vmHostFmt+":%d", 1, gossipPort) }

// Every VM's grep server, for the MP1 client to fan out to
func grepServers() []string {
	servers := make([]string, 0, vmCount)
	for i := 1; i <= vmCount; i++ {
		servers = append(servers, fmt.Sprintf(vmHostFmt+":%d", i, grepPort))
	}
	return servers
}

// Keeps the MP1 naming, with the machine number read off the VM hostname
func defaultLogFile() string {
	host, _ := os.Hostname()
	name, _, _ := strings.Cut(host, ".")
	if i, err := strconv.Atoi(strings.TrimPrefix(name, "fa26-cs425-23")); err == nil {
		return fmt.Sprintf("machine.%d.log", i)
	}
	return "machine.local.log"
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(1)
	}

	switch os.Args[1] {
	case "node":
		runNode(os.Args[2:])
	case "server":
		runServer(os.Args[2:])
	case "client":
		runClient(os.Args[2:])
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand %q\n%s\n", os.Args[1], usage)
		os.Exit(1)
	}
}
