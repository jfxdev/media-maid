// Package watcher observes the downloads directory and triggers organization
// once files stop changing.
package watcher

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	"github.com/fsnotify/fsnotify"
)

// OrganizeFunc organizes a path inside the downloads directory.
type OrganizeFunc func(ctx context.Context, path string)

type pending struct {
	size      int64
	mtime     time.Time
	lastEvent time.Time
	stableAt  time.Time
}

// Watcher recursively watches root.
type Watcher struct {
	root     string
	stable   time.Duration
	organize OrganizeFunc
	log      *slog.Logger
	fsw      *fsnotify.Watcher
	pending  map[string]*pending
	// queued holds releases handed to the worker and not yet finished. It is
	// owned by the event loop, like pending.
	queued map[string]bool
	work   chan string
	done   chan string

	// running and lastLoop back Healthy: set while Run is active, and the
	// last time its loop went round (unix nanoseconds).
	running  atomic.Bool
	lastLoop atomic.Int64
	// watchFailures counts folders that could not be watched (see Degraded).
	watchFailures atomic.Int64
}

// addWatch is fsnotify's Add; tests replace it to simulate a watch limit.
var addWatch = func(w *fsnotify.Watcher, dir string) error { return w.Add(dir) }

// Degraded reports whether some folder could not be watched (typically the
// inotify watch limit, fs.inotify.max_user_watches): new files there raise
// no event, so only a periodic scan finds them. It stays set once seen.
func (w *Watcher) Degraded() bool { return w.watchFailures.Load() > 0 }

// stallLimit is how long the event loop may go without a tick before the
// watcher reports itself unhealthy. The loop never blocks on organizing, so
// only a stuck filesystem call can hold it that long.
const stallLimit = 30 * time.Second

// Healthy reports whether Run is active and its event loop is not stalled.
func (w *Watcher) Healthy() bool {
	return w.running.Load() && time.Since(time.Unix(0, w.lastLoop.Load())) < stallLimit
}

const workQueueSize = 256

// New creates a watcher; call Run to start it.
func New(root string, stable time.Duration, organize OrganizeFunc, log *slog.Logger) (*Watcher, error) {
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	w := &Watcher{
		root: root, stable: stable, organize: organize, log: log, fsw: fsw,
		pending: map[string]*pending{},
		queued:  map[string]bool{},
		work:    make(chan string, workQueueSize),
		done:    make(chan string, workQueueSize),
	}
	if err := w.addTree(root); err != nil {
		fsw.Close()
		return nil, err
	}
	return w, nil
}

// addTree watches dir and its subdirectories (symlinks are not followed).
func (w *Watcher) addTree(dir string) error {
	return filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if err := addWatch(w.fsw, p); err != nil {
				w.watchFailures.Add(1)
				w.log.Warn("watch failed", "dir", w.rel(p), "err", err)
			}
		} else if d.Type().IsRegular() || d.Type()&fs.ModeSymlink != 0 {
			w.touch(p)
		}
		return nil
	})
}

func (w *Watcher) rel(p string) string {
	r, err := filepath.Rel(w.root, p)
	if err != nil {
		return filepath.Base(p)
	}
	return r
}

func (w *Watcher) touch(p string) {
	now := time.Now()
	if e, ok := w.pending[p]; ok {
		e.lastEvent = now
		return
	}
	w.pending[p] = &pending{size: -1, lastEvent: now}
}

// supportDir matches folders that belong to the release around them: seasons,
// subtitles, samples and extras (including the Jellyfin and Plex extras
// folders; keep in sync with organizer's junkDirs).
var supportDir = regexp.MustCompile(`(?i)^(?:season[ ._-]?\d{1,4}|s\d{1,2}|temporada[ ._-]?\d{1,2}|specials|subs?|subtitles|legendas|samples?|extras|featurettes|trailers|behind[ ._-]the[ ._-]scenes|deleted[ ._-]scenes|interviews|scenes|shorts|clips|bonus)$`)

// unitOf returns the path organized for p: its release folder (the folder
// holding it, above any season, subtitle or extras folder), or p itself when
// that folder is the downloads root or a container of other releases, such as
// a torrent client category ("downloads/movies/<release>"). A release is
// therefore never held back by unrelated downloads that share a category.
func (w *Watcher) unitOf(p string, containers map[string]bool) string {
	r, err := filepath.Rel(w.root, p)
	if err != nil || r == "." || strings.HasPrefix(r, "..") {
		return ""
	}
	dir := filepath.Dir(p)
	for dir != w.root && supportDir.MatchString(filepath.Base(dir)) {
		dir = filepath.Dir(dir)
	}
	if dir == w.root || w.isContainer(dir, containers) {
		return p
	}
	return dir
}

