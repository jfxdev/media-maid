package api

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jfxdev/media-maid/internal/browse"
	"github.com/jfxdev/media-maid/internal/integration"
	"github.com/jfxdev/media-maid/internal/jobs"
	"github.com/jfxdev/media-maid/internal/organizer"
	"github.com/jfxdev/media-maid/internal/orphans"
	"github.com/jfxdev/media-maid/internal/safepath"
)

type fakeOrg struct{ last organizer.OrganizeOptions }

func (f *fakeOrg) Organize(_ context.Context, p string, opts organizer.OrganizeOptions) ([]organizer.Result, error) {
	f.last = opts
	switch p {
	case "../etc":
		return nil, safepath.ErrInvalidPath
	case "evil-link":
		return nil, safepath.ErrUnauthorizedVolume
	case "missing":
		return nil, organizer.ErrNotFound
	case "unknown-id":
		return nil, organizer.ErrOverrideNotFound
	case "tmdb-down":
		return nil, organizer.ErrMetadataUnavailable
	case "no-title":
		return nil, organizer.ErrInvalidOverride
	}
	return []organizer.Result{{Source: p, Status: jobs.StatusPlanned}}, nil
}

func (f *fakeOrg) Scan(context.Context) ([]organizer.Result, error) { return nil, nil }

func (f *fakeOrg) Pending() []organizer.PendingItem {
	return []organizer.PendingItem{{Source: "The.Matrix.1999.1080p.mkv", Status: jobs.StatusConflict}}
}

type fakeOrphans struct{ removed []string }

func (f *fakeOrphans) Scan(context.Context) (*orphans.Report, error) {
	return &orphans.Report{ServerCheck: "ok", Orphans: []orphans.Orphan{}}, nil
}
func (f *fakeOrphans) Report() *orphans.Report { return nil }
func (f *fakeOrphans) Remove(_ context.Context, ids []string) []orphans.RemoveResult {
	f.removed = ids
	return []orphans.RemoveResult{{ID: ids[0], Status: jobs.StatusRemoved}}
}

type fakeFiles struct{ offset, limit int }

func (f *fakeFiles) List(_ context.Context, root, rel string, offset, limit int) (*browse.Listing, error) {
	f.offset, f.limit = offset, limit
	switch {
	case root != "downloads":
		return nil, browse.ErrUnknownRoot
	case rel == "../etc":
		return nil, safepath.ErrInvalidPath
	case rel == "a.mkv":
		return nil, browse.ErrNotDir
	case rel == "missing":
		return nil, browse.ErrNotFound
	case rel == "escape":
		return nil, safepath.ErrUnauthorizedVolume
	case rel == "locked":
		return nil, os.ErrPermission
	}
	return &browse.Listing{Root: root, Path: rel, Entries: []browse.Entry{{Name: ".hidden", Type: browse.TypeDir, Hidden: true}}, Total: 1}, nil
}

type disabledSyncer struct{}

func (disabledSyncer) Request(...string)             {}
func (disabledSyncer) SyncNow(context.Context) error { return integration.ErrSyncDisabled }

const token = "0123456789abcdef0123456789abcdef"

func do(t *testing.T, h http.Handler, method, path, auth, body string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	b, _ := io.ReadAll(rec.Body)
	return rec.Code, strings.TrimSpace(string(b))
}

