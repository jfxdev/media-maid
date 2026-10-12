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
}

// New creates a watcher; call Run to start it.
func New(root string, stable time.Duration, organize OrganizeFunc, log *slog.Logger) (*Watcher, error) {
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	w := &Watcher{root: root, stable: stable, organize: organize, log: log, fsw: fsw, pending: map[string]*pending{}}
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

// Run processes events until ctx is cancelled.
func (w *Watcher) Run(ctx context.Context) {
	defer w.fsw.Close()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
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
			w.flush(ctx)
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
func (w *Watcher) flush(ctx context.Context) {
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
		if !ok {
			continue
		}
		for p := range w.pending {
			if w.topLevel(p) == top {
				delete(w.pending, p)
			}
		}
		w.organize(ctx, top)
	}
}
