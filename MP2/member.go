package main

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

// Member status constants
const (
	StatusAlive   = "ALIVE"
	StatusSuspect = "SUSPECT"
	StatusDead    = "DEAD"
	StatusLeft    = "LEFT"
)

// MemberEntry represents a single membership record
type MemberEntry struct {
	ID          string    `json:"id"`          // Format: <IP:Port:Timestamp>
	Heartbeat   uint64    `json:"heartbeat"`   // Heartbeat sequence counter
	Incarnation uint64    `json:"incarnation"` // Incarnation number for Suspicion mechanism
	Status      string    `json:"status"`      // ALIVE, SUSPECT, DEAD, LEFT
	LocalTime   time.Time `json:"-"`           // Local update timestamp for timer checks
}

// SuspectRecord stores a history entry of suspected nodes
type SuspectRecord struct {
	ID          string
	SuspectTime time.Time
}

// MembershipTable manages concurrent access to the membership list
type MembershipTable struct {
	mu             sync.RWMutex
	SelfID         string
	// TODO: drop these copies, the self entry already holds them and every write must update both
	Incarnation    uint64
	Heartbeat      uint64
	UseSuspicion   bool
	Members        map[string]*MemberEntry
	// FIX: record this node's own timeout suspicions here, not only gossiped ones
	SuspectHistory []SuspectRecord
}

// NewMembershipTable creates and initializes a membership table instance
func NewMembershipTable(selfID string, useSuspicion bool) *MembershipTable {
	// TODO: drop zero-value fields and fold InitSelf into this constructor
	return &MembershipTable{
		SelfID:         selfID,
		Incarnation:    0,
		Heartbeat:      1,
		UseSuspicion:   useSuspicion,
		Members:        make(map[string]*MemberEntry),
		SuspectHistory: make([]SuspectRecord, 0),
	}
}

// InitSelf registers or resets the local node in the membership list
func (t *MembershipTable) InitSelf(selfID string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.SelfID = selfID
	t.Heartbeat = 1
	t.Incarnation = 0
	t.Members[selfID] = &MemberEntry{
		ID:          selfID,
		Heartbeat:   t.Heartbeat,
		Incarnation: t.Incarnation,
		Status:      StatusAlive,
		LocalTime:   time.Now(),
	}
}

// IncrementHeartbeat advances the local heartbeat counter
func (t *MembershipTable) IncrementHeartbeat() {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.Heartbeat++
	if entry, exists := t.Members[t.SelfID]; exists {
		entry.Heartbeat = t.Heartbeat
		entry.LocalTime = time.Now()
	}
}

// MarkSelfLeft marks the local node as LEFT
func (t *MembershipTable) MarkSelfLeft() {
	t.mu.Lock()
	defer t.mu.Unlock()

	if entry, exists := t.Members[t.SelfID]; exists {
		entry.Status = StatusLeft
		entry.LocalTime = time.Now()
	}
}