func TestAPI(t *testing.T) {
	org := &fakeOrg{}
	orph := &fakeOrphans{}
	files := &fakeFiles{}
	srv := New(token, org, orph, disabledSyncer{}, jobs.NewHistory(10), slog.New(slog.NewTextHandler(io.Discard, nil)))
	srv.SetFiles(files)
	srv.SetInfo(Info{Version: "1.2.3", Mode: "jellyfin", OrganizeTrigger: "watch", Roots: []string{"downloads", "movies", "tv"}})
	h := srv.Handler()
	bearer := "Bearer " + token

	tests := []struct {
		name, method, path, auth, body string
		code                           int
		contains                       string
	}{
		{"health open", "GET", "/healthz", "", "", 200, `"ok"`},
		{"no token", "POST", "/api/v1/scan", "", "", 401, "unauthorized"},
		{"wrong token", "POST", "/api/v1/scan", "Bearer nope", "", 401, "unauthorized"},
		{"basic auth", "POST", "/api/v1/scan", "Basic " + token, "", 401, "unauthorized"},
		{"token prefix", "POST", "/api/v1/scan", "Bearer " + token[:31], "", 401, "unauthorized"},
		{"scan", "POST", "/api/v1/scan", bearer, "", 200, `"results":[]`},
		{"scan bad async", "POST", "/api/v1/scan?async=maybe", bearer, "", 400, "invalid_request"},
		{"organize", "POST", "/api/v1/organize", bearer, `{"path":"Movie.2010"}`, 200, "planned"},
		{"traversal", "POST", "/api/v1/organize", bearer, `{"path":"../etc"}`, 400, "invalid_path"},
		{"unauthorized volume", "POST", "/api/v1/organize", bearer, `{"path":"evil-link"}`, 403, "unauthorized_volume"},
		{"missing", "POST", "/api/v1/organize", bearer, `{"path":"missing"}`, 404, "not_found"},
		{"organize overwrite", "POST", "/api/v1/organize", bearer, `{"path":"Movie.2010","overwrite":true}`, 200, "planned"},
		{"overwrite not bool", "POST", "/api/v1/organize", bearer, `{"path":"a","overwrite":"yes"}`, 400, "invalid_request"},
		{"unknown field", "POST", "/api/v1/organize", bearer, `{"path":"a","x":1}`, 400, "invalid_request"},
		{"bad json", "POST", "/api/v1/organize", bearer, `{`, 400, "invalid_request"},
		// A torrent name interpolated into {"path":"%N"} must not inject fields.
		{"injected duplicate key", "POST", "/api/v1/organize", bearer, `{"path":"x","overwrite":true,"path":"y"}`, 400, "invalid_request"},
		{"injected trailing data", "POST", "/api/v1/organize", bearer, `{"path":"x","overwrite":true}"}`, 400, "invalid_request"},
		{"two objects", "POST", "/api/v1/organize", bearer, `{"path":"x"}{"path":"y"}`, 400, "invalid_request"},
		{"nested duplicate", "POST", "/api/v1/orphans/remove", bearer, `{"ids":["a"],"ids":["b"]}`, 400, "invalid_request"},
		{"too large", "POST", "/api/v1/organize", bearer, `{"path":"` + strings.Repeat("a", 70<<10) + `"}`, 400, "invalid_request"},
		{"wrong method", "GET", "/api/v1/organize", bearer, "", 405, ""},
		{"automatic", "POST", "/api/v1/organize", bearer, `{"path":"Movie.2010","automatic":true}`, 200, "planned"},
		{"automatic overwrite", "POST", "/api/v1/organize", bearer, `{"path":"x","automatic":true,"overwrite":true}`, 400, "invalid_request"},
		{"automatic override", "POST", "/api/v1/organize", bearer, `{"path":"x","automatic":true,"kind":"movie","tmdb_id":603}`, 400, "invalid_request"},
		{"override", "POST", "/api/v1/organize", bearer, `{"path":"x","kind":"movie","tmdb_id":603}`, 200, "planned"},
		{"override title", "POST", "/api/v1/preview", bearer, `{"path":"x","kind":"episode","title":"Breaking Bad","year":2008}`, 200, "planned"},
		{"override without kind", "POST", "/api/v1/organize", bearer, `{"path":"x","tmdb_id":603}`, 400, "invalid_override"},
		{"override bad kind", "POST", "/api/v1/organize", bearer, `{"path":"x","kind":"music","title":"X"}`, 400, "invalid_override"},
		{"override bad year", "POST", "/api/v1/organize", bearer, `{"path":"x","kind":"movie","year":99999}`, 400, "invalid_override"},
		{"override negative id", "POST", "/api/v1/organize", bearer, `{"path":"x","kind":"movie","tmdb_id":-1}`, 400, "invalid_override"},
		{"override control chars", "POST", "/api/v1/organize", bearer, `{"path":"x","kind":"movie","title":"a\nb"}`, 400, "invalid_override"},
		{"override long title", "POST", "/api/v1/organize", bearer, `{"path":"x","kind":"movie","title":"` + strings.Repeat("a", 201) + `"}`, 400, "invalid_override"},
		{"override id not a number", "POST", "/api/v1/organize", bearer, `{"path":"x","kind":"movie","tmdb_id":"603"}`, 400, "invalid_request"},
		{"override unknown id", "POST", "/api/v1/organize", bearer, `{"path":"unknown-id","kind":"movie","tmdb_id":1}`, 404, "tmdb_not_found"},
		{"override tmdb down", "POST", "/api/v1/organize", bearer, `{"path":"tmdb-down","kind":"movie","tmdb_id":1}`, 502, "metadata_unavailable"},
		{"override needs title", "POST", "/api/v1/organize", bearer, `{"path":"no-title","kind":"movie","tmdb_id":1}`, 400, "invalid_override"},
		{"pending", "GET", "/api/v1/pending", bearer, "", 200, `"status":"conflict"`},
		{"pending no auth", "GET", "/api/v1/pending", "", "", 401, "unauthorized"},
		{"history", "GET", "/api/v1/history", bearer, "", 200, `"entries"`},
		{"sync disabled", "POST", "/api/v1/sync", bearer, "", 409, "sync_disabled"},
		{"orphans none yet", "GET", "/api/v1/orphans", bearer, "", 404, "no_report"},
		{"orphans scan", "POST", "/api/v1/orphans/scan", bearer, "", 200, `"server_check":"ok"`},
		{"orphans remove empty", "POST", "/api/v1/orphans/remove", bearer, `{"ids":[]}`, 400, "invalid_request"},
		{"orphans remove", "POST", "/api/v1/orphans/remove", bearer, `{"ids":["abc"]}`, 200, "removed"},
		{"orphans remove no auth", "POST", "/api/v1/orphans/remove", "", `{"ids":["abc"]}`, 401, "unauthorized"},
		{"info", "GET", "/api/v1/info", bearer, "", 200, `"roots":["downloads","movies","tv"]`},
		{"info no auth", "GET", "/api/v1/info", "", "", 401, "unauthorized"},
		{"files", "GET", "/api/v1/files?root=downloads", bearer, "", 200, `"hidden":true`},
		{"files no auth", "GET", "/api/v1/files?root=downloads", "", "", 401, "unauthorized"},
		{"files no root", "GET", "/api/v1/files", bearer, "", 400, "invalid_request"},
		{"files unknown root", "GET", "/api/v1/files?root=state", bearer, "", 400, "invalid_request"},
		{"files traversal", "GET", "/api/v1/files?root=downloads&path=../etc", bearer, "", 400, "invalid_path"},
		{"files on a file", "GET", "/api/v1/files?root=downloads&path=a.mkv", bearer, "", 400, "not_a_directory"},
		{"files missing", "GET", "/api/v1/files?root=downloads&path=missing", bearer, "", 404, "not_found"},
		{"files escape", "GET", "/api/v1/files?root=downloads&path=escape", bearer, "", 403, "unauthorized_volume"},
		{"files locked", "GET", "/api/v1/files?root=downloads&path=locked", bearer, "", 403, "permission_denied"},
		{"files bad offset", "GET", "/api/v1/files?root=downloads&offset=-1", bearer, "", 400, "invalid_request"},
		{"files bad limit", "GET", "/api/v1/files?root=downloads&limit=1001", bearer, "", 400, "invalid_request"},
		{"files limit not a number", "GET", "/api/v1/files?root=downloads&limit=ten", bearer, "", 400, "invalid_request"},
		{"files wrong method", "POST", "/api/v1/files?root=downloads", bearer, "", 405, ""},
		{"no ui by default", "GET", "/ui/", "", "", 404, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, body := do(t, h, tt.method, tt.path, tt.auth, tt.body)
			if code != tt.code || !strings.Contains(body, tt.contains) {
				t.Errorf("got %d %s, want %d containing %q", code, body, tt.code, tt.contains)
			}
		})
	}

	do(t, h, "GET", "/api/v1/files?root=downloads&offset=40&limit=20", bearer, "")
	if files.offset != 40 || files.limit != 20 {
		t.Errorf("files paging = %d/%d, want 40/20", files.offset, files.limit)
	}
	do(t, h, "GET", "/api/v1/files?root=downloads", bearer, "")
	if files.offset != 0 || files.limit != browse.DefaultLimit {
		t.Errorf("files default paging = %d/%d", files.offset, files.limit)
	}

	do(t, h, "POST", "/api/v1/preview", bearer, `{"path":"x","overwrite":true}`)
	if !org.last.Preview || !org.last.Overwrite {
		t.Errorf("preview options = %+v, want preview and overwrite", org.last)
	}
	do(t, h, "POST", "/api/v1/organize", bearer, `{"path":"x"}`)
	if org.last.Preview || org.last.Overwrite || !org.last.Explicit || org.last.Override != nil {
		t.Errorf("organize options = %+v, want explicit without preview, overwrite or override", org.last)
	}
	do(t, h, "POST", "/api/v1/organize", bearer, `{"path":"x","automatic":true}`)
	if org.last.Explicit || org.last.Overwrite || org.last.Override != nil {
		t.Errorf("automatic options = %+v, want an automatic run", org.last)
	}
	do(t, h, "POST", "/api/v1/organize", bearer, `{"path":"x","kind":"movie","title":" The Matrix ","year":1999,"tmdb_id":603}`)
	if ov := org.last.Override; ov == nil || *ov != (organizer.Override{Kind: "movie", Title: "The Matrix", Year: 1999, TMDBID: 603}) {
		t.Errorf("override = %+v", ov)
	}
}

