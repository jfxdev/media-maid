// Package metadata optionally enriches parsed media with canonical data.
package metadata

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Kind selects movies or series.
type Kind string

const (
	Movie  Kind = "movie"
	Series Kind = "tv"
)

// Result is canonical information about a movie or series.
type Result struct {
	Title         string `json:"title"`
	OriginalTitle string `json:"original_title,omitempty"`
	Year          int    `json:"year,omitempty"`
	TMDBID        int    `json:"tmdb_id"`
	// OriginCountry is the first ISO 3166-1 origin country of a series.
	OriginCountry string `json:"origin_country,omitempty"`
	// VoteCount is the number of TMDB votes, a popularity hint that settles
	// clear-cut ties (see PickIn).
	VoteCount int `json:"vote_count,omitempty"`
}

// Season is the episode count of one season of a series.
type Season struct {
	Number   int `json:"number"`
	Episodes int `json:"episodes"`
}

// Title is an official title of an entry in some language or country.
type Title struct {
	Language string `json:"language,omitempty"` // ISO 639-1, e.g. "pt"
	Country  string `json:"country,omitempty"`  // ISO 3166-1, e.g. "BR"
	Title    string `json:"title"`
	// Source is "translation" or "alternative".
	Source string `json:"source"`
}

// Provider looks up canonical titles.
type Provider interface {
	// Search returns up to MaxCandidates results in relevance order.
	Search(ctx context.Context, kind Kind, title string, year int) ([]Result, error)
	// Titles returns the translations and alternative titles of an entry.
	Titles(ctx context.Context, kind Kind, id int) ([]Title, error)
	// Seasons returns the regular seasons (no specials) of a series in order.
	Seasons(ctx context.Context, id int) ([]Season, error)
	// Lookup returns the entry with a TMDB id, or ErrNotFound.
	Lookup(ctx context.Context, kind Kind, id int) (Result, error)
}

// ErrNotFound is returned by Lookup when TMDB has no entry with that id.
var ErrNotFound = errors.New("tmdb: not found")

// MaxCandidates bounds how many search results are considered.
const MaxCandidates = 5

// Noop is used when no metadata provider is configured.
type Noop struct{}

func (Noop) Search(context.Context, Kind, string, int) ([]Result, error) { return nil, nil }
func (Noop) Titles(context.Context, Kind, int) ([]Title, error)          { return nil, nil }
func (Noop) Seasons(context.Context, int) ([]Season, error)              { return nil, nil }
func (Noop) Lookup(context.Context, Kind, int) (Result, error)           { return Result{}, ErrNotFound }

// Cache lifetimes. An empty search result expires quickly so that a title
// added to TMDB after a release came out is found on a later run.
const (
	searchTTL   = 30 * 24 * time.Hour
	negativeTTL = 24 * time.Hour
	titlesTTL   = 30 * 24 * time.Hour
	seasonsTTL  = 24 * time.Hour
	detailsTTL  = 30 * 24 * time.Hour
	// maxCacheEntries bounds each cache; the oldest entries go first.
	maxCacheEntries = 20000
)

type cached[T any] struct {
	V  T         `json:"v"`
	At time.Time `json:"at"`
}

// cache is a small TTL map. It is not safe for concurrent use: TMDB guards it.
type cache[T any] map[string]cached[T]

func (c cache[T]) get(key string, ttl time.Duration, now time.Time) (T, bool) {
	e, ok := c[key]
	if !ok || now.Sub(e.At) >= ttl {
		var zero T
		return zero, false
	}
	return e.V, true
}

func (c cache[T]) put(key string, v T, now time.Time) {
	if len(c) >= maxCacheEntries {
		oldest, at := "", now
		for k, e := range c {
			if e.At.Before(at) {
				oldest, at = k, e.At
			}
		}
		delete(c, oldest)
	}
	c[key] = cached[T]{V: v, At: now}
}

