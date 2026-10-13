// Package state persists small JSON documents (caches, history, the organize
// ledger) in STATE_DIR so restarts do not repeat work. A nil *Store keeps
// nothing, which is how persistence is disabled.
package state

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"regexp"
	"sync"
	"time"
)

// MaxDocument bounds what Load reads back. The organize ledger is the
// largest document: a full ledger must fit (see the organizer's tests).
const MaxDocument = 256 << 20

var reName = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)

// Store is a directory of JSON documents, accessed through os.Root.
type Store struct {
	root *os.Root
}

// Open opens dir, which must exist and be writable.
func Open(dir string) (*Store, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("state dir: %w", err)
	}
	s := &Store{root: root}
	probe := ".write-test-" + rand.Text()[:8]
	f, err := root.OpenFile(probe, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		root.Close()
		return nil, fmt.Errorf("state dir is not writable: %w", err)
	}
	f.Close()
	root.Remove(probe)
	return s, nil
}

// Close releases the directory.
func (s *Store) Close() error {
	if s == nil {
		return nil
	}
	return s.root.Close()
}

// Load returns the document name, or nil when it does not exist (or the
// store is disabled).
func (s *Store) Load(name string) ([]byte, error) {
	if s == nil {
		return nil, nil
	}
	if !reName.MatchString(name) {
		return nil, fmt.Errorf("invalid state document name %q", name)
	}
	f, err := s.root.Open(name + ".json")
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, MaxDocument+1))
	if err != nil {
		return nil, err
	}
	if len(data) > MaxDocument {
		return nil, fmt.Errorf("state document %s is too large", name)
	}
	return data, nil
}

// Save atomically replaces the document name with data.
func (s *Store) Save(name string, data []byte) error {
	if s == nil {
		return nil
	}
	if !reName.MatchString(name) {
		return fmt.Errorf("invalid state document name %q", name)
	}
	tmp := "." + name + "." + rand.Text()[:12] + ".tmp"
	f, err := s.root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = s.root.Rename(tmp, name+".json")
	}
	if err != nil {
		s.root.Remove(tmp)
	}
	return err
}

// Saver writes one document from export, at most once per delay after a
// Touch, and on Flush.
type Saver struct {
	store  *Store
	name   string
	delay  time.Duration
	export func() ([]byte, error)
	log    *slog.Logger

	mu    sync.Mutex
	timer *time.Timer
	dirty bool
}

// Saver returns a Saver for name. On a nil Store it does nothing.
func (s *Store) Saver(name string, delay time.Duration, export func() ([]byte, error), log *slog.Logger) *Saver {
	return &Saver{store: s, name: name, delay: delay, export: export, log: log}
}

// Touch schedules a save.
func (v *Saver) Touch() {
	if v == nil || v.store == nil {
		return
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	v.dirty = true
	if v.timer == nil {
		v.timer = time.AfterFunc(v.delay, v.Flush)
	}
}

// Flush writes the document now if it changed since the last save.
func (v *Saver) Flush() {
	if v == nil || v.store == nil {
		return
	}
	v.mu.Lock()
	if v.timer != nil {
		v.timer.Stop()
		v.timer = nil
	}
	dirty := v.dirty
	v.dirty = false
	v.mu.Unlock()
	if !dirty {
		return
	}
	data, err := v.export()
	if err == nil {
		err = v.store.Save(v.name, data)
	}
	if err != nil {
		v.log.Warn("state save failed", "document", v.name, "err", err)
	}
}
