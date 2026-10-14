package ui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func get(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
	return rec
}

func TestHandler(t *testing.T) {
	h := NewHandler(fstest.MapFS{
		"index.html":           {Data: []byte(`<!doctype html><script type="module" src="/ui/assets/app-1a2b.js"></script>`)},
		"assets/app-1a2b.js":   {Data: []byte("console.log(1)")},
		"assets/app-1a2b.css":  {Data: []byte("body{}")},
		"assets/nested/x.js":   {Data: []byte("x")},
		".keep":                {},
		"secret.txt":           {Data: []byte("not an asset")},
		"assets/dir/.keep":     {},
		"assets/dir/inner.txt": {Data: []byte("inner")},
	})
	tests := []struct {
		path, contains, ctype, cache string
		code                         int
	}{
		{"/ui/", "<!doctype html>", "text/html", "no-cache", 200},
		{"/ui/assets/app-1a2b.js", "console.log", "javascript", "immutable", 200},
		{"/ui/assets/app-1a2b.css", "body{}", "text/css", "immutable", 200},
		{"/ui/index.html", "", "", "", 301}, // ServeFileFS canonicalizes to /ui/
		{"/ui/assets/", "", "", "", 404},
		{"/ui/assets/dir", "", "", "", 404},
		{"/ui/assets/missing.js", "", "", "", 404},
		{"/ui/.keep", "", "", "", 404},
		{"/ui/secret.txt", "", "", "", 404},
		{"/ui/assets/../secret.txt", "", "", "", 404},
		{"/elsewhere", "", "", "", 404},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			rec := get(t, h, tt.path)
			if rec.Code != tt.code || !strings.Contains(rec.Body.String(), tt.contains) {
				t.Fatalf("got %d %q, want %d containing %q", rec.Code, rec.Body.String(), tt.code, tt.contains)
			}
			if !strings.Contains(rec.Header().Get("Content-Type"), tt.ctype) || !strings.Contains(rec.Header().Get("Cache-Control"), tt.cache) {
				t.Errorf("headers = %v", rec.Header())
			}
			if rec.Header().Get("Content-Security-Policy") != CSP || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
				t.Errorf("security headers missing: %v", rec.Header())
			}
		})
	}
}

// TestCSPSelfOnly: the UI may load nothing from another origin and run no
// inline script.
func TestCSPSelfOnly(t *testing.T) {
	for _, d := range strings.Split(CSP, ";") {
		d = strings.TrimSpace(d)
		if strings.Contains(d, "http") || strings.Contains(d, "*") {
			t.Errorf("directive allows another origin: %q", d)
		}
		if strings.HasPrefix(d, "script-src") && d != "script-src 'self'" {
			t.Errorf("scripts must be 'self' only: %q", d)
		}
	}
}

func TestNotBuilt(t *testing.T) {
	h := NewHandler(fstest.MapFS{".keep": {}})
	rec := get(t, h, "/ui/")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "built without the web UI") {
		t.Errorf("got %d %s", rec.Code, rec.Body.String())
	}
	// The embedded build, whatever it holds, serves its index.
	if rec := get(t, Handler(), "/ui/"); rec.Code != 200 {
		t.Errorf("embedded /ui/ = %d", rec.Code)
	}
}