// TMDB queries The Movie Database API.
type TMDB struct {
	key     string
	baseURL string
	http    *http.Client
	now     func() time.Time

	mu       sync.Mutex
	searches cache[[]Result]
	titles   cache[[]Title]
	seasons  cache[[]Season]
	details  cache[Result]
	// wait sleeps before a retry (replaced in tests).
	wait func(ctx context.Context, d time.Duration) error
	// OnChange, when set, is called (without locks held) after a cache update,
	// so the caller can persist Export.
	OnChange func()
}

// DefaultTMDBURL is the TMDB v3 API base.
const DefaultTMDBURL = "https://api.themoviedb.org/3"

// NewTMDB builds a TMDB provider. key may be a v3 API key or a v4 read token.
func NewTMDB(key, baseURL string) *TMDB {
	if baseURL == "" {
		baseURL = DefaultTMDBURL
	}
	return &TMDB{
		key:      key,
		baseURL:  strings.TrimRight(baseURL, "/"),
		http:     &http.Client{Timeout: 10 * time.Second},
		now:      time.Now,
		searches: cache[[]Result]{},
		titles:   cache[[]Title]{},
		seasons:  cache[[]Season]{},
		details:  cache[Result]{},
		wait:     sleepCtx,
	}
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// cacheFile is the persisted form of the caches (see Export and Import).
type cacheFile struct {
	Searches cache[[]Result] `json:"searches"`
	Titles   cache[[]Title]  `json:"titles"`
	Seasons  cache[[]Season] `json:"seasons"`
	Details  cache[Result]   `json:"details,omitempty"`
}

// Export returns the caches as JSON. It never contains the API key.
func (t *TMDB) Export() ([]byte, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return json.Marshal(cacheFile{Searches: t.searches, Titles: t.titles, Seasons: t.seasons, Details: t.details})
}

// Import loads caches saved by Export; expired entries are dropped.
func (t *TMDB) Import(data []byte) error {
	var f cacheFile
	if err := json.Unmarshal(data, &f); err != nil {
		return err
	}
	now := t.now()
	t.mu.Lock()
	defer t.mu.Unlock()
	for k, e := range f.Searches {
		if now.Sub(e.At) < searchTTLFor(e.V) {
			t.searches[k] = e
		}
	}
	for k, e := range f.Titles {
		if now.Sub(e.At) < titlesTTL {
			t.titles[k] = e
		}
	}
	for k, e := range f.Seasons {
		if now.Sub(e.At) < seasonsTTL {
			t.seasons[k] = e
		}
	}
	for k, e := range f.Details {
		if now.Sub(e.At) < detailsTTL {
			t.details[k] = e
		}
	}
	return nil
}

func searchTTLFor(res []Result) time.Duration {
	if len(res) == 0 {
		return negativeTTL
	}
	return searchTTL
}

func (t *TMDB) changed() {
	if t.OnChange != nil {
		t.OnChange()
	}
}

// Search queries /search/{movie,tv}. Transient failures are returned as
// errors and not cached; the caller falls back to the parser result. An empty
// result is cached for a day only.
func (t *TMDB) Search(ctx context.Context, kind Kind, title string, year int) ([]Result, error) {
	key := string(kind) + "|" + strings.ToLower(title) + "|" + strconv.Itoa(year)
	now := t.now()
	t.mu.Lock()
	if e, ok := t.searches[key]; ok && now.Sub(e.At) < searchTTLFor(e.V) {
		t.mu.Unlock()
		return e.V, nil
	}
	t.mu.Unlock()

	res, err := t.search(ctx, kind, title, year)
	if err != nil {
		return nil, err
	}
	t.mu.Lock()
	t.searches.put(key, res, now)
	t.mu.Unlock()
	t.changed()
	return res, nil
}

func (t *TMDB) search(ctx context.Context, kind Kind, title string, year int) ([]Result, error) {
	q := url.Values{"query": {title}}
	if year > 0 {
		if kind == Movie {
			q.Set("year", strconv.Itoa(year))
		} else {
			q.Set("first_air_date_year", strconv.Itoa(year))
		}
	}
	var body struct {
		Results []struct {
			ID            int      `json:"id"`
			Title         string   `json:"title"`
			Name          string   `json:"name"`
			OriginalTitle string   `json:"original_title"`
			OriginalName  string   `json:"original_name"`
			ReleaseDate   string   `json:"release_date"`
			FirstAirDate  string   `json:"first_air_date"`
			OriginCountry []string `json:"origin_country"`
			VoteCount     int      `json:"vote_count"`
		} `json:"results"`
	}
	if err := t.get(ctx, "/search/"+string(kind), q, &body); err != nil {
		return nil, err
	}
	var out []Result
	for _, r := range body.Results {
		name, orig, date := r.Title, r.OriginalTitle, r.ReleaseDate
		if kind == Series {
			name, orig, date = r.Name, r.OriginalName, r.FirstAirDate
		}
		if name == "" || r.ID <= 0 {
			continue
		}
		res := Result{Title: name, OriginalTitle: orig, TMDBID: r.ID, VoteCount: max(r.VoteCount, 0)}
		if len(r.OriginCountry) > 0 {
			res.OriginCountry = r.OriginCountry[0]
		}
		if len(date) >= 4 {
			res.Year, _ = strconv.Atoi(date[:4])
		}
		out = append(out, res)
		if len(out) == MaxCandidates {
			break
		}
	}
	return out, nil
}

// Titles fetches translations and alternative titles in a single request.
func (t *TMDB) Titles(ctx context.Context, kind Kind, id int) ([]Title, error) {
	key := string(kind) + "|" + strconv.Itoa(id)
	now := t.now()
	t.mu.Lock()
	if c, ok := t.titles.get(key, titlesTTL, now); ok {
		t.mu.Unlock()
		return c, nil
	}
	t.mu.Unlock()

	type alt struct {
		Country string `json:"iso_3166_1"`
		Title   string `json:"title"`
	}
	var body struct {
		Translations struct {
			Translations []struct {
				Language string `json:"iso_639_1"`
				Country  string `json:"iso_3166_1"`
				Data     struct {
					Title string `json:"title"`
					Name  string `json:"name"`
				} `json:"data"`
			} `json:"translations"`
		} `json:"translations"`
		AlternativeTitles struct {
			Titles  []alt `json:"titles"`  // movies
			Results []alt `json:"results"` // series
		} `json:"alternative_titles"`
	}
	q := url.Values{"append_to_response": {"translations,alternative_titles"}}
	if err := t.get(ctx, "/"+string(kind)+"/"+strconv.Itoa(id), q, &body); err != nil {
		return nil, err
	}
	var out []Title
	for _, tr := range body.Translations.Translations {
		name := tr.Data.Title
		if kind == Series {
			name = tr.Data.Name
		}
		if name != "" {
			out = append(out, Title{Language: tr.Language, Country: tr.Country, Title: name, Source: "translation"})
		}
	}
	for _, a := range append(body.AlternativeTitles.Titles, body.AlternativeTitles.Results...) {
		if a.Title != "" {
			out = append(out, Title{Country: a.Country, Title: a.Title, Source: "alternative"})
		}
	}
	t.mu.Lock()
	t.titles.put(key, out, now)
	t.mu.Unlock()
	t.changed()
	return out, nil
}

// Seasons fetches the season list of a series (season 0, specials, excluded).
func (t *TMDB) Seasons(ctx context.Context, id int) ([]Season, error) {
	key := strconv.Itoa(id)
	now := t.now()
	t.mu.Lock()
	if c, ok := t.seasons.get(key, seasonsTTL, now); ok {
		t.mu.Unlock()
		return c, nil
	}
	t.mu.Unlock()

	var body struct {
		Seasons []struct {
			Number   int `json:"season_number"`
			Episodes int `json:"episode_count"`
		} `json:"seasons"`
	}
	if err := t.get(ctx, "/tv/"+key, url.Values{}, &body); err != nil {
		return nil, err
	}
	var out []Season
	for _, s := range body.Seasons {
		if s.Number > 0 && s.Episodes > 0 {
			out = append(out, Season{Number: s.Number, Episodes: s.Episodes})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Number < out[j].Number })
	t.mu.Lock()
	t.seasons.put(key, out, now)
	t.mu.Unlock()
	t.changed()
	return out, nil
}

// Lookup fetches /{movie,tv}/{id}: the entry a user named by its TMDB id.
func (t *TMDB) Lookup(ctx context.Context, kind Kind, id int) (Result, error) {
	if id <= 0 {
		return Result{}, ErrNotFound
	}
	key := string(kind) + "|" + strconv.Itoa(id)
	now := t.now()
	t.mu.Lock()
	if c, ok := t.details.get(key, detailsTTL, now); ok {
		t.mu.Unlock()
		return c, nil
	}
	t.mu.Unlock()

	var body struct {
		ID            int      `json:"id"`
		Title         string   `json:"title"`
		Name          string   `json:"name"`
		OriginalTitle string   `json:"original_title"`
		OriginalName  string   `json:"original_name"`
		ReleaseDate   string   `json:"release_date"`
		FirstAirDate  string   `json:"first_air_date"`
		OriginCountry []string `json:"origin_country"`
	}
	if err := t.get(ctx, "/"+string(kind)+"/"+strconv.Itoa(id), url.Values{}, &body); err != nil {
		return Result{}, err
	}
	name, orig, date := body.Title, body.OriginalTitle, body.ReleaseDate
	if kind == Series {
		name, orig, date = body.Name, body.OriginalName, body.FirstAirDate
	}
	if name == "" || body.ID != id {
		return Result{}, ErrNotFound
	}
	res := Result{Title: name, OriginalTitle: orig, TMDBID: id}
	if len(body.OriginCountry) > 0 {
		res.OriginCountry = body.OriginCountry[0]
	}
	if len(date) >= 4 {
		res.Year, _ = strconv.Atoi(date[:4])
	}
	t.mu.Lock()
	t.details.put(key, res, now)
	t.mu.Unlock()
	t.changed()
	return res, nil
}

// AbsoluteToSeason maps an absolute episode number onto regular seasons. ok is
// false when the number lies beyond the known episodes.
func AbsoluteToSeason(seasons []Season, abs int) (season, episode int, ok bool) {
	if abs < 1 {
		return 0, 0, false
	}
	n := abs
	for _, s := range seasons {
		if n <= s.Episodes {
			return s.Number, n, true
		}
		n -= s.Episodes
	}
	return 0, 0, false
}

// Rate limiting: a 429 is retried up to maxRetries times, waiting what
// Retry-After asks (at most maxRetryWait) or 1s, 2s, 4s without it.
const (
	maxRetries   = 3
	maxRetryWait = 10 * time.Second
)

// retryAfter reads a Retry-After header in seconds.
func retryAfter(h string, attempt int) time.Duration {
	if n, err := strconv.Atoi(strings.TrimSpace(h)); err == nil && n >= 0 {
		return min(time.Duration(n)*time.Second, maxRetryWait)
	}
	return min(time.Second<<attempt, maxRetryWait)
}

func (t *TMDB) get(ctx context.Context, path string, q url.Values, into any) error {
	bearer := strings.HasPrefix(t.key, "eyJ")
	if !bearer {
		q.Set("api_key", t.key)
	}
	var resp *http.Response
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, t.baseURL+path+"?"+q.Encode(), nil)
		if err != nil {
			return err
		}
		if bearer {
			req.Header.Set("Authorization", "Bearer "+t.key)
		}
		req.Header.Set("Accept", "application/json")
		resp, err = t.http.Do(req)
		if err != nil {
			return fmt.Errorf("tmdb request failed")
		}
		if resp.StatusCode != http.StatusTooManyRequests || attempt == maxRetries {
			break
		}
		resp.Body.Close()
		if err := t.wait(ctx, retryAfter(resp.Header.Get("Retry-After"), attempt)); err != nil {
			return err
		}
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return ErrNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("tmdb status %d", resp.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(into); err != nil {
		return fmt.Errorf("tmdb response: invalid json")
	}
	return nil
}
