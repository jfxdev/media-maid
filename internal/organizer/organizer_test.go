package organizer

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jfxdev/media-maid/internal/assist"
	"github.com/jfxdev/media-maid/internal/integration"
	"github.com/jfxdev/media-maid/internal/jobs"
	"github.com/jfxdev/media-maid/internal/metadata"
	"github.com/jfxdev/media-maid/internal/safepath"
)

type fakeSyncer struct {
	n     atomic.Int32
	mu    sync.Mutex
	paths []string
}

func (f *fakeSyncer) Request(paths ...string) {
	f.n.Add(1)
	f.mu.Lock()
	f.paths = append(f.paths, paths...)
	f.mu.Unlock()
}
func (f *fakeSyncer) SyncNow(context.Context) error { return nil }

type fakeMeta struct{}

func (fakeMeta) Search(_ context.Context, kind metadata.Kind, title string, year int) ([]metadata.Result, error) {
	if kind != metadata.Movie {
		switch title {
		case "One Piece":
			return []metadata.Result{{Title: "One Piece", Year: 1999, TMDBID: 37854}}, nil
		case "The Office":
			return []metadata.Result{
				{Title: "The Office", Year: 2001, TMDBID: 2996, OriginCountry: "GB"},
				{Title: "The Office", Year: 2005, TMDBID: 2316, OriginCountry: "US"},
			}, nil
		}
		return nil, nil
	}
	switch title {
	case "Inception":
		return []metadata.Result{{Title: "Inception", Year: 2010, TMDBID: 27205}}, nil
	case "Evil":
		return []metadata.Result{{Title: "../../../../tmp/pwned", OriginalTitle: "Evil", Year: 2000, TMDBID: 1}}, nil
	case "Dune":
		return []metadata.Result{{Title: "Dune", Year: 1984, TMDBID: 841}, {Title: "Dune", Year: 2021, TMDBID: 438631}}, nil
	case "Spirited Away":
		return []metadata.Result{{Title: "Spirited Away", OriginalTitle: "Sen to Chihiro", Year: 2001, TMDBID: 129}}, nil
	case "Boom":
		return nil, errors.New("tmdb status 500")
	}
	return nil, nil
}

func (fakeMeta) Titles(_ context.Context, _ metadata.Kind, id int) ([]metadata.Title, error) {
	switch id {
	case 129:
		return []metadata.Title{{Language: "pt", Country: "BR", Title: "A Viagem de Chihiro", Source: "translation"}}, nil
	case 27205:
		return []metadata.Title{{Country: "BR", Title: "A Origem", Source: "alternative"}, {Country: "FR", Title: "Inception FR", Source: "alternative"}}, nil
	}
	return nil, nil
}

func (fakeMeta) Seasons(_ context.Context, id int) ([]metadata.Season, error) {
	if id == 37854 {
		return []metadata.Season{{Number: 1, Episodes: 61}, {Number: 2, Episodes: 16}}, nil
	}
	return nil, nil
}

func (fakeMeta) Lookup(_ context.Context, kind metadata.Kind, id int) (metadata.Result, error) {
	switch {
	case kind == metadata.Movie && id == 603:
		return metadata.Result{Title: "The Matrix", Year: 1999, TMDBID: 603}, nil
	case kind == metadata.Movie && id == 129:
		return metadata.Result{Title: "Spirited Away", OriginalTitle: "Sen to Chihiro", Year: 2001, TMDBID: 129}, nil
	case kind == metadata.Series && id == 1396:
		return metadata.Result{Title: "Breaking Bad", Year: 2008, TMDBID: 1396}, nil
	case id == 500:
		return metadata.Result{}, errors.New("tmdb status 500")
	}
	return metadata.Result{}, metadata.ErrNotFound
}

// fakeAssist is a scripted LLM layer.
type fakeAssist struct {
	decision  assist.Decision
	err       error
	localized string
	calls     []assist.Request
	locCalls  []assist.LocalizeRequest
}

func (f *fakeAssist) Resolve(_ context.Context, r assist.Request) (assist.Decision, error) {
	f.calls = append(f.calls, r)
	return f.decision, f.err
}

