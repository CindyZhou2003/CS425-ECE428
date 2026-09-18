package main

import (
	"encoding/json"
	"fmt"
	"math"
	"math/rand/v2"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// Protocol timing, sized so suspicion plus confirmation lands under the 3s first-detection bound
const (
	gossipPeriod   = 250 * time.Millisecond
	gossipFanout   = 3
	sweepPeriod    = 100 * time.Millisecond
	failTimeout    = 2500 * time.Millisecond // nosuspect: silence before declaring DEAD
	suspectTimeout = 1200 * time.Millisecond // suspect: silence before suspecting
	confirmTimeout = 1500 * time.Millisecond // suspect: window for the node to refute
	cleanupTimeout = 4 * time.Second         // outlives stale gossip so removed nodes aren't re-added
	joinRetries    = 5
	leaveRounds    = 3
	maxPacket      = 64 * 1024
)

const (
	msgGossip = "gossip"
	msgJoin   = "join"
)

type message struct {
	Type        string        `json:"type"`
	Members     []MemberEntry `json:"members"`
	Suspicion   bool          `json:"suspicion"`
	ModeVersion uint64        `json:"mode_version"`
}

// Stored as float64 bits so the receive loop reads it without a lock; survives leave/rejoin
var dropRate atomic.Uint64

// Takes a fraction in [0, 1], applied only to incoming messages
func SetDropRate(r float64) { dropRate.Store(math.Float64bits(r)) }

func DropRate() float64 { return math.Float64frombits(dropRate.Load()) }

// One group membership; leaving is terminal, so rejoining builds a new Node with a fresh ID
type Node struct {
	ID    string
	Table *MembershipTable

	conn         *net.UDPConn
	introducer   *net.UDPAddr
	isIntroducer bool
	done         chan struct{}

	modeMu      sync.Mutex
	modeVersion uint64
}

func NewNode(port int, introducer string, suspicion bool) (*Node, error) {
	intro, err := net.ResolveUDPAddr("udp", introducer)
	if err != nil {
		return nil, fmt.Errorf("resolving introducer: %w", err)
	}
	ip, err := localIP(intro)
	if err != nil {
		return nil, fmt.Errorf("finding local IP: %w", err)
	}
	conn, err := net.ListenUDP("udp", &net.UDPAddr{Port: port})
	if err != nil {
		return nil, err
	}

	// Timestamp distinguishes a rejoin from the earlier, already-removed incarnation of this host
	id := fmt.Sprintf("%s:%d:%d", ip, port, time.Now().UnixMilli())
	table := NewMembershipTable(id, suspicion)
	table.InitSelf(id)

	return &Node{
		ID:           id,
		Table:        table,
		conn:         conn,
		introducer:   intro,
		isIntroducer: intro.Port == port && intro.IP.Equal(ip),
		done:         make(chan struct{}),
	}, nil
}

// Picks the source address the OS would route to the introducer; UDP dial sends nothing
func localIP(remote *net.UDPAddr) (net.IP, error) {
	c, err := net.DialUDP("udp", nil, remote)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	return c.LocalAddr().(*net.UDPAddr).IP, nil
}

func (n *Node) Join() {
	LogEvent("[MEMBERSHIP] Joined as %s", n.ID)
	go n.receiveLoop()
	go n.gossipLoop()
	go n.sweepLoop()
	if !n.isIntroducer {
		go n.requestJoin()
	}
}

// Retries because the request itself can be dropped at the introducer
func (n *Node) requestJoin() {
	for range joinRetries {
		if len(n.Table.GetSnapshot()) > 1 {
			return
		}
		n.send(n.introducer.String(), n.encode(msgJoin, n.Table.GetSnapshot()))
		select {
		case <-n.done:
			return
		case <-time.After(2 * gossipPeriod):
		}
	}
	if len(n.Table.GetSnapshot()) == 1 {
		LogEvent("[JOIN] Introducer %s unreachable, no group joined", n.introducer)
	}
}

// Announces LEFT directly to every peer so a leave isn't mistaken for a crash
func (n *Node) Leave() {
	close(n.done)
	n.Table.MarkSelfLeft()
	LogEvent("[MEMBERSHIP] Leaving group as %s", n.ID)

	peers := n.Table.GetActivePeerAddresses()
	for range leaveRounds {
		payload := n.encode(msgGossip, n.Table.GetSnapshot())
		for _, p := range peers {
			n.send(p, payload)
		}
		time.Sleep(gossipPeriod)
	}
	n.conn.Close()
}

func (n *Node) gossipLoop() {
	ticker := time.NewTicker(gossipPeriod)
	defer ticker.Stop()
	for {
		select {
		case <-n.done:
			return
		case <-ticker.C:
		}
		n.Table.IncrementHeartbeat()

		peers := n.Table.GetActivePeerAddresses()
		rand.Shuffle(len(peers), func(i, j int) { peers[i], peers[j] = peers[j], peers[i] })
		payload := n.encode(msgGossip, n.Table.GetSnapshot())
		for _, p := range peers[:min(gossipFanout, len(peers))] {
			n.send(p, payload)
		}
	}
}

func (n *Node) receiveLoop() {
	buf := make([]byte, maxPacket)
	for {
		size, src, err := n.conn.ReadFromUDP(buf)
		if err != nil {
			return // conn closed by Leave
		}
		if rand.Float64() < DropRate() {
			LogEvent("[DROP] Dropped %d-byte message from %s", size, src)
			continue
		}
		var msg message
		if err := json.Unmarshal(buf[:size], &msg); err != nil {
			LogEvent("[WARN] Malformed message from %s: %v", src, err)
			continue
		}

		// Mode first, so the merge below follows the group's current protocol
		n.adoptMode(msg)
		n.Table.MergeMemberList(msg.Members)

		if msg.Type == msgJoin && n.isIntroducer {
			LogEvent("[JOIN] Join request from %s", src)
			n.send(src.String(), n.encode(msgGossip, n.Table.GetSnapshot()))
		}
	}
}

func (n *Node) sweepLoop() {
	ticker := time.NewTicker(sweepPeriod)
	defer ticker.Stop()
	for {
		select {
		case <-n.done:
			return
		case now := <-ticker.C:
			n.Table.sweep(now)
		}
	}
}

// Advances entries whose heartbeat went quiet; kept here since the timeouts are protocol parameters
func (t *MembershipTable) sweep(now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()

	for id, e := range t.Members {
		if id == t.SelfID {
			continue
		}
		idle := now.Sub(e.LocalTime)
		switch {
		case e.Status == StatusDead || e.Status == StatusLeft:
			if idle > cleanupTimeout {
				delete(t.Members, id)
				LogEvent("[MEMBERSHIP] Removed %s (%s)", id, e.Status)
			}
		case !t.UseSuspicion:
			if idle > failTimeout {
				e.Status = StatusDead
				e.LocalTime = now
				LogEvent("[FAILURE] Detected failure of %s: no heartbeat for %v", id, idle.Round(time.Millisecond))
			}
		case e.Status == StatusAlive:
			if idle > suspectTimeout {
				e.Status = StatusSuspect
				e.LocalTime = now
				t.SuspectHistory = append(t.SuspectHistory, SuspectRecord{ID: id, SuspectTime: now})
				LogEvent("[SUSPECT] Suspecting %s: no heartbeat for %v", id, idle.Round(time.Millisecond))
			}
		case idle > confirmTimeout:
			e.Status = StatusDead
			e.LocalTime = now
			LogEvent("[FAILURE] Confirmed failure of %s: suspicion not refuted", id)
		}
	}
}

// Bumps the mode version so the switch spreads through gossip instead of staying local
func (n *Node) SwitchMode(suspicion bool) {
	n.modeMu.Lock()
	defer n.modeMu.Unlock()
	n.modeVersion++
	n.Table.SetSuspicionMode(suspicion)
	LogEvent("[PROTOCOL] Switched to %s (version %d)", isSuspect(suspicion), n.modeVersion)
}

// Newer version wins; on a tie from concurrent switches, suspect wins so the group still converges
func (n *Node) adoptMode(msg message) {
	n.modeMu.Lock()
	defer n.modeMu.Unlock()
	cur := n.Table.IsSuspicionEnabled()
	if msg.ModeVersion < n.modeVersion || (msg.ModeVersion == n.modeVersion && (msg.Suspicion == cur || cur)) {
		return
	}
	n.modeVersion = msg.ModeVersion
	if msg.Suspicion != cur {
		n.Table.SetSuspicionMode(msg.Suspicion)
		LogEvent("[PROTOCOL] Adopted %s from gossip (version %d)", isSuspect(msg.Suspicion), msg.ModeVersion)
	}
}

func isSuspect(suspicion bool) string {
	if suspicion {
		return "suspect"
	}
	return "nosuspect"
}

func (n *Node) encode(kind string, members []MemberEntry) []byte {
	n.modeMu.Lock()
	msg := message{Type: kind, Members: members, Suspicion: n.Table.IsSuspicionEnabled(), ModeVersion: n.modeVersion}
	n.modeMu.Unlock()
	payload, _ := json.Marshal(msg) // plain structs, can't fail
	return payload
}

// Loss is tolerated by design, so send errors are ignored
func (n *Node) send(addr string, payload []byte) {
	udp, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return
	}
	n.conn.WriteToUDP(payload, udp)
}
