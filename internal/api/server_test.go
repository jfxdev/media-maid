package api

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jfxdev/media-maid/internal/integration"
	"github.com/jfxdev/media-maid/internal/jobs"
	"github.com/jfxdev/media-maid/internal/organizer"
	"github.com/jfxdev/media-maid/internal/orphans"
	"github.com/jfxdev/media-maid/internal/safepath"
)

type fakeOrg struct{ lastPreview bool }

func (f *fakeOrg) Organize(_ context.Context, p string, preview bool) ([]organizer.Result, error) {
	f.lastPreview = preview
	switch p {
	case "../etc":
		return nil, safepath.ErrInvalidPath
	case "evil-link":
		return nil, safepath.ErrUnauthorizedVolume
	case "missing":
		return nil, organizer.ErrNotFound
	}
	return []organizer.Result{{Source: p, Status: jobs.StatusPlanned}}, nil
}

func (f *fakeOrg) Scan(context.Context) ([]organizer.Result, error) { return nil, nil }

type fakeOrphans struct{ removed []string }

func (f *fakeOrphans) Scan(context.Context) (*orphans.Report, error) {
	return &orphans.Report{ServerCheck: "ok", Orphans: []orphans.Orphan{}}, nil
}
func (f *fakeOrphans) Report() *orphans.Report { return nil }
func (f *fakeOrphans) Remove(_ context.Context, ids []string) []orphans.RemoveResult {
	f.removed = ids
	return []orphans.RemoveResult{{ID: ids[0], Status: jobs.StatusRemoved}}
}

type disabledSyncer struct{}

func (disabledSyncer) Request()                      {}
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
	h := New(token, org, orph, disabledSyncer{}, jobs.NewHistory(10), slog.New(slog.NewTextHandler(io.Discard, nil))).Handler()
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
		{"organize", "POST", "/api/v1/organize", bearer, `{"path":"Movie.2010"}`, 200, "planned"},
		{"traversal", "POST", "/api/v1/organize", bearer, `{"path":"../etc"}`, 400, "invalid_path"},
		{"unauthorized volume", "POST", "/api/v1/organize", bearer, `{"path":"evil-link"}`, 403, "unauthorized_volume"},
		{"missing", "POST", "/api/v1/organize", bearer, `{"path":"missing"}`, 404, "not_found"},
		{"unknown field", "POST", "/api/v1/organize", bearer, `{"path":"a","x":1}`, 400, "invalid_request"},
		{"bad json", "POST", "/api/v1/organize", bearer, `{`, 400, "invalid_request"},
		{"too large", "POST", "/api/v1/organize", bearer, `{"path":"` + strings.Repeat("a", 70<<10) + `"}`, 400, "invalid_request"},
		{"wrong method", "GET", "/api/v1/organize", bearer, "", 405, ""},
		{"history", "GET", "/api/v1/history", bearer, "", 200, `"entries"`},
		{"sync disabled", "POST", "/api/v1/sync", bearer, "", 409, "sync_disabled"},
		{"orphans none yet", "GET", "/api/v1/orphans", bearer, "", 404, "no_report"},
		{"orphans scan", "POST", "/api/v1/orphans/scan", bearer, "", 200, `"server_check":"ok"`},
		{"orphans remove empty", "POST", "/api/v1/orphans/remove", bearer, `{"ids":[]}`, 400, "invalid_request"},
		{"orphans remove", "POST", "/api/v1/orphans/remove", bearer, `{"ids":["abc"]}`, 200, "removed"},
		{"orphans remove no auth", "POST", "/api/v1/orphans/remove", "", `{"ids":["abc"]}`, 401, "unauthorized"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, body := do(t, h, tt.method, tt.path, tt.auth, tt.body)
			if code != tt.code || !strings.Contains(body, tt.contains) {
				t.Errorf("got %d %s, want %d containing %q", code, body, tt.code, tt.contains)
			}
		})
	}

	do(t, h, "POST", "/api/v1/preview", bearer, `{"path":"x"}`)
	if !org.lastPreview {
		t.Error("preview did not request a dry run")
	}
}
