package metadata

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestTMDB(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		switch {
		case r.URL.Path == "/search/movie" && r.URL.Query().Get("query") == "inception" && r.URL.Query().Get("api_key") == "k" && r.URL.Query().Get("year") == "2010":
			io.WriteString(w, `{"results":[{"id":27205,"title":"Inception","release_date":"2010-07-15"}]}`)
		case r.URL.Path == "/search/tv" && r.Header.Get("Authorization") == "Bearer eyJtoken":
			io.WriteString(w, `{"results":[{"id":1396,"name":"Breaking Bad","first_air_date":"2008-01-20"}]}`)
		case r.URL.Path == "/search/movie" && r.URL.Query().Get("query") == "boom":
			w.WriteHeader(500)
		default:
			io.WriteString(w, `{"results":[]}`)
		}
	}))
	defer srv.Close()
	ctx := context.Background()

	m := NewTMDB("k", srv.URL)
	if r, ok := m.LookupMovie(ctx, "inception", 2010); !ok || r.Title != "Inception" || r.Year != 2010 || r.TMDBID != 27205 {
		t.Errorf("movie = %+v %v", r, ok)
	}
	m.LookupMovie(ctx, "inception", 2010)
	if calls.Load() != 1 {
		t.Errorf("cache miss: %d calls", calls.Load())
	}
	if _, ok := m.LookupMovie(ctx, "nothing", 0); ok {
		t.Error("expected no result")
	}
	if _, ok := m.LookupMovie(ctx, "boom", 0); ok {
		t.Error("server error must fall back")
	}

	tv := NewTMDB("eyJtoken", srv.URL)
	if r, ok := tv.LookupSeries(ctx, "breaking bad", 0); !ok || r.Title != "Breaking Bad" || r.Year != 2008 || r.TMDBID != 1396 {
		t.Errorf("series = %+v %v", r, ok)
	}
}
