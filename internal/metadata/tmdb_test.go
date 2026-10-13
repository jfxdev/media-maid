package metadata

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestTMDB(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		q := r.URL.Query()
		switch {
		case r.URL.Path == "/search/movie" && q.Get("query") == "inception" && q.Get("api_key") == "k" && q.Get("year") == "2010":
			io.WriteString(w, `{"results":[{"id":27205,"title":"Inception","original_title":"Inception","release_date":"2010-07-15","vote_count":37000},{"id":1,"title":"Inception: The Cobol Job","release_date":"2010-12-07"},{"id":0,"title":"no id"}]}`)
		case r.URL.Path == "/search/tv" && r.Header.Get("Authorization") == "Bearer eyJtoken":
			io.WriteString(w, `{"results":[{"id":1396,"name":"Breaking Bad","original_name":"Breaking Bad","first_air_date":"2008-01-20"}]}`)
		case r.URL.Path == "/movie/129" && q.Get("append_to_response") == "translations,alternative_titles":
			io.WriteString(w, `{"translations":{"translations":[{"iso_639_1":"pt","iso_3166_1":"BR","data":{"title":"A Viagem de Chihiro"}},{"iso_639_1":"pt","iso_3166_1":"PT","data":{"title":"A Viagem de Chihiro (PT)"}},{"iso_639_1":"de","iso_3166_1":"DE","data":{"title":""}}]},"alternative_titles":{"titles":[{"iso_3166_1":"BR","title":"Chihiro"}]}}`)
		case r.URL.Path == "/tv/1396":
			io.WriteString(w, `{"translations":{"translations":[{"iso_639_1":"es","iso_3166_1":"ES","data":{"name":"Breaking Bad ES"}}]},"alternative_titles":{"results":[{"iso_3166_1":"MX","title":"Hacerse Malo"}]}}`)
		case r.URL.Path == "/search/movie" && q.Get("query") == "boom":
			w.WriteHeader(500)
		case r.URL.Path == "/search/movie" && q.Get("query") == "garbage":
			io.WriteString(w, `{`)
		default:
			io.WriteString(w, `{"results":[]}`)
		}
	}))
	defer srv.Close()
	ctx := context.Background()

	m := NewTMDB("k", srv.URL)
	res, err := m.Search(ctx, Movie, "inception", 2010)
	if err != nil || len(res) != 2 || res[0] != (Result{Title: "Inception", OriginalTitle: "Inception", Year: 2010, TMDBID: 27205, VoteCount: 37000}) {
		t.Errorf("movie = %+v %v", res, err)
	}
	m.Search(ctx, Movie, "inception", 2010)
	if calls.Load() != 1 {
		t.Errorf("cache miss: %d calls", calls.Load())
	}
	if res, err := m.Search(ctx, Movie, "nothing", 0); err != nil || len(res) != 0 {
		t.Errorf("expected no result, got %v %v", res, err)
	}
	if _, err := m.Search(ctx, Movie, "boom", 0); err == nil {
		t.Error("server error must be reported")
	}
	if _, err := m.Search(ctx, Movie, "garbage", 0); err == nil {
		t.Error("invalid json must be reported")
	}

	tv := NewTMDB("eyJtoken", srv.URL)
	res, err = tv.Search(ctx, Series, "breaking bad", 0)
	if err != nil || len(res) != 1 || res[0].Title != "Breaking Bad" || res[0].Year != 2008 || res[0].TMDBID != 1396 {
		t.Errorf("series = %+v %v", res, err)
	}

	titles, err := m.Titles(ctx, Movie, 129)
	if err != nil || len(titles) != 3 {
		t.Fatalf("titles = %+v %v", titles, err)
	}
	if got, ok := Localized(titles, "pt-BR"); !ok || got != "A Viagem de Chihiro" {
		t.Errorf("pt-BR = %q %v", got, ok)
	}
	if got, ok := Localized(titles, "pt"); !ok || got != "A Viagem de Chihiro" {
		t.Errorf("pt = %q %v", got, ok)
	}
	if _, ok := Localized(titles, "de-DE"); ok {
		t.Error("empty translations must be ignored")
	}
	stitles, _ := tv.Titles(ctx, Series, 1396)
	if len(stitles) != 2 || stitles[1] != (Title{Country: "MX", Title: "Hacerse Malo", Source: "alternative"}) {
		t.Errorf("series titles = %+v", stitles)
	}
	if _, ok := Localized(stitles, "es-MX"); ok {
		t.Error("a translation for another country must not be used")
	}
}

