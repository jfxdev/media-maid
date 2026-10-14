// Package ui serves the optional web UI (UI_ENABLED) embedded in the binary.
// The app itself lives in web/ and is built into dist/ (npm run build) before
// go build; without it the binary still builds and serves a short notice.
//
// The UI is read-only: it only calls GET routes, with the UI_TOKEN typed
// into its login page. Everything it loads comes from this package (the CSP
// allows nothing else).
package ui

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed all:dist
var dist embed.FS

// CSP is the Content-Security-Policy of every UI response. Styles allow
// 'unsafe-inline' because the dialog primitives inject a <style> element
// (scroll lock); scripts never do.
const CSP = "default-src 'none'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; " +
	"font-src 'self'; connect-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"

// Prefix is where the UI is served.
const Prefix = "/ui/"

// notBuilt is served when the binary was built without the app.
const notBuilt = `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><title>Media Maid</title></head>
<body><p>This Media Maid binary was built without the web UI. Build it with
<code>npm --prefix web ci &amp;&amp; npm --prefix web run build</code> before
<code>go build</code>, or use the published image.</p></body></html>
`

// Handler serves the embedded UI.
func Handler() http.Handler {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err) // the embed pattern guarantees the directory
	}
	return NewHandler(sub)
}

// Built reports whether the app was built into the binary.
func Built() bool {
	_, err := fs.Stat(dist, "dist/index.html")
	return err == nil
}

// NewHandler serves fsys (the build output) under Prefix: index.html at the
// prefix itself and the regular files of assets/. Anything else is 404:
// there is no directory listing and no other file is reachable.
func NewHandler(fsys fs.FS) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", CSP)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Frame-Options", "DENY")

		rel, ok := strings.CutPrefix(r.URL.Path, Prefix)
		switch {
		case !ok:
			http.NotFound(w, r)
		case rel == "" || rel == "index.html":
			h.Set("Cache-Control", "no-cache")
			if !serveFile(w, r, fsys, "index.html") {
				h.Set("Content-Type", "text/html; charset=utf-8")
				_, _ = w.Write([]byte(notBuilt))
			}
		case strings.HasPrefix(rel, "assets/") && path.Clean(rel) == rel && !strings.HasSuffix(rel, "/"):
			// Built assets carry a content hash in their name.
			h.Set("Cache-Control", "public, max-age=31536000, immutable")
			if !serveFile(w, r, fsys, rel) {
				h.Del("Cache-Control")
				http.NotFound(w, r)
			}
		default:
			http.NotFound(w, r)
		}
	})
}

// serveFile serves name if it is a regular file of fsys.
func serveFile(w http.ResponseWriter, r *http.Request, fsys fs.FS, name string) bool {
	fi, err := fs.Stat(fsys, name)
	if err != nil || !fi.Mode().IsRegular() {
		return false
	}
	http.ServeFileFS(w, r, fsys, name)
	return true
}
