// Package jobs keeps an in-memory record of recent actions taken by Media Maid.
package jobs

import (
	"encoding/json"
	"sync"
	"time"
)

// Status values used across history entries.
const (
	StatusLinked   = "linked"
	StatusCopied   = "copied"
	StatusExists   = "exists"
	StatusConflict = "conflict"
	StatusReplaced = "replaced"
	// StatusUpgraded is an automatic replacement of a lower-quality release
	// already in the library (AUTO_UPGRADE).
	StatusUpgraded  = "upgraded"
	StatusDuplicate = "duplicate"
	StatusPlanned   = "planned"
	StatusSkipped   = "skipped"
	StatusError     = "error"
	StatusOK        = "ok"
	StatusRemoved   = "removed"
	StatusStale     = "stale"
	StatusDryRun    = "dry_run"
	StatusDisabled  = "disabled"
	ActionOrganize  = "organize"
	ActionSync      = "sync"
	ActionOrphanRm  = "orphan_remove"
	defaultCapacity = 500
)

// Entry is a single recorded action.
type Entry struct {
	Time   time.Time `json:"time"`
	Action string    `json:"action"`
	Source string    `json:"source,omitempty"`
	Dest   string    `json:"dest,omitempty"`
	Status string    `json:"status"`
	Error  string    `json:"error,omitempty"`
}

// SyncStatus describes the outcome of the most recent library sync.
type SyncStatus struct {
	Time   time.Time `json:"time"`
	Status string    `json:"status"`
	Error  string    `json:"error,omitempty"`
}

// History is a fixed-size ring buffer of entries, safe for concurrent use.
type History struct {
	mu       sync.Mutex
	entries  []Entry
	next     int
	full     bool
	lastSync *SyncStatus
	// OnChange, when set, is called (without locks held) after each change.
	OnChange func()
}

// NewHistory returns a history holding at most capacity entries.
func NewHistory(capacity int) *History {
	if capacity <= 0 {
		capacity = defaultCapacity
	}
	return &History{entries: make([]Entry, capacity)}
}

// Add records an entry, stamping the time if unset.
func (h *History) Add(e Entry) {
	if e.Time.IsZero() {
		e.Time = time.Now().UTC()
	}
	h.mu.Lock()
	h.entries[h.next] = e
	h.next = (h.next + 1) % len(h.entries)
	if h.next == 0 {
		h.full = true
	}
	h.mu.Unlock()
	if h.OnChange != nil {
		h.OnChange()
	}
}

// SetSync records the outcome of a library sync.
func (h *History) SetSync(s SyncStatus) {
	if s.Time.IsZero() {
		s.Time = time.Now().UTC()
	}
	h.mu.Lock()
	h.lastSync = &s
	h.mu.Unlock()
	h.Add(Entry{Time: s.Time, Action: ActionSync, Status: s.Status, Error: s.Error})
}

// Snapshot returns entries newest first plus the last sync status (may be nil).
func (h *History) Snapshot() ([]Entry, *SyncStatus) {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := h.next
	if h.full {
		n = len(h.entries)
	}
	out := make([]Entry, 0, n)
	for i := 1; i <= n; i++ {
		idx := (h.next - i + len(h.entries)) % len(h.entries)
		out = append(out, h.entries[idx])
	}
	var last *SyncStatus
	if h.lastSync != nil {
		s := *h.lastSync
		last = &s
	}
	return out, last
}

type saved struct {
	Entries  []Entry     `json:"entries"` // newest first
	LastSync *SyncStatus `json:"last_sync,omitempty"`
}

// Export returns the history as JSON.
func (h *History) Export() ([]byte, error) {
	entries, last := h.Snapshot()
	return json.Marshal(saved{Entries: entries, LastSync: last})
}

// Import restores a history saved by Export, keeping the newest entries that
// fit. Call it before the history is used.
func (h *History) Import(data []byte) error {
	var s saved
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	if len(s.Entries) > len(h.entries) {
		s.Entries = s.Entries[:len(h.entries)]
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.next, h.full = 0, false
	for i := len(s.Entries) - 1; i >= 0; i-- {
		h.entries[h.next] = s.Entries[i]
		h.next = (h.next + 1) % len(h.entries)
		if h.next == 0 {
			h.full = true
		}
	}
	h.lastSync = s.LastSync
	return nil
}
