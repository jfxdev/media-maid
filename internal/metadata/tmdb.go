// Package metadata optionally enriches parsed media with canonical data.
package metadata

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Result is canonical information about a movie or series.
type Result struct {
	Title  string
	Year   int
	TMDBID int
}

// Provider looks up canonical titles. ok is false when nothing was found.
type Provider interface {
	LookupMovie(ctx context.Context, title string, year int) (Result, bool)
	LookupSeries(ctx context.Context, title string, year int) (Result, bool)
}

// Noop is used when no metadata provider is configured.
type Noop struct{}

func (Noop) LookupMovie(context.Context, string, int) (Result, bool)  { return Result{}, false }
func (Noop) LookupSeries(context.Context, string, int) (Result, bool) { return Result{}, false }

// TMDB queries The Movie Database search API.
type TMDB struct {
	key     string
	baseURL string
	http    *http.Client

	mu    sync.Mutex
	cache map[string]cached
}

type cached struct {
	res Result
	ok  bool
}

// DefaultTMDBURL is the TMDB v3 API base.
const DefaultTMDBURL = "https://api.themoviedb.org/3"

// NewTMDB builds a TMDB provider. key may be a v3 API key or a v4 read token.
func NewTMDB(key, baseURL string) *TMDB {
	if baseURL == "" {
		baseURL = DefaultTMDBURL
	}
	return &TMDB{
		key:     key,
		baseURL: strings.TrimRight(baseURL, "/"),
		http:    &http.Client{Timeout: 10 * time.Second},
		cache:   map[string]cached{},
	}
}

func (t *TMDB) LookupMovie(ctx context.Context, title string, year int) (Result, bool) {
	return t.lookup(ctx, "movie", title, year)
}

func (t *TMDB) LookupSeries(ctx context.Context, title string, year int) (Result, bool) {
	return t.lookup(ctx, "tv", title, year)
}

func (t *TMDB) lookup(ctx context.Context, kind, title string, year int) (Result, bool) {
	key := kind + "|" + strings.ToLower(title) + "|" + strconv.Itoa(year)
	t.mu.Lock()
	if c, ok := t.cache[key]; ok {
		t.mu.Unlock()
		return c.res, c.ok
	}
	t.mu.Unlock()

	res, ok, err := t.search(ctx, kind, title, year)
	if err != nil {
		// Transient failures are not cached; the parser result is used instead.
		return Result{}, false
	}
	t.mu.Lock()
	t.cache[key] = cached{res, ok}
	t.mu.Unlock()
	return res, ok
}

func (t *TMDB) search(ctx context.Context, kind, title string, year int) (Result, bool, error) {
	q := url.Values{"query": {title}}
	if year > 0 {
		if kind == "movie" {
			q.Set("year", strconv.Itoa(year))
		} else {
			q.Set("first_air_date_year", strconv.Itoa(year))
		}
	}
	bearer := strings.HasPrefix(t.key, "eyJ")
	if !bearer {
		q.Set("api_key", t.key)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, t.baseURL+"/search/"+kind+"?"+q.Encode(), nil)
	if err != nil {
		return Result{}, false, err
	}
	if bearer {
		req.Header.Set("Authorization", "Bearer "+t.key)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := t.http.Do(req)
	if err != nil {
		return Result{}, false, fmt.Errorf("tmdb request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Result{}, false, fmt.Errorf("tmdb status %d", resp.StatusCode)
	}
	var body struct {
		Results []struct {
			ID           int    `json:"id"`
			Title        string `json:"title"`
			Name         string `json:"name"`
			ReleaseDate  string `json:"release_date"`
			FirstAirDate string `json:"first_air_date"`
		} `json:"results"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&body); err != nil {
		return Result{}, false, err
	}
	if len(body.Results) == 0 {
		return Result{}, false, nil
	}
	r := body.Results[0]
	name, date := r.Title, r.ReleaseDate
	if kind == "tv" {
		name, date = r.Name, r.FirstAirDate
	}
	res := Result{Title: name, TMDBID: r.ID}
	if len(date) >= 4 {
		res.Year, _ = strconv.Atoi(date[:4])
	}
	if res.Title == "" {
		return Result{}, false, nil
	}
	return res, true, nil
}
