package organizer

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jfxdev/media-maid/internal/integration"
	"github.com/jfxdev/media-maid/internal/jobs"
	"github.com/jfxdev/media-maid/internal/metadata"
	"github.com/jfxdev/media-maid/internal/safepath"
)

type fakeSyncer struct{ n atomic.Int32 }

func (f *fakeSyncer) Request()                      { f.n.Add(1) }
func (f *fakeSyncer) SyncNow(context.Context) error { return nil }

type fakeMeta struct{}

func (fakeMeta) LookupMovie(_ context.Context, title string, year int) (metadata.Result, bool) {
	if title == "Inception" {
		return metadata.Result{Title: "Inception", Year: 2010, TMDBID: 27205}, true
	}
	if title == "Evil" {
		return metadata.Result{Title: "../../../../tmp/pwned", Year: 2000, TMDBID: 1}, true
	}
	return metadata.Result{}, false
}

func (fakeMeta) LookupSeries(context.Context, string, int) (metadata.Result, bool) {
	return metadata.Result{}, false
}

type env struct {
	org       *Organizer
	downloads string
	movies    string
	tv        string
	outside   string
	otherVol  string
	syncer    *fakeSyncer
	history   *jobs.History
}

func newEnv(t *testing.T, mode string) *env {
	t.Helper()
	vol := t.TempDir()
	otherVol := t.TempDir()
	outside := t.TempDir()
	e := &env{
		downloads: filepath.Join(vol, "downloads"),
		movies:    filepath.Join(vol, "media", "movies"),
		tv:        filepath.Join(vol, "media", "tv"),
		outside:   outside,
		otherVol:  otherVol,
		syncer:    &fakeSyncer{},
		history:   jobs.NewHistory(100),
	}
	for _, d := range []string{e.downloads, e.movies, e.tv} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	g, err := safepath.NewGuard([]string{vol, otherVol})
	if err != nil {
		t.Fatal(err)
	}
	mr, _ := os.OpenRoot(e.movies)
	tr, _ := os.OpenRoot(e.tv)
	t.Cleanup(func() { mr.Close(); tr.Close() })
	spec, _ := integration.Lookup(mode)
	e.org = New(Options{
		DownloadsDir: e.downloads,
		Movies:       Library{Name: "movies", Path: e.movies, Root: mr},
		TV:           Library{Name: "tv", Path: e.tv, Root: tr},
		Filter: Filter{
			Video:    map[string]bool{".mkv": true, ".mp4": true},
			Subtitle: map[string]bool{".srt": true},
		},
		Guard:    g,
		Meta:     fakeMeta{},
		Platform: spec,
		Syncer:   e.syncer,
		History:  e.history,
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	return e
}

func write(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(p), 0o644); err != nil {
		t.Fatal(err)
	}
}

func sameInode(t *testing.T, a, b string) bool {
	t.Helper()
	fa, err := os.Stat(a)
	if err != nil {
		t.Fatalf("stat %s: %v", a, err)
	}
	fb, err := os.Stat(b)
	if err != nil {
		t.Fatalf("stat %s: %v", b, err)
	}
	return os.SameFile(fa, fb)
}

func statuses(rs []Result) map[string]string {
	m := map[string]string{}
	for _, r := range rs {
		m[r.Source] = r.Status
	}
	return m
}

func TestOrganizeTree(t *testing.T) {
	e := newEnv(t, "jellyfin")
	rel := "Inception.2010.1080p.BluRay.x264-GRP"
	write(t, filepath.Join(e.downloads, rel, "Inception.2010.1080p.BluRay.x264-GRP.mkv"))
	write(t, filepath.Join(e.downloads, rel, "Subs", "2_Portuguese.srt"))
	write(t, filepath.Join(e.downloads, rel, "Inception.2010.1080p.BluRay.x264-GRP.en.forced.srt"))
	write(t, filepath.Join(e.downloads, rel, "Sample", "sample.mkv"))
	write(t, filepath.Join(e.downloads, rel, "info.nfo"))
	write(t, filepath.Join(e.downloads, rel, "virus.exe"))
	show := "Breaking.Bad.S01.720p"
	write(t, filepath.Join(e.downloads, show, "Breaking.Bad.S01E01.720p.mkv"))
	write(t, filepath.Join(e.downloads, show, "Breaking.Bad.S01E02E03.720p.mkv"))
	write(t, filepath.Join(e.downloads, show, "Breaking.Bad.S01E02E03.720p.pt-br.srt"))

	res, err := e.org.Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	movieDir := filepath.Join(e.movies, "Inception (2010) [tmdbid-27205]")
	checks := map[string]string{
		filepath.Join(e.downloads, rel, "Inception.2010.1080p.BluRay.x264-GRP.mkv"):           filepath.Join(movieDir, "Inception (2010) [tmdbid-27205].mkv"),
		filepath.Join(e.downloads, rel, "Subs", "2_Portuguese.srt"):                           filepath.Join(movieDir, "Inception (2010) [tmdbid-27205].pt.srt"),
		filepath.Join(e.downloads, rel, "Inception.2010.1080p.BluRay.x264-GRP.en.forced.srt"): filepath.Join(movieDir, "Inception (2010) [tmdbid-27205].en.forced.srt"),
		filepath.Join(e.downloads, show, "Breaking.Bad.S01E01.720p.mkv"):                      filepath.Join(e.tv, "Breaking Bad", "Season 01", "Breaking Bad S01E01.mkv"),
		filepath.Join(e.downloads, show, "Breaking.Bad.S01E02E03.720p.mkv"):                   filepath.Join(e.tv, "Breaking Bad", "Season 01", "Breaking Bad S01E02-E03.mkv"),
		filepath.Join(e.downloads, show, "Breaking.Bad.S01E02E03.720p.pt-br.srt"):             filepath.Join(e.tv, "Breaking Bad", "Season 01", "Breaking Bad S01E02-E03.pt-BR.srt"),
	}
	for src, dst := range checks {
		if !sameInode(t, src, dst) {
			t.Errorf("%s is not hardlinked to %s", src, dst)
		}
	}
	for _, r := range res {
		if strings.Contains(r.Source, "sample") || strings.Contains(r.Source, ".nfo") || strings.Contains(r.Source, ".exe") {
			t.Errorf("junk processed: %+v", r)
		}
	}
	if len(res) != len(checks) {
		t.Errorf("got %d results, want %d: %+v", len(res), len(checks), res)
	}
	if e.syncer.n.Load() != 1 {
		t.Errorf("sync requests = %d, want 1", e.syncer.n.Load())
	}

	// Idempotent: a second run links nothing and does not sync again.
	res, err = e.org.Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range res {
		if r.Status != jobs.StatusExists {
			t.Errorf("second run: %+v", r)
		}
	}
	if e.syncer.n.Load() != 1 {
		t.Error("second run requested a sync")
	}
}

func TestIDTagPerPlatform(t *testing.T) {
	for mode, dir := range map[string]string{
		"jellyfin": "Inception (2010) [tmdbid-27205]",
		"emby":     "Inception (2010) [tmdbid=27205]",
		"plex":     "Inception (2010) {tmdb-27205}",
	} {
		e := newEnv(t, mode)
		write(t, filepath.Join(e.downloads, "Inception.2010.mkv"))
		if _, err := e.org.Scan(context.Background()); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(e.movies, dir, dir+".mkv")); err != nil {
			t.Errorf("%s: %v", mode, err)
		}
	}
}