func TestPick(t *testing.T) {
	cands := []Result{
		{Title: "Dune", Year: 1984, TMDBID: 1},
		{Title: "Dune", Year: 2021, TMDBID: 2},
		{Title: "Dune: Part Two", Year: 2024, TMDBID: 3},
	}
	tests := []struct {
		title    string
		year     int
		wantID   int
		minScore float64
		tie      bool
	}{
		{"Dune", 2021, 2, 1, false},
		{"Dune", 1984, 1, 1, false},
		{"Dune", 0, 1, 1, true},
		{"Dune Part Two", 2024, 3, 1, false},
	}
	for _, tt := range tests {
		best, score, margin, ok := Pick(tt.title, tt.year, cands)
		if !ok || best.TMDBID != tt.wantID || score < tt.minScore || (margin < MinMargin) != tt.tie {
			t.Errorf("Pick(%q, %d) = %d score %.2f margin %.2f", tt.title, tt.year, best.TMDBID, score, margin)
		}
	}
	if _, _, _, ok := Pick("x", 0, nil); ok {
		t.Error("no candidates must not pick")
	}

	scores := []struct {
		title string
		r     Result
		min   float64
		max   float64
	}{
		{"Marvels Agents of SHIELD", Result{Title: "Marvel's Agents of S.H.I.E.L.D."}, 1, 1},
		{"La Casa de Papel", Result{Title: "Money Heist", OriginalTitle: "La casa de papel"}, 1, 1},
		{"Amelie", Result{Title: "Amélie"}, 1, 1},
		{"Fast and Furious", Result{Title: "Fast & Furious"}, 1, 1},
		{"Some Movie", Result{Title: "Completely Different"}, 0, 0.1},
		{"Inception", Result{Title: "Inception", Year: 2010}, 1, 1},
	}
	for _, s := range scores {
		if got := Score(s.title, 0, s.r); got < s.min || got > s.max {
			t.Errorf("Score(%q, %q) = %.2f", s.title, s.r.Title, got)
		}
	}
	if Score("Inception", 2010, Result{Title: "Inception", Year: 2001}) >= Score("Inception", 2010, Result{Title: "Inception", Year: 2010}) {
		t.Error("a wrong year must lower the score")
	}
}

func TestPickIn(t *testing.T) {
	cands := []Result{
		{Title: "The Office", Year: 2001, TMDBID: 2996, OriginCountry: "GB"},
		{Title: "The Office", Year: 2005, TMDBID: 2316, OriginCountry: "US"},
	}
	if best, _, margin, _ := PickIn("The Office", 0, "US", cands); best.TMDBID != 2316 || margin < MinMargin {
		t.Errorf("US = %d margin %.2f", best.TMDBID, margin)
	}
	if best, _, _, _ := PickIn("The Office", 0, "UK", cands); best.TMDBID != 2996 {
		t.Errorf("UK = %d", best.TMDBID)
	}
	if _, _, margin, _ := Pick("The Office", 0, cands); margin >= MinMargin {
		t.Errorf("without a country the pick must stay a tie, margin %.2f", margin)
	}
}

// A tie is settled by popularity only when one candidate clearly dominates.
func TestPickPopularity(t *testing.T) {
	tests := []struct {
		name   string
		votes  [2]int
		wantID int
		tie    bool
	}{
		{"dominant namesake", [2]int{40, 2500}, 2, false},
		{"comparable entries stay a tie", [2]int{3000, 12000}, 1, true},
		{"too few votes stay a tie", [2]int{0, 90}, 1, true},
		{"exactly ten times", [2]int{100, 1000}, 2, false},
	}
	for _, tt := range tests {
		cands := []Result{
			{Title: "Shameless", Year: 2004, TMDBID: 1, VoteCount: tt.votes[0]},
			{Title: "Shameless", Year: 2011, TMDBID: 2, VoteCount: tt.votes[1]},
		}
		best, score, margin, ok := Pick("Shameless", 0, cands)
		if !ok || best.TMDBID != tt.wantID || score != 1 || (margin < MinMargin) != tt.tie {
			t.Errorf("%s: picked %d score %.2f margin %.2f", tt.name, best.TMDBID, score, margin)
		}
	}
	// A clear score lead is never overturned by votes.
	cands := []Result{{Title: "Dune", Year: 1984, TMDBID: 1, VoteCount: 10}, {Title: "Dune", Year: 2021, TMDBID: 2, VoteCount: 12000}}
	if best, _, _, _ := Pick("Dune", 1984, cands); best.TMDBID != 1 {
		t.Errorf("year match lost to votes: %d", best.TMDBID)
	}
}

func TestAbsoluteToSeason(t *testing.T) {
	seasons := []Season{{1, 28}, {2, 10}}
	tests := []struct {
		abs, season, ep int
		ok              bool
	}{
		{1, 1, 1, true}, {28, 1, 28, true}, {29, 2, 1, true}, {38, 2, 10, true}, {39, 0, 0, false}, {0, 0, 0, false},
	}
	for _, tt := range tests {
		s, e, ok := AbsoluteToSeason(seasons, tt.abs)
		if s != tt.season || e != tt.ep || ok != tt.ok {
			t.Errorf("AbsoluteToSeason(%d) = %d, %d, %v", tt.abs, s, e, ok)
		}
	}
}

