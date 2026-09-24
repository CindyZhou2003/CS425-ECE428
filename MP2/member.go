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
	ID          string    `json:"id"`
	Heartbeat   uint64    `json:"heartbeat"`
	Incarnation uint64    `json:"incarnation"`
	Status      string    `json:"status"`
	LocalTime   time.Time `json:"-"`
}

// SuspectRecord stores a history entry of suspected nodes
type SuspectRecord struct {
	ID          string
	SuspectTime time.Time
}

// MembershipTable manages concurrent access to the membership list
type MembershipTable struct {
	mu sync.RWMutex

	SelfID       string
	UseSuspicion bool
	Members      map[string]*MemberEntry

	// Record our own suspicion events, including locally generated ones.
	SuspectHistory []SuspectRecord
}

// NewMembershipTable creates and initializes a membership table instance
func NewMembershipTable(selfID string, useSuspicion bool) *MembershipTable {
	return &MembershipTable{
		SelfID:         selfID,
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

	t.Members[selfID] = &MemberEntry{
		ID:          selfID,
		Heartbeat:   1,
		Incarnation: 0,
		Status:      StatusAlive,
		LocalTime:   time.Now(),
	}
}

// IncrementHeartbeat advances the local heartbeat counter
func (t *MembershipTable) IncrementHeartbeat() {
	t.mu.Lock()
	defer t.mu.Unlock()

	entry, exists := t.Members[t.SelfID]
	if !exists {
		return
	}

	entry.Heartbeat++
	entry.LocalTime = time.Now()
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

		// 1. Handle gossip about ourselves
		if inc.ID == t.SelfID {
			self, exists := t.Members[t.SelfID]
			if !exists {
				continue
			}

			// Refute SUSPECT or DEAD claims about ourselves.
			if (inc.Status == StatusSuspect || inc.Status == StatusDead) &&
				inc.Incarnation >= self.Incarnation {

				self.Incarnation = inc.Incarnation + 1
				self.Status = StatusAlive
				self.LocalTime = now

				if inc.Status == StatusSuspect {
					LogEvent(
						"[REFUTE] Self was suspected; refuted with incarnation %d",
						self.Incarnation,
					)
				} else {
					LogEvent(
						"[REFUTE] Self was marked DEAD; refuted with incarnation %d",
						self.Incarnation,
					)
				}
			}

			// Never merge another node's heartbeat into our own.
			continue
		}

		// 2. Find existing member
		existing, exists := t.Members[inc.ID]

		// 3. New member
		if !exists {

			// Do not add a node that arrives already DEAD or LEFT.
			if inc.Status == StatusDead || inc.Status == StatusLeft {
				continue
			}

			t.Members[inc.ID] = &MemberEntry{
				ID:          inc.ID,
				Heartbeat:   inc.Heartbeat,
				Incarnation: inc.Incarnation,
				Status:      inc.Status,
				LocalTime:   now,
			}

			if inc.Status == StatusSuspect {
				t.SuspectHistory = append(
					t.SuspectHistory,
					SuspectRecord{
						ID:          inc.ID,
						SuspectTime: now,
					},
				)

				LogEvent(
					"[SUSPECT] New member arrived as SUSPECT: %s (Incarnation %d)",
					inc.ID,
					inc.Incarnation,
				)
			} else {
				LogEvent(
					"[MEMBERSHIP] New member added: %s (Status: %s)",
					inc.ID,
					inc.Status,
				)
			}

			continue
		}
		// 4. DEAD / LEFT are terminal local states

		if existing.Status == StatusDead ||
			existing.Status == StatusLeft {
			continue
		}

		// 5. Handle DEAD / LEFT
		//
		// DEAD and LEFT use the same merge logic.
		// The actual status is preserved through inc.Status.
		if inc.Status == StatusDead || inc.Status == StatusLeft {

			if !t.UseSuspicion {
				// Pure Gossip: heartbeat determines freshness.
				// Ignores a claim older than what we hold, or one false positive re-kills a re-added member
				if inc.Heartbeat < existing.Heartbeat {
					continue
				}
				existing.Status = inc.Status
				existing.LocalTime = now

				if inc.Heartbeat > existing.Heartbeat {
					existing.Heartbeat = inc.Heartbeat
				}

				if inc.Status == StatusDead {
					LogEvent(
						"[FAILURE] Communicated failure: %s confirmed DEAD",
						inc.ID,
					)
				} else {
					LogEvent(
						"[MEMBERSHIP] Communicated leave: %s",
						inc.ID,
					)
				}

				continue
			}

			// Gossip+S: incarnation determines freshness.
			if inc.Incarnation > existing.Incarnation {
				existing.Incarnation = inc.Incarnation
				existing.Heartbeat = inc.Heartbeat
				existing.Status = inc.Status
				existing.LocalTime = now

				if inc.Status == StatusDead {
					LogEvent(
						"[FAILURE] Communicated failure: %s confirmed DEAD",
						inc.ID,
					)
				} else {
					LogEvent(
						"[MEMBERSHIP] Communicated leave: %s",
						inc.ID,
					)
				}

				continue
			}

			if inc.Incarnation == existing.Incarnation {
				existing.Status = inc.Status

				if inc.Heartbeat > existing.Heartbeat {
					existing.Heartbeat = inc.Heartbeat
				}

				existing.LocalTime = now

				if inc.Status == StatusDead {
					LogEvent(
						"[FAILURE] Communicated failure: %s confirmed DEAD",
						inc.ID,
					)
				} else {
					LogEvent(
						"[MEMBERSHIP] Communicated leave: %s",
						inc.ID,
					)
				}

				continue
			}

			// Older incarnation: ignore.
			continue
		}

		// 6. Pure Gossip mode

		if !t.UseSuspicion {

			// Heartbeat determines freshness.
			if inc.Heartbeat > existing.Heartbeat {
				existing.Heartbeat = inc.Heartbeat
				existing.LocalTime = now

				if inc.Status == StatusAlive {
					existing.Status = StatusAlive
				}
			}

			continue
		}

		// 7. Gossip+S mode
		// Incarnation is more important than heartbeat.
		if inc.Incarnation > existing.Incarnation {

			existing.Incarnation = inc.Incarnation
			existing.Heartbeat = inc.Heartbeat
			existing.Status = inc.Status
			existing.LocalTime = now

			if inc.Status == StatusSuspect {
				t.SuspectHistory = append(
					t.SuspectHistory,
					SuspectRecord{
						ID:          inc.ID,
						SuspectTime: now,
					},
				)

				LogEvent(
					"[SUSPECT] Communicated suspect: %s (Incarnation %d)",
					inc.ID,
					inc.Incarnation,
				)
			}

			continue
		}

		// 8. Same incarnation
		if inc.Incarnation == existing.Incarnation {

			// ALIVE -> SUSPECT is a valid transition.
			if existing.Status == StatusAlive &&
				inc.Status == StatusSuspect {

				existing.Status = StatusSuspect
				existing.LocalTime = now

				t.SuspectHistory = append(
					t.SuspectHistory,
					SuspectRecord{
						ID:          inc.ID,
						SuspectTime: now,
					},
				)

				LogEvent(
					"[SUSPECT] Communicated suspect: %s (Incarnation %d)",
					inc.ID,
					inc.Incarnation,
				)

				continue
			}

			// A newer heartbeat updates an ALIVE member.
			if inc.Heartbeat > existing.Heartbeat {
				existing.Heartbeat = inc.Heartbeat

				// Do not automatically turn SUSPECT back into ALIVE.
				if existing.Status == StatusAlive &&
					inc.Status == StatusAlive {
					existing.LocalTime = now
				}
			}
		}
	}
}

// GetSnapshot returns a full copy of the current membership entries
func (t *MembershipTable) GetSnapshot() []MemberEntry {
	t.mu.RLock()
	defer t.mu.RUnlock()

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
		if id != t.SelfID &&
			entry.Status != StatusDead &&
			entry.Status != StatusLeft {

			parts := strings.Split(id, ":")

			if len(parts) >= 2 {
				targets = append(
					targets,
					fmt.Sprintf("%s:%s", parts[0], parts[1]),
				)
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
