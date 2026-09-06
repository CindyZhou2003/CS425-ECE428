package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: mp1 <server|client|genlog|testrunner> [args...]")
		os.Exit(1)
	}

	switch os.Args[1] {
	case "server":
		runServer(os.Args[2:])
	case "client":
		runClient(os.Args[2:])
	case "genlog":
		runGenlog(os.Args[2:])
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand %q\nusage: mp1 <server|client|genlog|testrunner> [args...]\n", os.Args[1])
		os.Exit(1)
	}
}