// isContainer reports whether dir holds other release folders (any
// subfolder that is not a support folder). Results are memoized per flush.
func (w *Watcher) isContainer(dir string, memo map[string]bool) bool {
	if v, ok := memo[dir]; ok {
		return v
	}
	entries, _ := os.ReadDir(dir)
	v := false
	for _, e := range entries {
		if e.IsDir() && !supportDir.MatchString(e.Name()) && !strings.HasPrefix(e.Name(), ".") {
			v = true
			break
		}
	}
	memo[dir] = v
	return v
}

// worker organizes releases one at a time, off the event loop, so long copies
// never stop the loop from draining filesystem events.
func (w *Watcher) worker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case top := <-w.work:
			w.organize(ctx, top)
			select {
			case w.done <- top:
			case <-ctx.Done():
				return
			}
		}
	}
}

// Run processes events until ctx is cancelled.
func (w *Watcher) Run(ctx context.Context) {
	w.lastLoop.Store(time.Now().UnixNano())
	w.running.Store(true)
	defer w.running.Store(false)
	defer w.fsw.Close()
	go w.worker(ctx)
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case top := <-w.done:
			delete(w.queued, top)
		case ev, ok := <-w.fsw.Events:
			if !ok {
				return
			}
			w.handle(ev)
		case err, ok := <-w.fsw.Errors:
			if !ok {
				return
			}
			w.handleError(err)
		case <-tick.C:
			w.lastLoop.Store(time.Now().UnixNano())
			w.flush()
		}
	}
}

// handleError logs a watcher error. When the kernel queue overflowed, events
// were lost: the whole tree is walked again so every file is reconsidered
// (files already placed are recognized by the organizer and left as they are).
func (w *Watcher) handleError(err error) {
	if errors.Is(err, fsnotify.ErrEventOverflow) {
		w.log.Warn("filesystem events were lost, rescanning the downloads folder", "err", err)
		_ = w.addTree(w.root)
		return
	}
	w.log.Warn("watcher error", "err", err)
}

func (w *Watcher) handle(ev fsnotify.Event) {
	if ev.Has(fsnotify.Remove) || ev.Has(fsnotify.Rename) {
		delete(w.pending, ev.Name)
		return
	}
	fi, err := os.Lstat(ev.Name)
	if err != nil {
		return
	}
	if fi.IsDir() {
		if ev.Has(fsnotify.Create) {
			_ = w.addTree(ev.Name)
		}
		return
	}
	w.touch(ev.Name)
}

// flush organizes every release whose pending files have all been stable
// (same size and modification time, no events) for the configured duration.
func (w *Watcher) flush() {
	now := time.Now()
	ready := map[string]bool{}
	units := map[string]string{}
	containers := map[string]bool{}
	for p, e := range w.pending {
		top := w.unitOf(p, containers)
		units[p] = top
		if top == "" {
			delete(w.pending, p)
			continue
		}
		fi, err := os.Lstat(p)
		if err != nil {
			delete(w.pending, p)
			continue
		}
		// The modification time counts too: a file preallocated at its full
		// size and written through a memory map (libtorrent 2.x) raises no
		// change events and keeps its size, but the kernel still moves its
		// mtime as pages are written.
		if fi.Size() != e.size || !fi.ModTime().Equal(e.mtime) {
			e.size, e.mtime = fi.Size(), fi.ModTime()
			e.stableAt = now
		}
		isReady := now.Sub(e.stableAt) >= w.stable && now.Sub(e.lastEvent) >= w.stable
		if v, seen := ready[top]; !seen || v {
			ready[top] = isReady
		}
	}
	for top, ok := range ready {
		// A release still being organized keeps its pending files; it is
		// dispatched again once the worker reports it done.
		if !ok || w.queued[top] {
			continue
		}
		select {
		case w.work <- top:
		default:
			continue // queue full: retry on the next tick
		}
		w.queued[top] = true
		for p := range w.pending {
			if units[p] == top {
				delete(w.pending, p)
			}
		}
	}
}