func TestPreviewAndConflict(t *testing.T) {
	e := newEnv(t, "jellyfin")
	write(t, filepath.Join(e.downloads, "Some.Film.1999.mkv"))
	res, err := e.org.Organize(context.Background(), "Some.Film.1999.mkv", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0].Status != jobs.StatusPlanned || res[0].Dest != "movies/Some Film (1999)/Some Film (1999).mkv" {
		t.Fatalf("preview = %+v", res)
	}
	if _, err := os.Stat(filepath.Join(e.movies, "Some Film (1999)")); !os.IsNotExist(err) {
		t.Error("preview wrote to the library")
	}
	if entries, _ := e.history.Snapshot(); len(entries) != 0 {
		t.Error("preview recorded history")
	}

	write(t, filepath.Join(e.movies, "Some Film (1999)", "Some Film (1999).mkv"))
	res, _ = e.org.Organize(context.Background(), "Some.Film.1999.mkv", false)
	if res[0].Status != jobs.StatusConflict {
		t.Errorf("status = %s, want conflict", res[0].Status)
	}
}

func TestSymlinks(t *testing.T) {
	e := newEnv(t, "jellyfin")
	secret := filepath.Join(e.outside, "Secret.Movie.2001.mkv")
	write(t, secret)
	os.Symlink(secret, filepath.Join(e.downloads, "Secret.Movie.2001.mkv"))
	os.Symlink("/etc/passwd", filepath.Join(e.downloads, "Passwd.Movie.2002.mkv"))

	other := filepath.Join(e.otherVol, "Other.Movie.2003.mkv")
	write(t, other)
	os.Symlink(other, filepath.Join(e.downloads, "Other.Movie.2003.mkv"))

	internal := filepath.Join(e.downloads, ".store", "Internal.Movie.2004.mkv")
	write(t, internal)
	os.Symlink(internal, filepath.Join(e.downloads, "Internal.Movie.2004.mkv"))

	// Direct request for a link to an unauthorized volume → error.
	if _, err := e.org.Organize(context.Background(), "Secret.Movie.2001.mkv", false); !errors.Is(err, safepath.ErrUnauthorizedVolume) {
		t.Errorf("err = %v, want ErrUnauthorizedVolume", err)
	}

	res, err := e.org.Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	st := statuses(res)
	for _, name := range []string{"Secret.Movie.2001.mkv", "Passwd.Movie.2002.mkv"} {
		if st[name] != jobs.StatusError {
			t.Errorf("%s status = %q, want error", name, st[name])
		}
	}
	for _, r := range res {
		if r.Status == jobs.StatusError && (r.Error != "unauthorized_volume" || strings.Contains(r.Error, e.outside)) {
			t.Errorf("unexpected error detail: %+v", r)
		}
	}
	if st["Other.Movie.2003.mkv"] != jobs.StatusLinked {
		t.Errorf("link to other mounted volume: %q", st["Other.Movie.2003.mkv"])
	}
	if st["Internal.Movie.2004.mkv"] != jobs.StatusLinked {
		t.Errorf("internal link: %q", st["Internal.Movie.2004.mkv"])
	}
	if !sameInode(t, other, filepath.Join(e.movies, "Other Movie (2003)", "Other Movie (2003).mkv")) {
		t.Error("other volume file not linked")
	}
	if _, err := os.Stat(filepath.Join(e.movies, "Secret Movie (2001)")); !os.IsNotExist(err) {
		t.Error("unauthorized file reached the library")
	}
	entries, _ := e.history.Snapshot()
	found := false
	for _, en := range entries {
		if en.Status == jobs.StatusError && en.Error == "unauthorized_volume" {
			found = true
		}
	}
	if !found {
		t.Error("unauthorized link not recorded in history")
	}
}

