// Package watcher observes the downloads directory and triggers organization
// once files stop changing.
package watcher

import (
	"context"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
)

// OrganizeFunc organizes a path inside the downloads directory.
type OrganizeFunc func(ctx context.Context, path string)

type pending struct {
	size      int64
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
			if err := w.fsw.Add(p); err != nil {
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

// topLevel returns the first path component under root (the "release").
func (w *Watcher) topLevel(p string) string {
	r, err := filepath.Rel(w.root, p)
	if err != nil || r == "." || strings.HasPrefix(r, "..") {
		return ""
	}
	first, _, _ := strings.Cut(r, string(filepath.Separator))
	return filepath.Join(w.root, first)
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
			w.log.Warn("watcher error", "err", err)
		case <-tick.C:
			w.flush()
		}
	}
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
// (same size, no events) for the configured duration.
func (w *Watcher) flush() {
	now := time.Now()
	ready := map[string]bool{}
	for p, e := range w.pending {
		top := w.topLevel(p)
		if top == "" {
			delete(w.pending, p)
			continue
		}
		fi, err := os.Lstat(p)
		if err != nil {
			delete(w.pending, p)
			continue
		}
		if fi.Size() != e.size {
			e.size = fi.Size()
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
			if w.topLevel(p) == top {
				delete(w.pending, p)
			}
		}
	}
}