func TestTMDBCacheExpiryAndExport(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		switch r.URL.Path {
		case "/tv/1":
			io.WriteString(w, `{"seasons":[{"season_number":0,"episode_count":3},{"season_number":2,"episode_count":12},{"season_number":1,"episode_count":28}]}`)
		default:
			io.WriteString(w, `{"results":[]}`)
		}
	}))
	defer srv.Close()
	ctx := context.Background()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	m := NewTMDB("k", srv.URL)
	m.now = func() time.Time { return now }
	changes := 0
	m.OnChange = func() { changes++ }

	seasons, err := m.Seasons(ctx, 1)
	if err != nil || len(seasons) != 2 || seasons[0] != (Season{1, 28}) {
		t.Fatalf("seasons = %+v %v", seasons, err)
	}
	m.Search(ctx, Movie, "new release", 2026)
	m.Search(ctx, Movie, "new release", 2026)
	if calls.Load() != 2 || changes != 2 {
		t.Fatalf("calls = %d changes = %d, want 2 and 2", calls.Load(), changes)
	}
	// An empty result expires after a day, so a later TMDB entry is found.
	now = now.Add(25 * time.Hour)
	m.Search(ctx, Movie, "new release", 2026)
	if calls.Load() != 3 {
		t.Errorf("negative result not refreshed: %d calls", calls.Load())
	}

	data, err := m.Export()
	if err != nil || strings.Contains(string(data), `"k"`) {
		t.Fatalf("export = %s %v", data, err)
	}
	m2 := NewTMDB("k", srv.URL)
	m2.now = func() time.Time { return now }
	if err := m2.Import(data); err != nil {
		t.Fatal(err)
	}
	m2.Search(ctx, Movie, "new release", 2026)
	m2.Seasons(ctx, 1) // expired (seasons live a day): fetched again
	if calls.Load() != 4 {
		t.Errorf("imported cache: %d calls, want 4", calls.Load())
	}
}

func TestTMDBLookup(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		switch r.URL.Path {
		case "/movie/603":
			io.WriteString(w, `{"id":603,"title":"The Matrix","original_title":"The Matrix","release_date":"1999-03-30"}`)
		case "/tv/1396":
			io.WriteString(w, `{"id":1396,"name":"Breaking Bad","original_name":"Breaking Bad","first_air_date":"2008-01-20","origin_country":["US"]}`)
		default:
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, `{"status_code":34}`)
		}
	}))
	defer srv.Close()
	ctx := context.Background()
	m := NewTMDB("k", srv.URL)
	if r, err := m.Lookup(ctx, Movie, 603); err != nil || r != (Result{Title: "The Matrix", OriginalTitle: "The Matrix", Year: 1999, TMDBID: 603}) {
		t.Errorf("movie = %+v, %v", r, err)
	}
	m.Lookup(ctx, Movie, 603)
	if calls.Load() != 1 {
		t.Errorf("lookup not cached: %d calls", calls.Load())
	}
	if r, err := m.Lookup(ctx, Series, 1396); err != nil || r.Title != "Breaking Bad" || r.Year != 2008 || r.OriginCountry != "US" {
		t.Errorf("series = %+v, %v", r, err)
	}
	for _, id := range []int{7, 0, -1} {
		if _, err := m.Lookup(ctx, Movie, id); !errors.Is(err, ErrNotFound) {
			t.Errorf("Lookup(%d) err = %v, want ErrNotFound", id, err)
		}
	}
	// The cache survives a restart.
	data, _ := m.Export()
	m2 := NewTMDB("k", srv.URL)
	m2.Import(data)
	before := calls.Load()
	if _, err := m2.Lookup(ctx, Movie, 603); err != nil || calls.Load() != before {
		t.Errorf("imported lookup: %v, calls %d -> %d", err, before, calls.Load())
	}
}

// A 429 is retried after Retry-After; a persistent one fails without
// looping forever.
func TestTMDBRateLimit(t *testing.T) {
	var calls atomic.Int32
	var limitFor atomic.Int32
	limitFor.Store(2)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) <= limitFor.Load() {
			w.Header().Set("Retry-After", "3")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		io.WriteString(w, `{"results":[{"id":27205,"title":"Inception","release_date":"2010-07-15"}]}`)
	}))
	defer srv.Close()
	m := NewTMDB("k", srv.URL)
	var waits []time.Duration
	m.wait = func(_ context.Context, d time.Duration) error { waits = append(waits, d); return nil }
	res, err := m.Search(context.Background(), Movie, "inception", 0)
	if err != nil || len(res) != 1 || len(waits) != 2 || waits[0] != 3*time.Second {
		t.Errorf("search = %+v, %v, waits %v", res, err, waits)
	}

	calls.Store(0)
	limitFor.Store(100)
	waits = nil
	if _, err := m.Search(context.Background(), Movie, "other", 0); err == nil || !strings.Contains(err.Error(), "429") {
		t.Errorf("persistent 429: %v", err)
	}
	if len(waits) != maxRetries {
		t.Errorf("retries = %d, want %d", len(waits), maxRetries)
	}

	for in, want := range map[string]time.Duration{"": time.Second, "0": 0, "120": maxRetryWait, "soon": time.Second} {
		if got := retryAfter(in, 0); got != want {
			t.Errorf("retryAfter(%q) = %v, want %v", in, got, want)
		}
	}
	if got := retryAfter("", 2); got != 4*time.Second {
		t.Errorf("backoff = %v", got)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := sleepCtx(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Errorf("sleepCtx = %v", err)
	}
}