func (f *fakeAssist) Localize(_ context.Context, r assist.LocalizeRequest) (string, error) {
	f.locCalls = append(f.locCalls, r)
	return f.localized, nil
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

func newEnv(t *testing.T, mode string, opts ...func(*Options)) *env {
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
	o := Options{
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
	}
	for _, f := range opts {
		f(&o)
	}
	e.org = New(o)
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
	res, err := e.org.Organize(context.Background(), "Some.Film.1999.mkv", OrganizeOptions{Preview: true})
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
	res, _ = e.org.Organize(context.Background(), "Some.Film.1999.mkv", OrganizeOptions{})
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
	if _, err := e.org.Organize(context.Background(), "Secret.Movie.2001.mkv", OrganizeOptions{}); !errors.Is(err, safepath.ErrUnauthorizedVolume) {
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
	res, _ = e.org.Organize(context.Background(), "Evil.2000.mkv", OrganizeOptions{})
	if res[0].Status != jobs.StatusLinked || strings.Contains(res[0].Dest, "..") {
		t.Errorf("result = %+v", res[0])
	}
}

func TestInvalidInput(t *testing.T) {
	e := newEnv(t, "jellyfin")
	for _, p := range []string{"../media/movies", "/etc/passwd", "a\x00b", ""} {
		if _, err := e.org.Organize(context.Background(), p, OrganizeOptions{}); !errors.Is(err, safepath.ErrInvalidPath) {
			t.Errorf("%q: err = %v", p, err)
		}
	}
	if _, err := e.org.Organize(context.Background(), "missing.mkv", OrganizeOptions{}); !errors.Is(err, ErrNotFound) {
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

func TestDuplicateReleases(t *testing.T) {
	e := newEnv(t, "jellyfin")
	low := filepath.Join(e.downloads, "The.Matrix.1999.720p.BluRay.x264-A", "The.Matrix.1999.720p.BluRay.x264-A.mkv")
	high := filepath.Join(e.downloads, "The.Matrix.1999.1080p.WEB-DL.x264-B", "The.Matrix.1999.1080p.WEB-DL.x264-B.mkv")
	write(t, low)
	write(t, high)
	write(t, filepath.Join(filepath.Dir(high), "The.Matrix.1999.1080p.WEB-DL.x264-B.en.srt"))

	res, err := e.org.Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	st := statuses(res)
	if st[e.org.rel(high)] != jobs.StatusLinked || st[e.org.rel(low)] != jobs.StatusDuplicate {
		t.Fatalf("statuses = %v", st)
	}
	dest := filepath.Join(e.movies, "The Matrix (1999)", "The Matrix (1999).mkv")
	if !sameInode(t, high, dest) {
		t.Error("the 1080p release was not the one linked")
	}
	if _, err := os.Stat(filepath.Join(e.movies, "The Matrix (1999)", "The Matrix (1999).en.srt")); err != nil {
		t.Errorf("subtitle of the kept release: %v", err)
	}
}

func TestOverwrite(t *testing.T) {
	e := newEnv(t, "jellyfin")
	low := filepath.Join(e.downloads, "The.Matrix.1999.720p.mkv")
	high := filepath.Join(e.downloads, "The.Matrix.1999.1080p.mkv")
	dest := filepath.Join(e.movies, "The Matrix (1999)", "The Matrix (1999).mkv")
	write(t, low)
	if _, err := e.org.Organize(context.Background(), "The.Matrix.1999.720p.mkv", OrganizeOptions{}); err != nil {
		t.Fatal(err)
	}
	write(t, high)

	res, _ := e.org.Organize(context.Background(), "The.Matrix.1999.1080p.mkv", OrganizeOptions{})
	if res[0].Status != jobs.StatusConflict {
		t.Fatalf("without overwrite = %+v, want conflict", res[0])
	}
	res, _ = e.org.Organize(context.Background(), "The.Matrix.1999.1080p.mkv", OrganizeOptions{Preview: true, Overwrite: true})
	if res[0].Status != jobs.StatusPlanned || !res[0].Replaces {
		t.Fatalf("preview overwrite = %+v, want planned + replaces", res[0])
	}
	if !sameInode(t, low, dest) {
		t.Fatal("preview replaced the destination")
	}
	n := e.syncer.n.Load()
	res, _ = e.org.Organize(context.Background(), "The.Matrix.1999.1080p.mkv", OrganizeOptions{Overwrite: true})
	if res[0].Status != jobs.StatusReplaced {
		t.Fatalf("overwrite = %+v, want replaced", res[0])
	}
	if !sameInode(t, high, dest) {
		t.Error("destination is not the 1080p release")
	}
	if _, err := os.Stat(low); err != nil {
		t.Errorf("the replaced download must stay in place: %v", err)
	}
	if e.syncer.n.Load() == n {
		t.Error("replace did not request a library sync")
	}
}

// Regression: overwrite must stay confined to the library when a folder in
// the destination path is a symlink to somewhere else.
func TestOverwriteEscape(t *testing.T) {
	e := newEnv(t, "jellyfin")
	victim := filepath.Join(e.outside, "The Matrix (1999).mkv")
	write(t, victim)
	if err := os.Symlink(e.outside, filepath.Join(e.movies, "The Matrix (1999)")); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(e.downloads, "The.Matrix.1999.1080p.mkv"))
	res, _ := e.org.Organize(context.Background(), "The.Matrix.1999.1080p.mkv", OrganizeOptions{Overwrite: true})
	if res[0].Status != jobs.StatusError || res[0].Error != errUnauthorizedMsg {
		t.Fatalf("result = %+v, want unauthorized_volume error", res[0])
	}
	if b, _ := os.ReadFile(victim); string(b) != victim {
		t.Error("file outside the library was replaced")
	}
}

func withAssist(a *fakeAssist, lang string) func(*Options) {
	return func(o *Options) { o.Assist, o.MinConfidence, o.Language = a, 0.7, lang }
}

func organizeOne(t *testing.T, e *env, name string) Result {
	t.Helper()
	write(t, filepath.Join(e.downloads, name))
	res, err := e.org.Organize(context.Background(), name, OrganizeOptions{Preview: true})
	if err != nil || len(res) != 1 {
		t.Fatalf("Organize(%q) = %+v, %v", name, res, err)
	}
	return res[0]
}

func TestDecisionLayers(t *testing.T) {
	dune2021 := assist.Decision{Kind: "movie", Title: "Dune", Year: 2021, Choice: 1, Confidence: 0.9}
	tests := []struct {
		name     string
		file     string
		fa       *fakeAssist
		noMeta   bool
		wantDest string
		wantBy   string
		llm      bool
	}{
		{"confident, no llm call", "Inception.2010.1080p.BluRay.mkv", &fakeAssist{}, false,
			"movies/Inception (2010) [tmdbid-27205]/Inception (2010) [tmdbid-27205].mkv", ByTMDB, false},
		{"tie settled by llm", "Dune.1080p.mkv", &fakeAssist{decision: dune2021}, false,
			"movies/Dune (2021) [tmdbid-438631]/Dune (2021) [tmdbid-438631].mkv", ByLLM, true},
		// An unsure TMDB pick is not a decision: the file waits for a
		// manual identification instead of getting a possibly wrong ID.
		{"llm failure leaves tie pending", "Dune.1080p.mkv", &fakeAssist{err: errors.New("llm status 500")}, false,
			"", "", true},
		{"llm kind mismatch ignored", "Dune.1080p.mkv", &fakeAssist{decision: assist.Decision{Kind: "episode", Title: "Dune", Season: 1, Episodes: []int{1}, Choice: 1, Confidence: 0.9}}, false,
			"", "", true},
		{"llm title unknown to tmdb rejected", "Some.Obscure.Film.mkv", &fakeAssist{decision: assist.Decision{Kind: "movie", Title: "Invented", Choice: -1, Confidence: 0.9}}, false,
			"movies/Some Obscure Film/Some Obscure Film.mkv", ByParser, true},
		{"llm free title without tmdb", "Some.Obscure.Film.mkv", &fakeAssist{decision: assist.Decision{Kind: "movie", Title: "Some Obscure Film", Year: 0, Choice: -1, Confidence: 0.9}}, true,
			"movies/Some Obscure Film/Some Obscure Film.mkv", ByLLM, true},
		// A TMDB outage is not "not found": the file waits for a retry
		// instead of landing in a folder without the ID tag.
		{"tmdb outage waits", "Boom.2010.1080p.mkv", &fakeAssist{}, false,
			"", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t, "jellyfin", withAssist(tt.fa, ""), func(o *Options) {
				if tt.noMeta {
					o.Meta = nil
				}
			})
			r := organizeOne(t, e, tt.file)
			if r.Dest != tt.wantDest || r.ResolvedBy != tt.wantBy {
				t.Errorf("got %s by %s, want %s by %s", r.Dest, r.ResolvedBy, tt.wantDest, tt.wantBy)
			}
			if (len(tt.fa.calls) > 0) != tt.llm {
				t.Errorf("llm calls = %d, want called=%v", len(tt.fa.calls), tt.llm)
			}
		})
	}

	// Without the LLM layer a tie is pending, naming the candidates.
	e := newEnv(t, "jellyfin")
	r := organizeOne(t, e, "Dune.1080p.mkv")
	if r.Status != jobs.StatusSkipped || r.Error != "ambiguous title: Dune (1984) [tmdb 841], Dune (2021) [tmdb 438631]; identify it manually" {
		t.Errorf("no llm: %+v", r)
	}
	if _, err := e.org.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	if items := e.org.Pending(); len(items) != 1 || items[0].Source != "Dune.1080p.mkv" {
		t.Errorf("pending = %+v", items)
	}
	// A year settles it.
	if r := organizeOne(t, e, "Dune.2021.1080p.mkv"); r.Dest != "movies/Dune (2021) [tmdbid-438631]/Dune (2021) [tmdbid-438631].mkv" {
		t.Errorf("with year: %+v", r)
	}
	// AMBIGUOUS_MATCH=best keeps the previous behavior.
	e = newEnv(t, "jellyfin", func(o *Options) { o.AmbiguousBest = true })
	if r := organizeOne(t, e, "Dune.1080p.mkv"); r.Dest != "movies/Dune (1984) [tmdbid-841]/Dune (1984) [tmdbid-841].mkv" || r.ResolvedBy != ByTMDB {
		t.Errorf("best: %+v", r)
	}
	if r := organizeOne(t, e, "1080p.mkv"); r.Status != jobs.StatusSkipped {
		t.Errorf("unparseable without llm: %+v", r)
	}
}

func TestPreferredLanguage(t *testing.T) {
	fa := &fakeAssist{localized: "A Origem"}
	e := newEnv(t, "jellyfin", withAssist(fa, "pt-BR"))
	r := organizeOne(t, e, "Spirited.Away.2001.1080p.mkv")
	if r.Dest != "movies/A Viagem de Chihiro (2001) [tmdbid-129]/A Viagem de Chihiro (2001) [tmdbid-129].mkv" || r.TitleSource != TitleTMDBTranslation {
		t.Errorf("translation: %+v", r)
	}
	if len(fa.locCalls) != 0 {
		t.Error("the llm must not be asked when TMDB has a translation")
	}

	r = organizeOne(t, e, "Inception.2010.1080p.mkv")
	if r.Dest != "movies/A Origem (2010) [tmdbid-27205]/A Origem (2010) [tmdbid-27205].mkv" || r.TitleSource != TitleLLMLocalized {
		t.Errorf("llm localized: %+v", r)
	}
	if opts := fa.locCalls[0].Options; len(opts) != 1 || opts[0].Title != "A Origem" {
		t.Errorf("the llm must only see TMDB titles for the market: %+v", opts)
	}

	fa.localized = ""
	r = organizeOne(t, e, "Inception.2010.720p.mkv")
	if r.Dest != "movies/Inception (2010) [tmdbid-27205]/Inception (2010) [tmdbid-27205].mkv" || r.TitleSource != TitleOriginal {
		t.Errorf("fallback: %+v", r)
	}

	// TMDB is on but found nothing: the llm must not invent a localized title.
	fa.localized, fa.locCalls = "Inventado", nil
	r = organizeOne(t, e, "Some.Obscure.Film.2003.mkv")
	if r.TitleSource != TitleOriginal || !strings.Contains(r.Dest, "Some Obscure Film (2003)") || len(fa.locCalls) != 0 {
		t.Errorf("unconfirmed work: %+v, %d llm calls", r, len(fa.locCalls))
	}

	// Without the LLM layer, no translation means the original title.
	e = newEnv(t, "jellyfin", func(o *Options) { o.Language = "pt-BR" })
	if r := organizeOne(t, e, "Inception.2010.1080p.mkv"); r.TitleSource != TitleOriginal || !strings.Contains(r.Dest, "Inception (2010)") {
		t.Errorf("no llm: %+v", r)
	}
}

// TestLLMEndToEnd runs the real assist client against a fake
// OpenAI-compatible server through the organizer.
func TestLLMEndToEnd(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		answer := `{"kind":"movie","title":"Dune","year":2021,"season":0,"episodes":[],"choice":1,"confidence":0.9}`
		b, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]any{"content": answer}}}})
		w.Write(b)
	}))
	defer srv.Close()
	client, err := assist.New(assist.Options{Provider: assist.ProviderOpenAI, BaseURL: srv.URL, Model: "m", MinConfidence: 0.7})
	if err != nil {
		t.Fatal(err)
	}
	e := newEnv(t, "jellyfin", func(o *Options) { o.Assist, o.MinConfidence = client, 0.7 })
	r := organizeOne(t, e, "Dune.1080p.mkv")
	if r.Dest != "movies/Dune (2021) [tmdbid-438631]/Dune (2021) [tmdbid-438631].mkv" || r.ResolvedBy != ByLLM {
		t.Errorf("ambiguous name: %+v", r)
	}
	r = organizeOne(t, e, "Inception.2010.1080p.BluRay.mkv")
	if r.ResolvedBy != ByTMDB || calls.Load() != 1 {
		t.Errorf("confident name must not call the llm: %+v, %d calls", r, calls.Load())
	}
}

