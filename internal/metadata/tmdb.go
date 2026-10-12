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
}

// MaxCandidates bounds how many search results are considered.
const MaxCandidates = 5

// Noop is used when no metadata provider is configured.
type Noop struct{}

func (Noop) Search(context.Context, Kind, string, int) ([]Result, error) { return nil, nil }
func (Noop) Titles(context.Context, Kind, int) ([]Title, error)          { return nil, nil }

// TMDB queries The Movie Database API.
type TMDB struct {
	key     string
	baseURL string
	http    *http.Client

	mu     sync.Mutex
	cache  map[string][]Result
	titles map[string][]Title
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
		cache:   map[string][]Result{},
		titles:  map[string][]Title{},
	}
}

// Search queries /search/{movie,tv}. Transient failures are returned as
// errors and not cached; the caller falls back to the parser result.
func (t *TMDB) Search(ctx context.Context, kind Kind, title string, year int) ([]Result, error) {
	key := string(kind) + "|" + strings.ToLower(title) + "|" + strconv.Itoa(year)
	t.mu.Lock()
	if c, ok := t.cache[key]; ok {
		t.mu.Unlock()
		return c, nil
	}
	t.mu.Unlock()

	res, err := t.search(ctx, kind, title, year)
	if err != nil {
		return nil, err
	}
	t.mu.Lock()
	t.cache[key] = res
	t.mu.Unlock()
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
			ID            int    `json:"id"`
			Title         string `json:"title"`
			Name          string `json:"name"`
			OriginalTitle string `json:"original_title"`
			OriginalName  string `json:"original_name"`
			ReleaseDate   string `json:"release_date"`
			FirstAirDate  string `json:"first_air_date"`
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
		res := Result{Title: name, OriginalTitle: orig, TMDBID: r.ID}
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
	t.mu.Lock()
	if c, ok := t.titles[key]; ok {
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
	t.titles[key] = out
	t.mu.Unlock()
	return out, nil
}

func (t *TMDB) get(ctx context.Context, path string, q url.Values, into any) error {
	bearer := strings.HasPrefix(t.key, "eyJ")
	if !bearer {
		q.Set("api_key", t.key)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, t.baseURL+path+"?"+q.Encode(), nil)
	if err != nil {
		return err
	}
	if bearer {
		req.Header.Set("Authorization", "Bearer "+t.key)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := t.http.Do(req)
	if err != nil {
		return fmt.Errorf("tmdb request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("tmdb status %d", resp.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(into); err != nil {
		return fmt.Errorf("tmdb response: invalid json")
	}
	return nil
}
