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
	"sync/atomic"
	"testing"

	"github.com/jfxdev/media-maid/internal/assist"
	"github.com/jfxdev/media-maid/internal/integration"
	"github.com/jfxdev/media-maid/internal/jobs"
	"github.com/jfxdev/media-maid/internal/metadata"
	"github.com/jfxdev/media-maid/internal/safepath"
)

type fakeSyncer struct{ n atomic.Int32 }

func (f *fakeSyncer) Request()                      { f.n.Add(1) }
func (f *fakeSyncer) SyncNow(context.Context) error { return nil }

type fakeMeta struct{}

func (fakeMeta) Search(_ context.Context, kind metadata.Kind, title string, year int) ([]metadata.Result, error) {
	if kind != metadata.Movie {
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
		{"llm failure keeps tmdb pick", "Dune.1080p.mkv", &fakeAssist{err: errors.New("llm status 500")}, false,
			"movies/Dune (1984) [tmdbid-841]/Dune (1984) [tmdbid-841].mkv", ByTMDB, true},
		{"llm kind mismatch ignored", "Dune.1080p.mkv", &fakeAssist{decision: assist.Decision{Kind: "episode", Title: "Dune", Season: 1, Episodes: []int{1}, Choice: 1, Confidence: 0.9}}, false,
			"movies/Dune (1984) [tmdbid-841]/Dune (1984) [tmdbid-841].mkv", ByTMDB, true},
		{"llm title unknown to tmdb rejected", "Some.Obscure.Film.mkv", &fakeAssist{decision: assist.Decision{Kind: "movie", Title: "Invented", Choice: -1, Confidence: 0.9}}, false,
			"movies/Some Obscure Film/Some Obscure Film.mkv", ByParser, true},
		{"llm free title without tmdb", "Some.Obscure.Film.mkv", &fakeAssist{decision: assist.Decision{Kind: "movie", Title: "Some Obscure Film", Year: 0, Choice: -1, Confidence: 0.9}}, true,
			"movies/Some Obscure Film/Some Obscure Film.mkv", ByLLM, true},
		{"tmdb outage keeps parser", "Boom.2010.1080p.mkv", &fakeAssist{}, false,
			"movies/Boom (2010)/Boom (2010).mkv", ByParser, false},
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

	// Without the LLM layer the pipeline behaves as before.
	e := newEnv(t, "jellyfin")
	if r := organizeOne(t, e, "Dune.1080p.mkv"); r.Dest != "movies/Dune (1984) [tmdbid-841]/Dune (1984) [tmdbid-841].mkv" || r.ResolvedBy != ByTMDB {
		t.Errorf("no llm: %+v", r)
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