func TestReleaseShapes(t *testing.T) {
	e := newEnv(t, "plex")
	files := map[string]string{
		// Season pack whose files only carry the episode number.
		"Show.Name.S01.COMPLETE/Show.Name.E05.mkv": filepath.Join(e.tv, "Show Name", "Season 01", "Show Name S01E05.mkv"),
		// Multi-file movie: both parts land in one folder.
		"Old.Movie.1990.DVDRip/Old.Movie.1990.DVDRip.CD1.mkv": filepath.Join(e.movies, "Old Movie (1990)", "Old Movie (1990) - part1.mkv"),
		"Old.Movie.1990.DVDRip/Old.Movie.1990.DVDRip.CD2.mkv": filepath.Join(e.movies, "Old Movie (1990)", "Old Movie (1990) - part2.mkv"),
		// Edition: Plex tag, and TMDB still matches the bare title.
		"Inception.2010.Extended.Cut.1080p.mkv": filepath.Join(e.movies, "Inception (2010) {tmdb-27205} {edition-Extended Cut}", "Inception (2010) {tmdb-27205} {edition-Extended Cut}.mkv"),
		// Absolute anime numbering mapped through TMDB seasons (61 + 16).
		"[Grp] One Piece - 65 (1080p).mkv": filepath.Join(e.tv, "One Piece (1999) {tmdb-37854}", "Season 02", "One Piece S02E04.mkv"),
		// Origin marker picks the US remake.
		"The.Office.US.S02E03.720p.mkv": filepath.Join(e.tv, "The Office (2005) {tmdb-2316}", "Season 02", "The Office S02E03.mkv"),
	}
	for src := range files {
		write(t, filepath.Join(e.downloads, src))
	}
	res, err := e.org.Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for src, dst := range files {
		if _, err := os.Stat(dst); err != nil {
			t.Errorf("%s: %v (results %+v)", src, err, res)
			continue
		}
		if !sameInode(t, filepath.Join(e.downloads, src), dst) {
			t.Errorf("%s is not linked to %s", src, dst)
		}
	}
}

