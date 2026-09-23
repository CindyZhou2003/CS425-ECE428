package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"syscall"
)

const cliUsage = "usage: mp2 node [-port N] [-introducer host:port] [-log file] [-nosuspect] [-daemon] [-drop pct]"

const cliHelp = `commands:
  list_mem              print the membership list
  list_self             print this node's ID
  join                  join the group
  leave                 voluntarily leave the group
  display_suspects      print every node this process has ever suspected
  switch suspect|nosuspect
                        enable or disable the suspicion mechanism
  display_protocol      print whether suspicion is enabled
  set_drop_rate <pct>   drop this percentage of incoming messages
  help                  print this message`

// Holds what a rejoin needs, since Leave is terminal and a new Node gets a fresh ID
type cli struct {
	port       int
	introducer string
	node       *Node
	suspicion  bool
	daemon     bool
}

func runNode(args []string) {
	fs := flag.NewFlagSet("node", flag.ExitOnError)
	fs.Usage = func() { fmt.Fprintln(os.Stderr, cliUsage); fs.PrintDefaults() }
	port := fs.Int("port", gossipPort, "UDP port to gossip on")
	introducer := fs.String("introducer", introducerAddr(), "introducer host:port")
	logFile := fs.String("log", defaultLogFile(), "log file")
	nosuspect := fs.Bool("nosuspect", false, "start with the suspicion mechanism off")
	daemon := fs.Bool("daemon", false, "run without the command prompt, until killed")
	drop := fs.Float64("drop", 0, "percentage of incoming messages to drop from the start")
	fs.Parse(args)

	if *drop < 0 || *drop > 100 {
		fmt.Fprintf(os.Stderr, "drop must be a percentage between 0 and 100\n%s\n", cliUsage)
		os.Exit(1)
	}
	if err := InitLogger(*logFile); err != nil {
		fmt.Fprintf(os.Stderr, "error: opening log file: %v\n", err)
		os.Exit(1)
	}

	c := &cli{port: *port, introducer: *introducer, suspicion: !*nosuspect, daemon: *daemon}
	SetDropRate(*drop / 100)
	if *drop > 0 {
		LogEvent("[PROTOCOL] Receiver drop rate set to %.1f%%", *drop)
	}

	c.join()
	c.loop()
}

func (c *cli) loop() {
	// Started in the background by run_mp.sh: no terminal to read commands from,
	// so gossip until a signal instead of taking the empty stdin as an exit
	if c.daemon {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
		<-sig
		c.leave()
		return
	}

	fmt.Println(cliHelp)
	in := bufio.NewScanner(os.Stdin)
	for {
		fmt.Print("> ")
		if !in.Scan() {
			if err := in.Err(); err != nil {
				fmt.Fprintf(os.Stderr, "error: reading stdin: %v\n", err)
			}
			fmt.Println()
			c.leave()
			return
		}
		fields := strings.Fields(in.Text())
		if len(fields) == 0 {
			continue
		}
		c.dispatch(fields[0], fields[1:])
	}
}

func (c *cli) dispatch(cmd string, args []string) {
	switch cmd {
	case "list_mem":
		c.listMem()
	case "list_self":
		c.listSelf()
	case "join":
		c.join()
	case "leave":
		c.leave()
	case "display_suspects":
		c.displaySuspects()
	case "switch":
		c.switchMode(args)
	case "display_protocol":
		c.displayProtocol()
	case "set_drop_rate":
		c.setDropRate(args)
	case "help":
		fmt.Println(cliHelp)
	default:
		fmt.Printf("unknown command %q, try help\n", cmd)
	}
}

func (c *cli) listMem() {
	if c.node == nil {
		fmt.Println("not in the group")
		return
	}
	members := c.node.Table.GetSnapshot()
	sort.Slice(members, func(i, j int) bool { return members[i].ID < members[j].ID })

	fmt.Printf("%-34s %-8s %-10s %-12s %s\n", "ID", "STATUS", "HEARTBEAT", "INCARNATION", "LAST UPDATE")
	for _, m := range members {
		self := ""
		if m.ID == c.node.ID {
			self = "  (self)"
		}
		fmt.Printf("%-34s %-8s %-10d %-12d %s%s\n",
			m.ID, m.Status, m.Heartbeat, m.Incarnation, m.LocalTime.Format("15:04:05.000"), self)
	}
	fmt.Printf("%d member(s)\n", len(members))
}

func (c *cli) listSelf() {
	if c.node == nil {
		fmt.Println("not in the group")
		return
	}
	fmt.Println(c.node.ID)
}

func (c *cli) join() {
	if c.node != nil {
		fmt.Println("already in the group as " + c.node.ID)
		return
	}
	node, err := NewNode(c.port, c.introducer, c.suspicion)
	if err != nil {
		fmt.Printf("join failed: %v\n", err)
		return
	}
	c.node = node
	node.Join()
}

func (c *cli) leave() {
	if c.node == nil {
		fmt.Println("not in the group")
		return
	}
	c.node.Leave()
	c.node = nil
}

func (c *cli) displaySuspects() {
	if c.node == nil {
		fmt.Println("not in the group")
		return
	}
	history := c.node.Table.GetSuspectHistory()
	if len(history) == 0 {
		fmt.Println("no nodes suspected so far")
		return
	}
	for _, s := range history {
		fmt.Printf("%-34s suspected at %s\n", s.ID, s.SuspectTime.Format("2006-01-02 15:04:05.000"))
	}
}

func (c *cli) switchMode(args []string) {
	if len(args) != 1 {
		fmt.Println("usage: switch suspect|nosuspect")
		return
	}
	var suspicion bool
	switch args[0] {
	case "suspect":
		suspicion = true
	case "nosuspect":
		suspicion = false
	default:
		fmt.Println("usage: switch suspect|nosuspect")
		return
	}

	// Remembered so a later rejoin starts in the mode the operator last chose
	c.suspicion = suspicion
	if c.node == nil {
		fmt.Printf("not in the group, will join with suspicion %s\n", onOff(suspicion))
		return
	}
	c.node.SwitchMode(suspicion)
	fmt.Printf("suspicion %s\n", onOff(suspicion))
}

func (c *cli) displayProtocol() {
	suspicion := c.suspicion
	if c.node != nil {
		suspicion = c.node.Table.IsSuspicionEnabled()
	}
	fmt.Printf("protocol: Gossip%s (suspicion %s)\n", suspectSuffix(suspicion), onOff(suspicion))
}

func (c *cli) setDropRate(args []string) {
	if len(args) != 1 {
		fmt.Println("usage: set_drop_rate <percent>")
		return
	}
	pct, err := strconv.ParseFloat(strings.TrimSuffix(args[0], "%"), 64)
	if err != nil || pct < 0 || pct > 100 {
		fmt.Println("drop rate must be a percentage between 0 and 100")
		return
	}
	if c.node == nil {
		SetDropRate(pct / 100)
		LogEvent("[PROTOCOL] Receiver drop rate set to %.1f%%", pct)
		return
	}
	c.node.SetDropRate(pct / 100)
	fmt.Printf("drop rate %.1f%% on this node, spreading to the group\n", pct)
}

func onOff(suspicion bool) string {
	if suspicion {
		return "enabled"
	}
	return "disabled"
}

func suspectSuffix(suspicion bool) string {
	if suspicion {
		return "+S"
	}
	return ""
}