func TestDestinationEscape(t *testing.T) {
	e := newEnv(t, "jellyfin")
	// A folder inside the library that points outside must not be written through.
	os.Symlink(e.outside, filepath.Join(e.movies, "Escape Movie (2005)"))
	write(t, filepath.Join(e.downloads, "Escape.Movie.2005.mkv"))
	res, err := e.org.Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res[0].Status != jobs.StatusError || res[0].Error != "unauthorized_volume" {
		t.Errorf("result = %+v", res[0])
	}
	if entries, _ := os.ReadDir(e.outside); len(entries) != 0 {
		t.Error("file written outside the library")
	}

	// Malicious metadata cannot escape either.
	write(t, filepath.Join(e.downloads, "Evil.2000.mkv"))
	res, _ = e.org.Organize(context.Background(), "Evil.2000.mkv", false)
	if res[0].Status != jobs.StatusLinked || strings.Contains(res[0].Dest, "..") {
		t.Errorf("result = %+v", res[0])
	}
}

func TestInvalidInput(t *testing.T) {
	e := newEnv(t, "jellyfin")
	for _, p := range []string{"../media/movies", "/etc/passwd", "a\x00b", ""} {
		if _, err := e.org.Organize(context.Background(), p, false); !errors.Is(err, safepath.ErrInvalidPath) {
			t.Errorf("%q: err = %v", p, err)
		}
	}
	if _, err := e.org.Organize(context.Background(), "missing.mkv", false); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v", err)
	}
}

func TestExtensionAllowlist(t *testing.T) {
	e := newEnv(t, "jellyfin")
	write(t, filepath.Join(e.downloads, "Some.Movie.2010.avi"))
	write(t, filepath.Join(e.downloads, "Some.Movie.2010.mkv.part"))
	res, err := e.org.Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 0 {
		t.Errorf("non-allowlisted extensions processed: %+v", res)
	}
}