func TestUnidentifiedSameName(t *testing.T) {
	e := newEnv(t, "jellyfin")
	a := filepath.Join(e.downloads, "A", "Some.Film.2001.1080p.WEB-DL.mkv")
	b := filepath.Join(e.downloads, "B", "Some.Film.2001.1080p.WEB-DL.mkv")
	write(t, a)
	write(t, b)
	write(t, filepath.Join(e.downloads, "B", "Some.Film.2001.1080p.WEB-DL.en.srt"))
	res, err := e.org.Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	st := statuses(res)
	linked, conflict := 0, 0
	for _, s := range st {
		switch s {
		case jobs.StatusLinked:
			linked++
		case jobs.StatusConflict:
			conflict++
		}
	}
	if linked != 1 || conflict != 1 {
		t.Errorf("statuses = %v, want one linked video and one conflict", st)
	}
}

// countingMeta counts TMDB searches.
type countingMeta struct {
	fakeMeta
	n atomic.Int32
}

func (c *countingMeta) Search(ctx context.Context, kind metadata.Kind, title string, year int) ([]metadata.Result, error) {
	c.n.Add(1)
	return c.fakeMeta.Search(ctx, kind, title, year)
}

func TestLedger(t *testing.T) {
	meta := &countingMeta{}
	ledger := NewLedger()
	e := newEnv(t, "jellyfin", func(o *Options) { o.Meta = meta; o.Ledger = ledger })
	src := filepath.Join(e.downloads, "Inception.2010.1080p", "Inception.2010.1080p.mkv")
	write(t, src)
	dest := filepath.Join(e.movies, "Inception (2010) [tmdbid-27205]", "Inception (2010) [tmdbid-27205].mkv")

	if _, err := e.org.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !sameInode(t, src, dest) || meta.n.Load() != 1 {
		t.Fatalf("first scan: searches = %d", meta.n.Load())
	}
	e.syncer.mu.Lock()
	if len(e.syncer.paths) != 1 || e.syncer.paths[0] != filepath.Dir(dest) {
		t.Errorf("sync paths = %v, want the movie folder", e.syncer.paths)
	}
	e.syncer.mu.Unlock()

	// A restart with the saved ledger: nothing is identified again.
	data, err := ledger.Export()
	if err != nil {
		t.Fatal(err)
	}
	restored := NewLedger()
	if err := restored.Import(data); err != nil {
		t.Fatal(err)
	}
	meta2 := &countingMeta{}
	e2 := *e
	e2.org = New(Options{
		DownloadsDir: e.downloads, Movies: e.org.movies, TV: e.org.tv, Filter: e.org.filter, Guard: e.org.guard,
		Meta: meta2, Platform: e.org.platform, Syncer: e.syncer, History: e.history, Log: e.org.log, Ledger: restored,
	})
	res, err := e2.org.Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if meta2.n.Load() != 0 || len(res) != 1 || res[0].Status != jobs.StatusExists {
		t.Errorf("rescan: searches = %d, results %+v", meta2.n.Load(), res)
	}

	// The user deletes the movie from the library: automatic runs respect it.
	os.Remove(dest)
	res, _ = e2.org.Scan(context.Background())
	if len(res) != 1 || res[0].Status != jobs.StatusSkipped || res[0].Error != errRemovedMsg {
		t.Errorf("after deletion: %+v", res)
	}
	if _, err := os.Stat(dest); err == nil {
		t.Error("a deleted library entry was linked again by a scan")
	}
	// An explicit request links it again.
	res, _ = e2.org.Organize(context.Background(), src, OrganizeOptions{Explicit: true})
	if len(res) != 1 || res[0].Status != jobs.StatusLinked || !sameInode(t, src, dest) {
		t.Errorf("explicit organize: %+v", res)
	}

	// Entries for files gone from the downloads folder are pruned by a scan.
	os.RemoveAll(filepath.Dir(src))
	e2.org.Scan(context.Background())
	if data, _ := restored.Export(); string(data) != "{}" {
		t.Errorf("ledger not pruned: %s", data)
	}
}

