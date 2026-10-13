package organizer

import (
	"encoding/json"
	"os"
	"strconv"
	"strings"
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
	Quality int `json:"quality,omitempty"`
	// DestIno is the inode of the library file when Status is "copied": a
	// copy is only recognized while it is still that file. Zero in entries
	// recorded before it existed.
	DestIno uint64    `json:"dest_ino,omitempty"`
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
	// byAlt maps an entry's device-independent identity (inode, size and
	// mtime, see altKey) to its key: the device number of a filesystem can
	// change across reboots or remounts (btrfs subvolumes, mergerfs, ZFS),
	// and the entry must still be found then.
	byAlt map[string]string
	// OnChange, when set, is called (without locks held) after an update.
	OnChange func()
}

// NewLedger returns an empty ledger.
func NewLedger() *Ledger {
	return &Ledger{m: map[string]LedgerEntry{}, byDest: map[string]string{}, byAlt: map[string]string{}}
}

// altKey is the device-independent identity of the entry e stored under key.
func altKey(key string, e LedgerEntry) string {
	_, ino, _ := strings.Cut(key, ":")
	return ino + ":" + strconv.FormatInt(e.Size, 10) + ":" + strconv.FormatInt(e.MTime.UnixNano(), 10)
}

// fileAltKey is altKey for a file on disk.
func fileAltKey(fi os.FileInfo) string {
	return strconv.FormatUint(Inode(fi), 10) + ":" + strconv.FormatInt(fi.Size(), 10) + ":" + strconv.FormatInt(fi.ModTime().UnixNano(), 10)
}

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
	l.byAlt[altKey(key, e)] = key
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
	if ak := altKey(key, e); l.byAlt[ak] == key {
		delete(l.byAlt, ak)
	}
}

// rekeyLocked moves the entry stored under old to key (the same file seen
// under a new device number). l.mu must be held.
func (l *Ledger) rekeyLocked(old, key string) {
	e := l.m[old]
	l.deleteLocked(old)
	if prev, ok := l.m[key]; ok {
		l.dropDestLocked(key, prev)
	}
	l.setLocked(key, e)
}

// fileKey identifies a file by device and inode.
func fileKey(fi os.FileInfo) (string, bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return "", false
	}
	return strconv.FormatUint(uint64(st.Dev), 10) + ":" + strconv.FormatUint(st.Ino, 10), true
}

// Inode returns the inode number of fi (0 when unknown).
func Inode(fi os.FileInfo) uint64 {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return st.Ino
	}
	return 0
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
	moved := false
	if !ok {
		// Same inode, size and mtime under another device number: the
		// filesystem was remounted. The entry follows the file.
		if old, found := l.byAlt[fileAltKey(fi)]; found && old != key {
			l.rekeyLocked(old, key)
			e, ok, moved = l.m[key], true, true
		}
	}
	l.mu.Unlock()
	if moved {
		l.changed()
	}
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
		old.Quality == e.Quality && old.ResolvedBy == e.ResolvedBy && old.DestIno == e.DestIno {
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

// Prune keeps the entries of files (the downloads folder as listed at since),
// following any file whose device number changed, and the entries recorded
// at or after since (placed while the folder was being listed). Every other
// entry is dropped: its file left the downloads folder.
func (l *Ledger) Prune(files []os.FileInfo, since time.Time) {
	if l == nil {
		return
	}
	keys := make(map[string]bool, len(files))
	alts := make(map[string]string, len(files))
	for _, fi := range files {
		if k, ok := fileKey(fi); ok {
			keys[k] = true
			alts[fileAltKey(fi)] = k
		}
	}
	n := 0
	l.mu.Lock()
	for k, e := range l.m {
		switch nk, moved := alts[altKey(k, e)]; {
		case keys[k], !e.At.Before(since):
		case moved && nk != k:
			l.rekeyLocked(k, nk)
			n++
		default:
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
	byDest, byAlt := map[string]string{}, map[string]string{}
	for k, e := range m {
		if cur, ok := byDest[destKey(e.Lib, e.Dest)]; !ok || m[cur].At.Before(e.At) {
			byDest[destKey(e.Lib, e.Dest)] = k
		}
		byAlt[altKey(k, e)] = k
	}
	l.mu.Lock()
	l.m, l.byDest, l.byAlt = m, byDest, byAlt
	l.mu.Unlock()
	return nil
}
