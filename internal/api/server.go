// Package api exposes the Media Maid HTTP API.
package api

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jfxdev/media-maid/internal/integration"
	"github.com/jfxdev/media-maid/internal/jobs"
	"github.com/jfxdev/media-maid/internal/organizer"
	"github.com/jfxdev/media-maid/internal/orphans"
	"github.com/jfxdev/media-maid/internal/safepath"
)

const maxBody = 64 << 10

// Organizer is what the API needs from the organizer.
type Organizer interface {
	Organize(ctx context.Context, path string, opts organizer.OrganizeOptions) ([]organizer.Result, error)
	Scan(ctx context.Context) ([]organizer.Result, error)
}

// Orphans is what the API needs from the orphan detector.
type Orphans interface {
	Scan(ctx context.Context) (*orphans.Report, error)
	Report() *orphans.Report
	Remove(ctx context.Context, ids []string) []orphans.RemoveResult
}

// Server wires handlers together.
type Server struct {
	token     []byte
	organizer Organizer
	orphans   Orphans
	syncer    integration.LibrarySyncer
	history   *jobs.History
	log       *slog.Logger

	// bg bounds background work (async scans); it is cancelled at shutdown.
	bg       context.Context
	scanning atomic.Bool
}

// New builds the API server. token must already be validated.
func New(token string, org Organizer, orph Orphans, syncer integration.LibrarySyncer, history *jobs.History, log *slog.Logger) *Server {
	return &Server{token: []byte(token), organizer: org, orphans: orph, syncer: syncer, history: history, log: log, bg: context.Background()}
}

// SetContext sets the context background work runs under (cancelled at
// shutdown). Call it before serving.
func (s *Server) SetContext(ctx context.Context) { s.bg = ctx }

// Handler returns the HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /openapi.yaml", serveSpec)
	mux.HandleFunc("GET /docs", serveDocs)
	mux.HandleFunc("GET /docs/init.js", serveDocsInit)

	api := http.NewServeMux()
	api.HandleFunc("POST /api/v1/scan", s.scan)
	api.HandleFunc("POST /api/v1/organize", s.organize(false))
	api.HandleFunc("POST /api/v1/preview", s.organize(true))
	api.HandleFunc("GET /api/v1/history", s.historyHandler)
	api.HandleFunc("POST /api/v1/sync", s.sync)
	api.HandleFunc("POST /api/v1/orphans/scan", s.orphansScan)
	api.HandleFunc("GET /api/v1/orphans", s.orphansGet)
	api.HandleFunc("POST /api/v1/orphans/remove", s.orphansRemove)
	mux.Handle("/api/", s.auth(api))
	return mux
}

// NewHTTPServer returns an http.Server with conservative timeouts.
func NewHTTPServer(addr string, h http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      10 * time.Minute,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
}

func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := r.Header.Get("Authorization")
		tok, ok := strings.CutPrefix(h, "Bearer ")
		if !ok || subtle.ConstantTimeCompare([]byte(tok), s.token) != 1 {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// decode reads exactly one JSON object into v. Unknown fields, duplicate keys
// and trailing data are rejected, so a value interpolated into a JSON template
// (e.g. a torrent name in a client hook) cannot smuggle in extra fields.
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	body, err := io.ReadAll(r.Body)
	if err == nil {
		err = strictJSON(body)
	}
	if err == nil {
		dec := json.NewDecoder(bytes.NewReader(body))
		dec.DisallowUnknownFields()
		err = dec.Decode(v)
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request")
		return false
	}
	return true
}

var errDuplicateKey = errors.New("duplicate key")

// strictJSON checks that data is a single JSON value without duplicate object
// keys at any depth and without trailing data.
func strictJSON(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := strictValue(dec); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return errors.New("trailing data")
	}
	return nil
}

func strictValue(dec *json.Decoder) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	switch tok {
	case json.Delim('{'):
		seen := map[string]bool{}
		for dec.More() {
			k, err := dec.Token()
			if err != nil {
				return err
			}
			key, _ := k.(string)
			if seen[key] {
				return errDuplicateKey
			}
			seen[key] = true
			if err := strictValue(dec); err != nil {
				return err
			}
		}
		_, err = dec.Token() // '}'
		return err
	case json.Delim('['):
		for dec.More() {
			if err := strictValue(dec); err != nil {
				return err
			}
		}
		_, err = dec.Token() // ']'
		return err
	}
	return nil
}

// scan organizes the whole downloads folder. With ?async=true it answers 202
// at once and the outcome goes to the history; one scan runs at a time.
func (s *Server) scan(w http.ResponseWriter, r *http.Request) {
	async := false
	switch r.URL.Query().Get("async") {
	case "", "false":
	case "true":
		async = true
	default:
		writeError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	if !s.scanning.CompareAndSwap(false, true) {
		writeError(w, http.StatusConflict, "scan_running")
		return
	}
	if async {
		go func() {
			defer s.scanning.Store(false)
			if _, err := s.organizer.Scan(s.bg); err != nil && s.bg.Err() == nil {
				s.log.Error("background scan failed", "err", err)
			}
		}()
		writeJSON(w, http.StatusAccepted, map[string]string{"status": "started"})
		return
	}
	defer s.scanning.Store(false)
	res, err := s.organizer.Scan(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": nonNil(res)})
}

func (s *Server) organize(preview bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Path      string `json:"path"`
			Overwrite bool   `json:"overwrite"`
		}
		if !decode(w, r, &req) {
			return
		}
		opts := organizer.OrganizeOptions{Preview: preview, Overwrite: req.Overwrite, Explicit: true}
		res, err := s.organizer.Organize(r.Context(), req.Path, opts)
		if err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"results": nonNil(res)})
	}
}

func (s *Server) historyHandler(w http.ResponseWriter, r *http.Request) {
	entries, last := s.history.Snapshot()
	writeJSON(w, http.StatusOK, map[string]any{"entries": entries, "last_sync": last})
}

func (s *Server) sync(w http.ResponseWriter, r *http.Request) {
	if err := s.syncer.SyncNow(r.Context()); err != nil {
		if errors.Is(err, integration.ErrSyncDisabled) {
			writeError(w, http.StatusConflict, "sync_disabled")
			return
		}
		writeError(w, http.StatusBadGateway, "sync_failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) orphansScan(w http.ResponseWriter, r *http.Request) {
	rep, err := s.orphans.Scan(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rep)
}

func (s *Server) orphansGet(w http.ResponseWriter, r *http.Request) {
	rep := s.orphans.Report()
	if rep == nil {
		writeError(w, http.StatusNotFound, "no_report")
		return
	}
	writeJSON(w, http.StatusOK, rep)
}

func (s *Server) orphansRemove(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs []string `json:"ids"`
	}
	if !decode(w, r, &req) {
		return
	}
	if len(req.IDs) == 0 || len(req.IDs) > 1000 {
		writeError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": s.orphans.Remove(r.Context(), req.IDs)})
}

func (s *Server) fail(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, safepath.ErrInvalidPath):
		writeError(w, http.StatusBadRequest, "invalid_path")
	case errors.Is(err, safepath.ErrUnauthorizedVolume):
		writeError(w, http.StatusForbidden, "unauthorized_volume")
	case errors.Is(err, organizer.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found")
	default:
		s.log.Error("request failed", "err", err)
		writeError(w, http.StatusInternalServerError, "internal_error")
	}
}

func nonNil[T any](v []T) []T {
	if v == nil {
		return []T{}
	}
	return v
}

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