// MergeMemberList reconciles incoming gossip entries with the local table
func (t *MembershipTable) MergeMemberList(incoming []MemberEntry) {
	t.mu.Lock()
	defer t.mu.Unlock()

	now := time.Now()

	for _, inc := range incoming {
		// Handle self node refutation under Gossip+S
		if inc.ID == t.SelfID {
			if t.UseSuspicion && inc.Status == StatusSuspect && inc.Incarnation >= t.Incarnation {
				t.Incarnation = inc.Incarnation + 1
				t.Members[t.SelfID].Incarnation = t.Incarnation
				t.Members[t.SelfID].Status = StatusAlive
				t.Members[t.SelfID].LocalTime = now
				LogEvent("[REFUTE] Self was suspected; refuted with incarnation %d", t.Incarnation)
			}
			// FIX: also react to being gossiped as DEAD, otherwise this node keeps gossiping under an ID the group removed
			continue
		}

		existing, exists := t.Members[inc.ID]
		// FIX: skip existing DEAD/LEFT entries here so a confirmed failure is never rescinded

		// Add new member if not previously known and not dead/left
		// FIX: consult a tombstone set so stale gossip doesn't re-add a node the sweep already deleted
		if !exists {
			if inc.Status != StatusDead && inc.Status != StatusLeft {
				t.Members[inc.ID] = &MemberEntry{
					ID:          inc.ID,
					Heartbeat:   inc.Heartbeat,
					Incarnation: inc.Incarnation,
					Status:      inc.Status,
					LocalTime:   now,
				}
				LogEvent("[MEMBERSHIP] New member added: %s (Status: %s)", inc.ID, inc.Status)
			}
			continue
		}

		// Merge logic for pure Gossip mode
		if !t.UseSuspicion {
			if inc.Heartbeat > existing.Heartbeat {
				existing.Heartbeat = inc.Heartbeat
				existing.LocalTime = now
				// FIX: resurrects DEAD/LEFT when a peer that heard from the node more recently gossips a higher heartbeat
				if existing.Status != StatusAlive && inc.Status == StatusAlive {
					existing.Status = StatusAlive
					LogEvent("[MEMBERSHIP] Member recovered to ALIVE: %s", inc.ID)
				}
			}
			if inc.Status == StatusLeft && existing.Status != StatusLeft {
				existing.Status = StatusLeft
				existing.LocalTime = now
				LogEvent("[MEMBERSHIP] Communicated leave: %s", inc.ID)
			}
			// FIX: gossiped DEAD is dropped here, so nodes in different modes disagree on failures
			continue
		}

		// Merge logic for Gossip+S mode (Incarnation prioritized over Heartbeat)
		// FIX: overwrites a confirmed DEAD with ALIVE when a late refutation arrives
		if inc.Incarnation > existing.Incarnation {
			existing.Incarnation = inc.Incarnation
			existing.Heartbeat = inc.Heartbeat
			existing.Status = inc.Status
			existing.LocalTime = now

			if inc.Status == StatusSuspect {
				t.SuspectHistory = append(t.SuspectHistory, SuspectRecord{ID: inc.ID, SuspectTime: now})
				LogEvent("[SUSPECT] Communicated suspect: %s (Incarnation %d)", inc.ID, inc.Incarnation)
			}
		} else if inc.Incarnation == existing.Incarnation {
			if existing.Status == StatusAlive && inc.Status == StatusSuspect {
				existing.Status = StatusSuspect
				existing.LocalTime = now
				t.SuspectHistory = append(t.SuspectHistory, SuspectRecord{ID: inc.ID, SuspectTime: now})
				LogEvent("[SUSPECT] Communicated suspect: %s (Incarnation %d)", inc.ID, inc.Incarnation)
			} else if inc.Heartbeat > existing.Heartbeat {
				existing.Heartbeat = inc.Heartbeat
				if existing.Status == StatusAlive {
					existing.LocalTime = now
				}
			}
		}

		if inc.Status == StatusDead && existing.Status != StatusDead {
			existing.Status = StatusDead
			existing.LocalTime = now
			LogEvent("[FAILURE] Communicated failure: %s confirmed DEAD", inc.ID)
		}

		if inc.Status == StatusLeft && existing.Status != StatusLeft {
			existing.Status = StatusLeft
			existing.LocalTime = now
			LogEvent("[MEMBERSHIP] Communicated leave: %s", inc.ID)
		}
	}
}

// GetSnapshot returns a full copy of the current membership entries
func (t *MembershipTable) GetSnapshot() []MemberEntry {
	t.mu.RLock()
	defer t.mu.RUnlock()

	// TODO: entries grow with every past failure until the sweep deletes them, inflating gossip bandwidth
	snapshot := make([]MemberEntry, 0, len(t.Members))
	for _, entry := range t.Members {
		snapshot = append(snapshot, *entry)
	}
	return snapshot
}

// GetActivePeerAddresses extracts host:port strings for peers eligible for gossip
func (t *MembershipTable) GetActivePeerAddresses() []string {
	t.mu.RLock()
	defer t.mu.RUnlock()

	var targets []string
	for id, entry := range t.Members {
		if id != t.SelfID && entry.Status != StatusDead && entry.Status != StatusLeft {
			parts := strings.Split(id, ":")
			if len(parts) >= 2 {
				targets = append(targets, fmt.Sprintf("%s:%s", parts[0], parts[1]))
			}
		}
	}
	return targets
}

// SetSuspicionMode enables or disables the suspicion mechanism
func (t *MembershipTable) SetSuspicionMode(enabled bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.UseSuspicion = enabled
}

// IsSuspicionEnabled checks if suspicion mode is active
func (t *MembershipTable) IsSuspicionEnabled() bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.UseSuspicion
}

// GetSuspectHistory returns recorded suspect events
func (t *MembershipTable) GetSuspectHistory() []SuspectRecord {
	t.mu.RLock()
	defer t.mu.RUnlock()

	history := make([]SuspectRecord, len(t.SuspectHistory))
	copy(history, t.SuspectHistory)
	return history
}