// TestReadOnlyToken: the web UI token reads (GET) and never changes anything.
func TestReadOnlyToken(t *testing.T) {
	uiToken := "fedcba9876543210fedcba9876543210"
	orph := &fakeOrphans{}
	srv := New(token, &fakeOrg{}, orph, disabledSyncer{}, jobs.NewHistory(10), slog.New(slog.NewTextHandler(io.Discard, nil)))
	srv.SetFiles(&fakeFiles{})
	srv.SetUIToken(uiToken)
	h := srv.Handler()
	read := "Bearer " + uiToken
	tests := []struct {
		name, method, path, auth, body string
		code                           int
		contains                       string
	}{
		{"pending", "GET", "/api/v1/pending", read, "", 200, `"items"`},
		{"history", "GET", "/api/v1/history", read, "", 200, `"entries"`},
		{"orphans", "GET", "/api/v1/orphans", read, "", 404, "no_report"},
		{"files", "GET", "/api/v1/files?root=downloads", read, "", 200, `"entries"`},
		{"info", "GET", "/api/v1/info", read, "", 200, `"roots":[]`},
		{"head", "HEAD", "/api/v1/pending", read, "", 200, ""},
		{"scan", "POST", "/api/v1/scan", read, "", 403, "read_only_token"},
		{"organize", "POST", "/api/v1/organize", read, `{"path":"x","overwrite":true}`, 403, "read_only_token"},
		{"preview", "POST", "/api/v1/preview", read, `{"path":"x"}`, 403, "read_only_token"},
		{"sync", "POST", "/api/v1/sync", read, "", 403, "read_only_token"},
		{"orphans scan", "POST", "/api/v1/orphans/scan", read, "", 403, "read_only_token"},
		{"orphans remove", "POST", "/api/v1/orphans/remove", read, `{"ids":["abc"]}`, 403, "read_only_token"},
		{"wrong ui token", "GET", "/api/v1/pending", "Bearer " + uiToken[:31] + "x", "", 401, "unauthorized"},
		{"api token still writes", "POST", "/api/v1/orphans/scan", "Bearer " + token, "", 200, "server_check"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, body := do(t, h, tt.method, tt.path, tt.auth, tt.body)
			if code != tt.code || !strings.Contains(body, tt.contains) {
				t.Errorf("got %d %s, want %d containing %q", code, body, tt.code, tt.contains)
			}
		})
	}
	if orph.removed != nil {
		t.Error("the read-only token removed orphans")
	}

	// Without a UI token configured, an empty bearer never matches it.
	h = New(token, &fakeOrg{}, &fakeOrphans{}, disabledSyncer{}, jobs.NewHistory(10), slog.New(slog.NewTextHandler(io.Discard, nil))).Handler()
	if code, _ := do(t, h, "GET", "/api/v1/pending", "Bearer ", ""); code != 401 {
		t.Errorf("empty bearer = %d, want 401", code)
	}
}

