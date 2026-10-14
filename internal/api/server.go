// Package api exposes the Media Maid HTTP API.
package api

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/jfxdev/media-maid/internal/browse"
	"github.com/jfxdev/media-maid/internal/integration"
	"github.com/jfxdev/media-maid/internal/jobs"
	"github.com/jfxdev/media-maid/internal/organizer"
	"github.com/jfxdev/media-maid/internal/orphans"
	"github.com/jfxdev/media-maid/internal/parser"
	"github.com/jfxdev/media-maid/internal/safepath"
)

const maxBody = 64 << 10

// Organizer is what the API needs from the organizer.
type Organizer interface {
	Organize(ctx context.Context, path string, opts organizer.OrganizeOptions) ([]organizer.Result, error)
	Scan(ctx context.Context) ([]organizer.Result, error)
	Pending() []organizer.PendingItem
}

// Orphans is what the API needs from the orphan detector.
type Orphans interface {
	Scan(ctx context.Context) (*orphans.Report, error)
	Report() *orphans.Report
	Remove(ctx context.Context, ids []string) []orphans.RemoveResult
}

// Files is what the API needs from the folder lister.
type Files interface {
	List(ctx context.Context, root, rel string, offset, limit int) (*browse.Listing, error)
}

// Info describes the running instance to clients (the web UI). It holds no
// secrets and no host paths.
type Info struct {
	Version         string   `json:"version"`
	Mode            string   `json:"mode"`
	DryRun          bool     `json:"dry_run"`
	OrganizeTrigger string   `json:"organize_trigger"`
	SyncEnabled     bool     `json:"sync_enabled"`
	TMDBEnabled     bool     `json:"tmdb_enabled"`
	LLMEnabled      bool     `json:"llm_enabled"`
	Roots           []string `json:"roots"`
}

// Server wires handlers together.
type Server struct {
	token []byte
	// uiToken, when set, is accepted on GET routes only (the web UI).
	uiToken   []byte
	organizer Organizer
	orphans   Orphans
	syncer    integration.LibrarySyncer
	history   *jobs.History
	log       *slog.Logger

	// bg bounds background work (async scans); it is cancelled at shutdown.
	bg       context.Context
	scanning atomic.Bool
	// watcherOK, when set, makes /healthz fail while the watcher is down.
	watcherOK func() bool
	// watcherDegraded, when set, makes /healthz say some folders are not
	// watched (it still answers 200: periodic scans cover them).
	watcherDegraded func() bool

	files Files
	info  Info
	// ui, when set, serves the web UI at /ui/.
	ui http.Handler
}

// New builds the API server. token must already be validated.
func New(token string, org Organizer, orph Orphans, syncer integration.LibrarySyncer, history *jobs.History, log *slog.Logger) *Server {
	return &Server{token: []byte(token), organizer: org, orphans: orph, syncer: syncer, history: history, log: log, bg: context.Background()}
}

// SetContext sets the context background work runs under (cancelled at
// shutdown). Call it before serving.
func (s *Server) SetContext(ctx context.Context) { s.bg = ctx }

// SetHealth makes /healthz report 503 while watcherOK returns false. Call it
// before serving.
func (s *Server) SetHealth(watcherOK func() bool) { s.watcherOK = watcherOK }

// SetDegraded makes /healthz report "watcher": "degraded" while degraded
// returns true. Call it before serving.
func (s *Server) SetDegraded(degraded func() bool) { s.watcherDegraded = degraded }

// SetUIToken sets the read-only token of the web UI: it authorizes GET
// requests only. It must be validated (and differ from the API token). Call it
// before serving.
func (s *Server) SetUIToken(token string) { s.uiToken = []byte(token) }

// SetFiles sets the folder lister behind GET /api/v1/files. Call it before
// serving.
func (s *Server) SetFiles(f Files) { s.files = f }

// SetInfo sets what GET /api/v1/info reports. Call it before serving.
func (s *Server) SetInfo(info Info) { s.info = info }

// SetUI serves h at /ui/ (and redirects / there). Call it before serving.
func (s *Server) SetUI(h http.Handler) { s.ui = h }

// Handler returns the HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.health)
	mux.HandleFunc("GET /openapi.yaml", serveSpec)
	mux.HandleFunc("GET /docs", serveDocs)
	mux.HandleFunc("GET /docs/init.js", serveDocsInit)
	if s.ui != nil {
		// The UI files hold no data: like /docs they are public, and every
		// API call they make carries the token.
		mux.Handle("GET /ui/", s.ui)
		mux.Handle("GET /{$}", http.RedirectHandler("/ui/", http.StatusFound))
	}

	api := http.NewServeMux()
	api.HandleFunc("POST /api/v1/scan", s.scan)
	api.HandleFunc("POST /api/v1/organize", s.organize(false))
	api.HandleFunc("POST /api/v1/preview", s.organize(true))
	api.HandleFunc("GET /api/v1/pending", s.pending)
	api.HandleFunc("GET /api/v1/history", s.historyHandler)
	api.HandleFunc("POST /api/v1/sync", s.sync)
	api.HandleFunc("POST /api/v1/orphans/scan", s.orphansScan)
	api.HandleFunc("GET /api/v1/orphans", s.orphansGet)
	api.HandleFunc("POST /api/v1/orphans/remove", s.orphansRemove)
	// GET routes never change anything: the read-only UI token relies on it.
	api.HandleFunc("GET /api/v1/info", s.infoHandler)
	api.HandleFunc("GET /api/v1/files", s.filesHandler)
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

