package organizer

import (
	"encoding/json"
	"os"
	"strconv"
	"sync"
	"syscall"
	"time"
)

// maxLedgerEntries bounds the ledger; the oldest entries go first.
const maxLedgerEntries = 200000

// LedgerEntry records where a downloaded video was placed.
type LedgerEntry struct {
	Size  int64     `json:"size"`
	MTime time.Time `json:"mtime"`
	// Lib is "movies" or "tv"; Dest is relative to that library.
	Lib         string `json:"lib"`
	Dest        string `json:"dest"`
	Status      string `json:"status"`
	Kind        string `json:"kind,omitempty"`
	ResolvedBy  string `json:"resolved_by,omitempty"`
	TitleSource string `json:"title_source,omitempty"`
	// Quality is the parser.Quality rank of the release (0 when unknown); it
	// decides automatic upgrades.
	Quality int       `json:"quality,omitempty"`
	At      time.Time `json:"at"`
}

// Ledger maps source files (by device and inode, so renames inside the
// downloads folder keep their entry) to where they were placed. Automatic runs
// use it to skip identification of files already organized, and to leave alone
// files whose library copy was deleted on purpose. It is safe for concurrent
// use.
type Ledger struct {
	mu sync.Mutex
	m  map[string]LedgerEntry
	// byDest maps "lib/dest" to the key of the entry placed there (the most
	// recent one), so a library file can be traced back to its source.
	byDest map[string]string
	// OnChange, when set, is called (without locks held) after an update.
	OnChange func()
}

// NewLedger returns an empty ledger.
func NewLedger() *Ledger { return &Ledger{m: map[string]LedgerEntry{}, byDest: map[string]string{}} }

func destKey(lib, dest string) string { return lib + "/" + dest }

// ByDest returns the entry placed at dest in lib, if any. The caller decides
// whether the library file is still that placement.
func (l *Ledger) ByDest(lib, dest string) (LedgerEntry, bool) {
	if l == nil {
		return LedgerEntry{}, false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	k, ok := l.byDest[destKey(lib, dest)]
	if !ok {
		return LedgerEntry{}, false
	}
	e, ok := l.m[k]
	return e, ok && e.Lib == lib && e.Dest == dest
}

// setLocked stores e under key and keeps byDest in step. l.mu must be held.
func (l *Ledger) setLocked(key string, e LedgerEntry) {
	if old, ok := l.m[key]; ok {
		l.dropDestLocked(key, old)
	}
	l.m[key] = e
	l.byDest[destKey(e.Lib, e.Dest)] = key
}

func (l *Ledger) deleteLocked(key string) {
	if old, ok := l.m[key]; ok {
		l.dropDestLocked(key, old)
		delete(l.m, key)
	}
}

func (l *Ledger) dropDestLocked(key string, e LedgerEntry) {
	if dk := destKey(e.Lib, e.Dest); l.byDest[dk] == key {
		delete(l.byDest, dk)
	}
}

// fileKey identifies a file by device and inode.
func fileKey(fi os.FileInfo) (string, bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return "", false
	}
	return strconv.FormatUint(uint64(st.Dev), 10) + ":" + strconv.FormatUint(st.Ino, 10), true
}

// Lookup returns the entry for fi when it still describes the same content.
func (l *Ledger) Lookup(fi os.FileInfo) (LedgerEntry, bool) {
	if l == nil {
		return LedgerEntry{}, false
	}
	key, ok := fileKey(fi)
	if !ok {
		return LedgerEntry{}, false
	}
	l.mu.Lock()
	e, ok := l.m[key]
	l.mu.Unlock()
	if !ok || e.Size != fi.Size() || !e.MTime.Equal(fi.ModTime()) {
		return LedgerEntry{}, false
	}
	return e, true
}

// Put records fi as placed at e.
func (l *Ledger) Put(fi os.FileInfo, e LedgerEntry) {
	if l == nil {
		return
	}
	key, ok := fileKey(fi)
	if !ok {
		return
	}
	e.Size, e.MTime = fi.Size(), fi.ModTime()
	l.mu.Lock()
	if old, ok := l.m[key]; ok && old.Lib == e.Lib && old.Dest == e.Dest && old.Size == e.Size && old.MTime.Equal(e.MTime) &&
		old.Quality == e.Quality && old.ResolvedBy == e.ResolvedBy {
		l.mu.Unlock()
		return // nothing new
	}
	e.At = time.Now()
	if len(l.m) >= maxLedgerEntries {
		oldest, at := "", e.At
		for k, v := range l.m {
			if v.At.Before(at) {
				oldest, at = k, v.At
			}
		}
		l.deleteLocked(oldest)
	}
	l.setLocked(key, e)
	l.mu.Unlock()
	l.changed()
}

// Prune keeps only the entries whose key is in keep.
func (l *Ledger) Prune(keep map[string]bool) {
	if l == nil {
		return
	}
	n := 0
	l.mu.Lock()
	for k := range l.m {
		if !keep[k] {
			l.deleteLocked(k)
			n++
		}
	}
	l.mu.Unlock()
	if n > 0 {
		l.changed()
	}
}

func (l *Ledger) changed() {
	if l.OnChange != nil {
		l.OnChange()
	}
}

// Export returns the ledger as JSON.
func (l *Ledger) Export() ([]byte, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return json.Marshal(l.m)
}

// Import loads a ledger saved by Export, replacing the current entries.
func (l *Ledger) Import(data []byte) error {
	m := map[string]LedgerEntry{}
	if err := json.Unmarshal(data, &m); err != nil {
		return err
	}
	byDest := map[string]string{}
	for k, e := range m {
		if cur, ok := byDest[destKey(e.Lib, e.Dest)]; !ok || m[cur].At.Before(e.At) {
			byDest[destKey(e.Lib, e.Dest)] = k
		}
	}
	l.mu.Lock()
	l.m, l.byDest = m, byDest
	l.mu.Unlock()
	return nil
}