// TestFilesNeverReturnContents is a regression guard: listing a folder, or
// asking for a file, never returns what the file holds.
func TestFilesNeverReturnContents(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "Movie.2010"), 0o755); err != nil {
		t.Fatal(err)
	}
	const marker = "SECRET-CONTENT-MARKER"
	for _, name := range []string{"Movie.2010/Movie.2010.mkv", "Movie.2010/.hidden.nfo", "notes.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(marker), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	srv := New(token, &fakeOrg{}, &fakeOrphans{}, disabledSyncer{}, jobs.NewHistory(10), slog.New(slog.NewTextHandler(io.Discard, nil)))
	srv.SetFiles(browse.New(map[string]*os.Root{"downloads": root}, nil, nil))
	h := srv.Handler()
	for q, want := range map[string]int{
		"":                                      200,
		"&path=Movie.2010":                      200,
		"&path=notes.txt":                       400,
		"&path=Movie.2010/Movie.2010.mkv":       400,
		"&path=Movie.2010/.hidden.nfo":          400,
		"&path=Movie.2010/Movie.2010.mkv&raw=1": 400,
	} {
		code, body := do(t, h, "GET", "/api/v1/files?root=downloads"+q, "Bearer "+token, "")
		if strings.Contains(body, marker) {
			t.Errorf("%s: response holds file contents: %d %s", q, code, body)
		}
		if code != want || (want == 400 && !strings.Contains(body, "not_a_directory")) {
			t.Errorf("%s: got %d %s, want %d", q, code, body, want)
		}
	}
	if _, body := do(t, h, "GET", "/api/v1/files?root=downloads&path=Movie.2010", "Bearer "+token, ""); !strings.Contains(body, `".hidden.nfo"`) {
		t.Errorf("hidden entry not listed: %s", body)
	}
}