func TestMinAge(t *testing.T) {
	e := newEnv(t, "jellyfin")
	fresh := filepath.Join(e.downloads, "Inception.2010.mkv")
	old := filepath.Join(e.downloads, "Dune.2021.mkv")
	write(t, fresh)
	write(t, old)
	past := time.Now().Add(-time.Hour)
	os.Chtimes(old, past, past)
	res, err := e.org.Organize(context.Background(), e.downloads, OrganizeOptions{MinAge: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0].Source != "Dune.2021.mkv" {
		t.Errorf("results = %+v, want only the settled file", res)
	}
}

// Subtitles matched by episode number still resolve when the videos come
// from the ledger on a later scan.
func TestLedgerKeepsSubtitleMatching(t *testing.T) {
	e := newEnv(t, "jellyfin")
	dir := filepath.Join(e.downloads, "Show.S01.1080p")
	write(t, filepath.Join(dir, "Show.S01E01.1080p.mkv"))
	write(t, filepath.Join(dir, "Show.S01E02.1080p.mkv"))
	write(t, filepath.Join(dir, "Subs", "S01E02.en.srt"))
	for run := 0; run < 2; run++ {
		res, err := e.org.Scan(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range res {
			if r.Status == jobs.StatusSkipped {
				t.Errorf("run %d: %+v", run, r)
			}
		}
	}
}

func TestFilterSkipsExtras(t *testing.T) {
	f := Filter{Video: map[string]bool{".mkv": true}, Subtitle: map[string]bool{".srt": true}}
	tests := map[string]bool{
		"Movie.2010/Movie.2010.mkv":                  true,
		"movies/Movie.2010/Movie.2010.mkv":           true,
		"other/Movie.2010/Movie.2010.mkv":            true, // "other" is a category, not junk
		"Movie.2010/Sample/sample.mkv":               false,
		"Movie.2010/Behind The Scenes/Making Of.mkv": false,
		"Movie.2010/Behind.The.Scenes/Making.Of.mkv": false,
		"Movie.2010/deleted_scenes/Scene 1.mkv":      false,
		"Movie.2010/Interviews/Director.mkv":         false,
		"Movie.2010/Shorts/Short.Film.2010.mkv":      false,
		"Movie.2010/Featurettes/Visual Effects.mkv":  false,
		"Movie.2010/Bonus/Gag Reel.mkv":              false,
		"Movie.2010/Clips/Clip.mkv":                  false,
		"Movie.2010/Scenes/Opening.mkv":              false,
		"Movie.2010/Movie.2010.mkv.part":             false,
	}
	for p, want := range tests {
		if got := f.IsVideo(filepath.FromSlash(p)); got != want {
			t.Errorf("IsVideo(%s) = %v, want %v", p, got, want)
		}
	}
}

// downMeta is fakeMeta with searches failing while down is set.
type downMeta struct {
	fakeMeta
	down *bool
}

func (m downMeta) Search(ctx context.Context, kind metadata.Kind, title string, year int) ([]metadata.Result, error) {
	if *m.down {
		return nil, errors.New("tmdb status 503")
	}
	return m.fakeMeta.Search(ctx, kind, title, year)
}

// A download seen while TMDB is unreachable stays pending and out of the
// library; RetryUnavailable places it with its ID once TMDB answers again.
func TestTMDBOutageRetried(t *testing.T) {
	down := true
	e := newEnv(t, "jellyfin", func(o *Options) { o.Meta = downMeta{down: &down} })
	write(t, filepath.Join(e.downloads, "Inception.2010.1080p.mkv"))
	res, err := e.org.Scan(context.Background())
	if err != nil || len(res) != 1 || res[0].Status != jobs.StatusSkipped || res[0].Error != errMetadataUnavailableMsg {
		t.Fatalf("outage = %+v, %v", res, err)
	}
	if entries, _ := os.ReadDir(e.movies); len(entries) != 0 {
		t.Fatalf("library written during the outage: %v", entries)
	}
	if items := e.org.Pending(); len(items) != 1 || items[0].Error != errMetadataUnavailableMsg {
		t.Fatalf("pending = %+v", items)
	}
	if n := e.org.RetryUnavailable(context.Background()); n != 1 {
		t.Fatalf("retried %d while down", n)
	}
	down = false
	if n := e.org.RetryUnavailable(context.Background()); n != 1 {
		t.Fatalf("retried %d", n)
	}
	want := filepath.Join(e.movies, "Inception (2010) [tmdbid-27205]", "Inception (2010) [tmdbid-27205].mkv")
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("not placed after retry: %v", err)
	}
	if items := e.org.Pending(); len(items) != 0 {
		t.Errorf("pending after retry = %+v", items)
	}
	if n := e.org.RetryUnavailable(context.Background()); n != 0 {
		t.Errorf("retried %d with nothing pending", n)
	}
}
