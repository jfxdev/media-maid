package integration

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/jfxdev/media-maid/internal/jobs"
)

// ErrSyncDisabled is returned when no media server is configured.
var ErrSyncDisabled = errors.New("library sync disabled")

// LibrarySyncer triggers library rescans on the configured media server.
type LibrarySyncer interface {
	// Request schedules a debounced sync.
	Request()
	// SyncNow runs a sync immediately.
	SyncNow(ctx context.Context) error
}

// Syncer is the single, generic LibrarySyncer used for every platform.
type Syncer struct {
	client   *Client
	debounce time.Duration
	history  *jobs.History
	log      *slog.Logger

	mu    sync.Mutex
	timer *time.Timer
}

// NewSyncer returns a syncer. A nil client yields a disabled syncer.
func NewSyncer(client *Client, debounce time.Duration, history *jobs.History, log *slog.Logger) *Syncer {
	return &Syncer{client: client, debounce: debounce, history: history, log: log}
}

// Request schedules a sync after the debounce window, coalescing bursts.
func (s *Syncer) Request() {
	if s.client == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.timer != nil {
		s.timer.Stop()
	}
	s.timer = time.AfterFunc(s.debounce, func() {
		_ = s.SyncNow(context.Background())
	})
}

// SyncNow triggers the sync immediately and records the outcome.
func (s *Syncer) SyncNow(ctx context.Context) error {
	if s.client == nil {
		return ErrSyncDisabled
	}
	err := s.client.Sync(ctx)
	st := jobs.SyncStatus{Status: jobs.StatusOK}
	if err != nil {
		st.Status = jobs.StatusError
		st.Error = err.Error()
		s.log.Warn("library sync failed", "platform", s.client.spec.Name, "err", err)
	} else {
		s.log.Info("library sync requested", "platform", s.client.spec.Name)
	}
	s.history.SetSync(st)
	return err
}

// Stop cancels any pending debounced sync.
func (s *Syncer) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.timer != nil {
		s.timer.Stop()
	}
}
