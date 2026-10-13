package integration

import (
	"context"
	"errors"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/jfxdev/media-maid/internal/jobs"
)

// ErrSyncDisabled is returned when no media server is configured.
var ErrSyncDisabled = errors.New("library sync disabled")

const (
	// maxTargetedPaths is the most folders refreshed one by one; beyond it a
	// full refresh is cheaper.
	maxTargetedPaths = 50
	// maxWaitFactor bounds how long a steady stream of requests can delay a
	// refresh, in debounce windows.
	maxWaitFactor = 5
)

// LibrarySyncer triggers library rescans on the configured media server.
type LibrarySyncer interface {
	// Request schedules a debounced sync of the given library folders (local
	// paths); with none, the whole library is refreshed.
	Request(paths ...string)
	// SyncNow runs a full sync immediately.
	SyncNow(ctx context.Context) error
}

// Syncer is the single, generic LibrarySyncer used for every platform.
type Syncer struct {
	client   *Client
	debounce time.Duration
	history  *jobs.History
	log      *slog.Logger
	// ToServer maps a local library path to the server's view of it
	// (SERVER_PATH_MAP); nil keeps paths unchanged.
	ToServer func(string) string

	mu      sync.Mutex
	timer   *time.Timer // at most one, armed while a batch is pending
	first   time.Time   // when the pending batch started
	due     time.Time   // when the pending batch is sent
	paths   map[string]bool
	full    bool
	stopped bool
}

// NewSyncer returns a syncer. A nil client yields a disabled syncer.
func NewSyncer(client *Client, debounce time.Duration, history *jobs.History, log *slog.Logger) *Syncer {
	return &Syncer{client: client, debounce: debounce, history: history, log: log, paths: map[string]bool{}}
}

// Request schedules a sync after the debounce window, coalescing bursts. A
// burst never delays the sync by more than maxWaitFactor windows.
func (s *Syncer) Request(paths ...string) {
	if s.client == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return
	}
	if len(paths) == 0 {
		s.full = true
	}
	for _, p := range paths {
		s.paths[p] = true
	}
	now := time.Now()
	if s.timer == nil {
		s.first, s.due = now, now.Add(s.debounce)
		s.timer = time.AfterFunc(s.debounce, s.fire)
		return
	}
	s.due = now.Add(s.debounce)
	if limit := s.first.Add(maxWaitFactor * s.debounce); s.due.After(limit) {
		s.due = limit
	}
}

// fire sends the pending batch, or re-arms itself when Request pushed the
// deadline back. Only the armed timer calls it, so batches never overlap.
func (s *Syncer) fire() {
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return
	}
	if wait := time.Until(s.due); wait > 0 {
		s.timer = time.AfterFunc(wait, s.fire)
		s.mu.Unlock()
		return
	}
	s.timer = nil
	full := s.full || len(s.paths) == 0 || len(s.paths) > maxTargetedPaths
	paths := make([]string, 0, len(s.paths))
	for p := range s.paths {
		if s.ToServer != nil {
			p = s.ToServer(p)
		}
		paths = append(paths, p)
	}
	s.paths, s.full = map[string]bool{}, false
	s.mu.Unlock()
	sort.Strings(paths)

	ctx := context.Background()
	if !full {
		err := s.client.SyncPaths(ctx, paths)
		if err == nil {
			s.record(nil, len(paths))
			return
		}
		if !errors.Is(err, ErrNoTargetedSync) {
			s.log.Warn("targeted library refresh failed, refreshing everything", "platform", s.client.spec.Name, "err", err)
		}
	}
	_ = s.SyncNow(ctx)
}

// SyncNow triggers a full sync immediately and records the outcome.
func (s *Syncer) SyncNow(ctx context.Context) error {
	if s.client == nil {
		return ErrSyncDisabled
	}
	err := s.client.Sync(ctx)
	s.record(err, 0)
	return err
}

func (s *Syncer) record(err error, paths int) {
	st := jobs.SyncStatus{Status: jobs.StatusOK}
	if err != nil {
		st.Status = jobs.StatusError
		st.Error = err.Error()
		s.log.Warn("library sync failed", "platform", s.client.spec.Name, "err", err)
	} else if paths > 0 {
		s.log.Info("library refresh requested", "platform", s.client.spec.Name, "folders", paths)
	} else {
		s.log.Info("library sync requested", "platform", s.client.spec.Name)
	}
	s.history.SetSync(st)
}

// Stop cancels any pending debounced sync.
func (s *Syncer) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopped = true
	if s.timer != nil {
		s.timer.Stop()
	}
}
