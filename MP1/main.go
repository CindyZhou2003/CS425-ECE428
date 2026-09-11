package main

import (
	"fmt"
	"os"
)

const usage = "usage: mp1 <server|client|genlog|bench> [args...]"

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(1)
	}

	switch os.Args[1] {
	case "server":
		runServer(os.Args[2:])
	case "client":
		runClient(os.Args[2:])
	case "genlog":
		runGenlog(os.Args[2:])
	case "bench":
		runBench(os.Args[2:])
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand %q\n%s\n", os.Args[1], usage)
		os.Exit(1)
	}
}
