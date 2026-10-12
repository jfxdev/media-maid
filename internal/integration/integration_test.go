package integration

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jfxdev/media-maid/internal/jobs"
)

type recorded struct {
	method, path, query, token string
}

type fakeServer struct {
	mu   sync.Mutex
	reqs []recorded
	srv  *httptest.Server
}

func newFake(t *testing.T, header string, routes map[string]string) *fakeServer {
	f := &fakeServer{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.reqs = append(f.reqs, recorded{r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Get(header)})
		f.mu.Unlock()
		if body, ok := routes[r.Method+" "+r.URL.Path]; ok {
			io.WriteString(w, body)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeServer) last() recorded {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reqs[len(f.reqs)-1]
}

var discard = slog.New(slog.NewTextHandler(io.Discard, nil))

func TestSyncPerPlatform(t *testing.T) {
	tests := []struct {
		mode, method, path, header, tag string
	}{
		{"jellyfin", "POST", "/Library/Refresh", "X-Emby-Token", "[tmdbid-42]"},
		{"emby", "POST", "/emby/Library/Refresh", "X-Emby-Token", "[tmdbid=42]"},
		{"plex", "GET", "/library/sections/all/refresh", "X-Plex-Token", "{tmdb-42}"},
	}
	for _, tt := range tests {
		t.Run(tt.mode, func(t *testing.T) {
			spec, ok := Lookup(tt.mode)
			if !ok {
				t.Fatal("unknown mode")
			}
			if got := spec.IDTag(42); got != tt.tag {
				t.Errorf("IDTag = %q, want %q", got, tt.tag)
			}
			if spec.IDTag(0) != "" {
				t.Error("IDTag(0) should be empty")
			}
			f := newFake(t, tt.header, map[string]string{tt.method + " " + tt.path: ""})
			c, _ := NewClient(spec, f.srv.URL+"/", "secret")
			h := jobs.NewHistory(10)
			s := NewSyncer(c, 10*time.Millisecond, h, discard)
			if err := s.SyncNow(context.Background()); err != nil {
				t.Fatal(err)
			}
			r := f.last()
			if r.method != tt.method || r.path != tt.path || r.token != "secret" {
				t.Errorf("request = %+v", r)
			}
			if _, last := h.Snapshot(); last == nil || last.Status != jobs.StatusOK {
				t.Errorf("last sync = %+v", last)
			}
		})
	}
}

func TestSyncDebounceAndErrors(t *testing.T) {
	spec, _ := Lookup("jellyfin")
	f := newFake(t, "X-Emby-Token", map[string]string{})
	c, _ := NewClient(spec, f.srv.URL, "secret")
	h := jobs.NewHistory(10)
	s := NewSyncer(c, 20*time.Millisecond, h, discard)
	for i := 0; i < 5; i++ {
		s.Request()
	}
	time.Sleep(100 * time.Millisecond)
	f.mu.Lock()
	n := len(f.reqs)
	f.mu.Unlock()
	if n != 1 {
		t.Errorf("debounced requests = %d, want 1", n)
	}
	_, last := h.Snapshot()
	if last == nil || last.Status != jobs.StatusError || strings.Contains(last.Error, "secret") {
		t.Errorf("last sync = %+v", last)
	}

	disabled := NewSyncer(nil, time.Millisecond, h, discard)
	disabled.Request()
	if err := disabled.SyncNow(context.Background()); !errors.Is(err, ErrSyncDisabled) {
		t.Errorf("err = %v", err)
	}
}

func TestListAndDelete(t *testing.T) {
	embyItems := `{"Items":[{"Id":"a1","Name":"Inception","Type":"Movie","Path":"/media/movies/Inception (2010)/Inception (2010).mkv"},{"Id":"f","Name":"Folder","Type":"Movie"}],"TotalRecordCount":2}`
	plexSections := `{"MediaContainer":{"Directory":[{"key":"1","type":"movie"},{"key":"2","type":"show"},{"key":"3","type":"artist"}]}}`
	plexMovies := `{"MediaContainer":{"totalSize":1,"Metadata":[{"ratingKey":"10","title":"Inception","type":"movie","Media":[{"Part":[{"file":"/m/a.mkv"},{"file":"/m/b.mkv"}]}]}]}}`
	plexEps := `{"MediaContainer":{"totalSize":1,"Metadata":[{"ratingKey":"20","title":"Pilot","type":"episode","Media":[{"Part":[{"file":"/t/s01e01.mkv"}]}]}]}}`

	tests := []struct {
		mode, header string
		routes       map[string]string
		wantIDs      []string
		deletePath   string
	}{
		{"jellyfin", "X-Emby-Token", map[string]string{"GET /Items": embyItems, "DELETE /Items/a1": ""}, []string{"a1"}, "/Items/a1"},
		{"emby", "X-Emby-Token", map[string]string{"GET /emby/Items": embyItems, "DELETE /emby/Items/a1": ""}, []string{"a1"}, "/emby/Items/a1"},
		{"plex", "X-Plex-Token", map[string]string{
			"GET /library/sections": plexSections, "GET /library/sections/1/all": plexMovies,
			"GET /library/sections/2/all": plexEps, "DELETE /library/metadata/a1": "",
		}, []string{"10", "20"}, "/library/metadata/a1"},
	}
	for _, tt := range tests {
		t.Run(tt.mode, func(t *testing.T) {
			spec, _ := Lookup(tt.mode)
			f := newFake(t, tt.header, tt.routes)
			c, _ := NewClient(spec, f.srv.URL, "tok")
			items, err := c.ListItems(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			var ids []string
			for _, it := range items {
				ids = append(ids, it.ID)
			}
			if strings.Join(ids, ",") != strings.Join(tt.wantIDs, ",") {
				t.Errorf("ids = %v, want %v", ids, tt.wantIDs)
			}
			if tt.mode == "plex" && len(items[0].Paths) != 2 {
				t.Errorf("plex parts = %v", items[0].Paths)
			}
			if err := c.DeleteItem(context.Background(), "a1"); err != nil {
				t.Fatal(err)
			}
			if r := f.last(); r.method != "DELETE" || r.path != tt.deletePath || r.token != "tok" {
				t.Errorf("delete = %+v", r)
			}
			if err := c.DeleteItem(context.Background(), "missing"); !errors.Is(err, ErrNotFound) {
				t.Errorf("err = %v", err)
			}
			f.mu.Lock()
			before := len(f.reqs)
			f.mu.Unlock()
			for _, bad := range []string{"../../Library/Refresh", "a/b", "a?x=1", ""} {
				if err := c.DeleteItem(context.Background(), bad); !errors.Is(err, ErrInvalidItemID) {
					t.Errorf("DeleteItem(%q) err = %v", bad, err)
				}
			}
			f.mu.Lock()
			after := len(f.reqs)
			f.mu.Unlock()
			if after != before {
				t.Error("invalid ids must not reach the server")
			}
		})
	}
}