func TestUIRoutes(t *testing.T) {
	srv := New(token, &fakeOrg{}, &fakeOrphans{}, disabledSyncer{}, jobs.NewHistory(10), slog.New(slog.NewTextHandler(io.Discard, nil)))
	srv.SetUI(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ui:"+r.URL.Path) }))
	h := srv.Handler()
	if code, body := do(t, h, "GET", "/ui/", "", ""); code != 200 || body != "ui:/ui/" {
		t.Errorf("/ui/ = %d %s", code, body)
	}
	if code, body := do(t, h, "GET", "/ui/assets/x.js", "", ""); code != 200 || body != "ui:/ui/assets/x.js" {
		t.Errorf("/ui/assets = %d %s", code, body)
	}
	req := httptest.NewRequest("GET", "/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/ui/" {
		t.Errorf("/ = %d %s, want a redirect to /ui/", rec.Code, rec.Header().Get("Location"))
	}
	if code, _ := do(t, h, "GET", "/elsewhere", "", ""); code != 404 {
		t.Errorf("/elsewhere = %d, want 404", code)
	}
	if code, _ := do(t, h, "POST", "/ui/", "", ""); code != 405 {
		t.Errorf("POST /ui/ = %d, want 405", code)
	}
}

func TestHealthReflectsWatcher(t *testing.T) {
	srv := New(token, &fakeOrg{}, &fakeOrphans{}, disabledSyncer{}, jobs.NewHistory(10), slog.New(slog.NewTextHandler(io.Discard, nil)))
	up := true
	srv.SetHealth(func() bool { return up })
	h := srv.Handler()
	if code, body := do(t, h, "GET", "/healthz", "", ""); code != 200 || !strings.Contains(body, `"ok"`) {
		t.Errorf("watcher up: got %d %s", code, body)
	}
	degraded := true
	srv.SetDegraded(func() bool { return degraded })
	if code, body := do(t, h, "GET", "/healthz", "", ""); code != 200 || !strings.Contains(body, `"watcher":"degraded"`) {
		t.Errorf("watcher degraded: got %d %s", code, body)
	}
	up = false
	if code, body := do(t, h, "GET", "/healthz", "", ""); code != 503 || !strings.Contains(body, `"watcher":"stopped"`) {
		t.Errorf("watcher down: got %d %s", code, body)
	}
}

// blockingOrg holds Scan until release is closed.
type blockingOrg struct {
	fakeOrg
	started chan struct{}
	release chan struct{}
}

func (b *blockingOrg) Scan(ctx context.Context) ([]organizer.Result, error) {
	b.started <- struct{}{}
	<-b.release
	return nil, nil
}

func TestAsyncScan(t *testing.T) {
	org := &blockingOrg{started: make(chan struct{}, 1), release: make(chan struct{})}
	h := New(token, org, &fakeOrphans{}, disabledSyncer{}, jobs.NewHistory(10), slog.New(slog.NewTextHandler(io.Discard, nil))).Handler()
	bearer := "Bearer " + token
	if code, body := do(t, h, "POST", "/api/v1/scan?async=true", bearer, ""); code != 202 || !strings.Contains(body, "started") {
		t.Fatalf("async scan = %d %s", code, body)
	}
	<-org.started
	if code, body := do(t, h, "POST", "/api/v1/scan?async=true", bearer, ""); code != 409 || !strings.Contains(body, "scan_running") {
		t.Errorf("second scan = %d %s", code, body)
	}
	if code, _ := do(t, h, "POST", "/api/v1/scan", bearer, ""); code != 409 {
		t.Errorf("sync scan during async = %d", code)
	}
	close(org.release)
	for i := 0; i < 100; i++ {
		if code, _ := do(t, h, "POST", "/api/v1/scan", bearer, ""); code == 200 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Error("scan never became available again")
}

func TestDocs(t *testing.T) {
	h := New(token, &fakeOrg{}, &fakeOrphans{}, disabledSyncer{}, jobs.NewHistory(10), slog.New(slog.NewTextHandler(io.Discard, nil))).Handler()
	for path, want := range map[string]string{
		"/openapi.yaml": "openapi: 3.1.0",
		"/docs":         "swagger-ui",
		"/docs/init.js": "/openapi.yaml",
	} {
		code, body := do(t, h, "GET", path, "", "")
		if code != 200 || !strings.Contains(body, want) {
			t.Errorf("%s: got %d, want 200 containing %q", path, code, want)
		}
	}
}

// TestDocsSubresourceIntegrity: the token may be typed into /docs, so every
// file it loads from the CDN must be pinned by hash.
func TestDocsSubresourceIntegrity(t *testing.T) {
	tags := regexp.MustCompile(`<(?:script|link)\b[^>]*https://[^>]*>`).FindAllString(docsPage, -1)
	if len(tags) != 2 {
		t.Fatalf("found %d external assets, want 2: %q", len(tags), tags)
	}
	for _, tag := range tags {
		if !regexp.MustCompile(`integrity="sha384-[A-Za-z0-9+/]{64}"`).MatchString(tag) || !strings.Contains(tag, `crossorigin="anonymous"`) {
			t.Errorf("external asset without integrity: %s", tag)
		}
	}
}

// TestSpecCoversRoutes keeps openapi.yaml in sync with the routes in Handler.
func TestSpecCoversRoutes(t *testing.T) {
	src, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	routes := regexp.MustCompile(`HandleFunc\("(GET|POST) (/(?:api|healthz)[^"]*)"`).FindAllStringSubmatch(string(src), -1)
	if len(routes) < 9 {
		t.Fatalf("found only %d routes in server.go", len(routes))
	}
	spec := string(openAPISpec)
	for _, r := range routes {
		method, path := strings.ToLower(r[1]), r[2]
		i := strings.Index(spec, "\n  "+path+":\n")
		if i < 0 {
			t.Errorf("%s missing from openapi.yaml", path)
			continue
		}
		block := spec[i+len(path)+4:]
		if j := regexp.MustCompile(`\n  /`).FindStringIndex(block); j != nil {
			block = block[:j[0]]
		}
		if !strings.Contains(block, "\n    "+method+":") {
			t.Errorf("%s %s missing from openapi.yaml", r[1], path)
		}
	}
}