// auth accepts the API token on every route and the UI token, when set, on
// GET (and HEAD) requests only: GET routes never change anything.
func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := r.Header.Get("Authorization")
		tok, ok := strings.CutPrefix(h, "Bearer ")
		full := subtle.ConstantTimeCompare([]byte(tok), s.token) == 1
		read := len(s.uiToken) > 0 && subtle.ConstantTimeCompare([]byte(tok), s.uiToken) == 1
		switch {
		case !ok || (!full && !read):
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		case !full && r.Method != http.MethodGet && r.Method != http.MethodHead:
			writeError(w, http.StatusForbidden, "read_only_token")
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

// health is the liveness probe: the process serves requests and, when
// configured, the downloads watcher is running.
func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	if s.watcherOK != nil && !s.watcherOK() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unhealthy", "watcher": "stopped"})
		return
	}
	if s.watcherDegraded != nil && s.watcherDegraded() {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "watcher": "degraded"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
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
			// Automatic organizes like the watcher (a torrent client's
			// completion hook): the ledger is reused, so a file deleted
			// from the library is not linked again on a re-fired hook.
			Automatic bool   `json:"automatic"`
			Kind      string `json:"kind"`
			Title     string `json:"title"`
			Year      int    `json:"year"`
			TMDBID    int    `json:"tmdb_id"`
		}
		if !decode(w, r, &req) {
			return
		}
		manual := req.Kind != "" || req.Title != "" || req.Year != 0 || req.TMDBID != 0
		if req.Automatic && (req.Overwrite || manual) {
			// Overwriting and naming a title are decisions; an automatic
			// run never takes them.
			writeError(w, http.StatusBadRequest, "invalid_request")
			return
		}
		opts := organizer.OrganizeOptions{Preview: preview, Overwrite: req.Overwrite, Explicit: !req.Automatic}
		if manual {
			ov, ok := override(req.Kind, req.Title, req.Year, req.TMDBID)
			if !ok {
				writeError(w, http.StatusBadRequest, "invalid_override")
				return
			}
			opts.Override = ov
		}
		res, err := s.organizer.Organize(r.Context(), req.Path, opts)
		if err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"results": nonNil(res)})
	}
}

// Bounds of a manual identification.
const (
	maxOverrideTitle = 200
	minOverrideYear  = 1870
	maxOverrideYear  = 2100
	maxTMDBID        = 1 << 30
)

// override validates a manual identification: kind is required with any
// other field; the title is plain text (it is sanitized again before it
// becomes a path); year and tmdb_id are in range.
func override(kind, title string, year, tmdbID int) (*organizer.Override, bool) {
	k := parser.Kind(kind)
	if k != parser.Movie && k != parser.Episode {
		return nil, false
	}
	title = strings.TrimSpace(title)
	if len([]rune(title)) > maxOverrideTitle || strings.IndexFunc(title, unicode.IsControl) >= 0 {
		return nil, false
	}
	if year != 0 && (year < minOverrideYear || year > maxOverrideYear) {
		return nil, false
	}
	if tmdbID < 0 || tmdbID > maxTMDBID {
		return nil, false
	}
	return &organizer.Override{Kind: k, Title: title, Year: year, TMDBID: tmdbID}, true
}

// pending lists the downloads that need a decision (conflicts, duplicates,
// unidentified titles, errors), oldest first.
func (s *Server) pending(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"items": nonNil(s.organizer.Pending())})
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
	if s.watcherDegraded != nil && s.watcherDegraded() {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "watcher": "degraded"})
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

// infoHandler describes the instance: version, mode and enabled layers.
func (s *Server) infoHandler(w http.ResponseWriter, r *http.Request) {
	info := s.info
	info.Roots = nonNil(info.Roots)
	writeJSON(w, http.StatusOK, info)
}

// filesHandler lists one folder of a root: names and metadata only. There is
// no route that reads or serves file contents.
func (s *Server) filesHandler(w http.ResponseWriter, r *http.Request) {
	if s.files == nil {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	q := r.URL.Query()
	offset, ok1 := queryInt(q.Get("offset"), 0, 0, 1<<30)
	limit, ok2 := queryInt(q.Get("limit"), browse.DefaultLimit, 1, browse.MaxLimit)
	root := q.Get("root")
	if !ok1 || !ok2 || root == "" {
		writeError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	res, err := s.files.List(r.Context(), root, q.Get("path"), offset, limit)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// queryInt parses an optional integer query parameter within [lo, hi].
func queryInt(v string, def, lo, hi int) (int, bool) {
	if v == "" {
		return def, true
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < lo || n > hi {
		return 0, false
	}
	return n, true
}

func (s *Server) fail(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, browse.ErrUnknownRoot):
		writeError(w, http.StatusBadRequest, "invalid_request")
	case errors.Is(err, browse.ErrNotDir):
		writeError(w, http.StatusBadRequest, "not_a_directory")
	case errors.Is(err, browse.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found")
	case errors.Is(err, fs.ErrPermission):
		writeError(w, http.StatusForbidden, "permission_denied")
	case errors.Is(err, safepath.ErrInvalidPath):
		writeError(w, http.StatusBadRequest, "invalid_path")
	case errors.Is(err, safepath.ErrUnauthorizedVolume):
		writeError(w, http.StatusForbidden, "unauthorized_volume")
	case errors.Is(err, organizer.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found")
	case errors.Is(err, organizer.ErrInvalidOverride):
		writeError(w, http.StatusBadRequest, "invalid_override")
	case errors.Is(err, organizer.ErrOverrideNotFound):
		writeError(w, http.StatusNotFound, "tmdb_not_found")
	case errors.Is(err, organizer.ErrMetadataUnavailable):
		writeError(w, http.StatusBadGateway, "metadata_unavailable")
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
