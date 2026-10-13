package organizer

import (
	"encoding/json"
	"sort"
	"sync"
	"time"

	"github.com/jfxdev/media-maid/internal/jobs"
)

// maxPendingItems bounds the pending list; the oldest items go first.
const maxPendingItems = 5000

// PendingItem is a download that is not in the library and needs a decision:
// a conflict, a duplicate, an unidentified title or an error.
type PendingItem struct {
	Source string `json:"source"`
	Dest   string `json:"dest,omitempty"`
	Kind   string `json:"kind,omitempty"`
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
	// Since is when the item first needed attention, Updated when its
	// status or error last changed.
	Since   time.Time `json:"since"`
	Updated time.Time `json:"updated"`
}

// Pending tracks the downloads that need attention, by source path. A later
// successful placement removes an item, and full scans drop items whose file
// is gone. It is safe for concurrent use.
type Pending struct {
	mu sync.Mutex
	m  map[string]PendingItem
	// OnChange, when set, is called (without locks held) after an update.
	OnChange func()
}

// NewPending returns an empty pending list.
func NewPending() *Pending { return &Pending{m: map[string]PendingItem{}} }

// attention reports whether a result needs a decision from the user.
func attention(r Result) bool {
	if r.Kind == "subtitle" || r.Kind == KindExtra || leftAlone(r) {
		return false
	}
	switch r.Status {
	case jobs.StatusConflict, jobs.StatusDuplicate, jobs.StatusSkipped, jobs.StatusError:
		return true
	}
	return false
}

// placed reports whether a result leaves the source in the library.
func placed(status string) bool {
	switch status {
	case jobs.StatusLinked, jobs.StatusCopied, jobs.StatusReplaced, jobs.StatusUpgraded, jobs.StatusExists:
		return true
	}
	return false
}

// Update records r. It reports whether the item is new or its status or
// error changed, which is when the user should hear about it again.
func (p *Pending) Update(r Result) bool {
	if p == nil {
		return attention(r)
	}
	now := time.Now().UTC()
	changed, dirty := false, false
	p.mu.Lock()
	old, had := p.m[r.Source]
	switch {
	case attention(r):
		changed = !had || old.Status != r.Status || old.Error != r.Error || old.Dest != r.Dest
		if !changed {
			break
		}
		it := PendingItem{Source: r.Source, Dest: r.Dest, Kind: r.Kind, Status: r.Status, Error: r.Error, Since: now, Updated: now}
		if had {
			it.Since = old.Since
		}
		if !had && len(p.m) >= maxPendingItems {
			oldest, at := "", now
			for k, v := range p.m {
				if v.Updated.Before(at) {
					oldest, at = k, v.Updated
				}
			}
			delete(p.m, oldest)
		}
		p.m[r.Source] = it
		dirty = true
	case had && (placed(r.Status) || leftAlone(r)):
		delete(p.m, r.Source)
		dirty = true
	}
	p.mu.Unlock()
	if dirty && p.OnChange != nil {
		p.OnChange()
	}
	return changed
}

// Prune keeps only the items whose source is in keep.
func (p *Pending) Prune(keep map[string]bool) {
	if p == nil {
		return
	}
	n := 0
	p.mu.Lock()
	for k := range p.m {
		if !keep[k] {
			delete(p.m, k)
			n++
		}
	}
	p.mu.Unlock()
	if n > 0 && p.OnChange != nil {
		p.OnChange()
	}
}

// List returns the items, oldest first.
func (p *Pending) List() []PendingItem {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	out := make([]PendingItem, 0, len(p.m))
	for _, it := range p.m {
		out = append(out, it)
	}
	p.mu.Unlock()
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Since.Equal(out[j].Since) {
			return out[i].Since.Before(out[j].Since)
		}
		return out[i].Source < out[j].Source
	})
	return out
}

// Export returns the list as JSON.
func (p *Pending) Export() ([]byte, error) {
	return json.Marshal(p.List())
}

// Import loads a list saved by Export, replacing the current items.
func (p *Pending) Import(data []byte) error {
	var items []PendingItem
	if err := json.Unmarshal(data, &items); err != nil {
		return err
	}
	m := make(map[string]PendingItem, len(items))
	for _, it := range items {
		if it.Source != "" {
			m[it.Source] = it
		}
	}
	p.mu.Lock()
	p.m = m
	p.mu.Unlock()
	return nil
}
