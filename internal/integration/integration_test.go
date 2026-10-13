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
	method, path, query, token, body string
}

type fakeServer struct {
	mu   sync.Mutex
	reqs []recorded
	srv  *httptest.Server
}

func newFake(t *testing.T, header string, routes map[string]string) *fakeServer {
	f := &fakeServer{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.reqs = append(f.reqs, recorded{r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Get(header), string(body)})
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
		mode, method, path, header, tag, edition string
	}{
		{"jellyfin", "POST", "/Library/Refresh", "X-Emby-Token", "[tmdbid-42]", ""},
		{"emby", "POST", "/emby/Library/Refresh", "X-Emby-Token", "[tmdbid=42]", ""},
		{"plex", "GET", "/library/sections/all/refresh", "X-Plex-Token", "{tmdb-42}", "{edition-Director's Cut}"},
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
			if got := spec.EditionTag("Director's Cut"); got != tt.edition {
				t.Errorf("EditionTag = %q, want %q", got, tt.edition)
			}
			if spec.EditionTag("") != "" {
				t.Error("EditionTag(\"\") should be empty")
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

func TestTargetedSync(t *testing.T) {
	plexSections := `{"MediaContainer":{"Directory":[{"key":"1","type":"movie","Location":[{"path":"/media/movies"}]},{"key":"2","type":"show","Location":[{"path":"/media/tv"}]}]}}`
	tests := []struct {
		mode, header string
		routes       map[string]string
		paths        []string
		want         []recorded // method, path, query (token not compared)
	}{
		{"jellyfin", "X-Emby-Token", map[string]string{"POST /Library/Media/Updated": ""}, []string{"/data/media/movies/A (2000)"},
			[]recorded{{method: "POST", path: "/Library/Media/Updated"}}},
		{"emby", "X-Emby-Token", map[string]string{"POST /emby/Library/Media/Updated": ""}, []string{"/data/media/movies/A (2000)"},
			[]recorded{{method: "POST", path: "/emby/Library/Media/Updated"}}},
		{"plex", "X-Plex-Token", map[string]string{"GET /library/sections": plexSections, "GET /library/sections/2/refresh": ""}, []string{"/data/media/tv/Show/Season 01"},
			[]recorded{{method: "GET", path: "/library/sections"}, {method: "GET", path: "/library/sections/2/refresh", query: "path=%2Fmedia%2Ftv%2FShow%2FSeason+01"}}},
		// A folder outside every Plex section falls back to a full refresh.
		{"plex", "X-Plex-Token", map[string]string{"GET /library/sections": plexSections, "GET /library/sections/all/refresh": ""}, []string{"/data/elsewhere"},
			[]recorded{{method: "GET", path: "/library/sections"}, {method: "GET", path: "/library/sections/all/refresh"}}},
		// A failing targeted refresh also falls back.
		{"jellyfin", "X-Emby-Token", map[string]string{"POST /Library/Refresh": ""}, []string{"/data/media/movies/A (2000)"},
			[]recorded{{method: "POST", path: "/Library/Media/Updated"}, {method: "POST", path: "/Library/Refresh"}}},
	}
	for _, tt := range tests {
		t.Run(tt.mode, func(t *testing.T) {
			spec, _ := Lookup(tt.mode)
			f := newFake(t, tt.header, tt.routes)
			c, _ := NewClient(spec, f.srv.URL, "tok")
			h := jobs.NewHistory(10)
			s := NewSyncer(c, 10*time.Millisecond, h, discard)
			s.ToServer = func(p string) string { return strings.Replace(p, "/data/media", "/media", 1) }
			s.Request(tt.paths...)
			time.Sleep(150 * time.Millisecond)
			f.mu.Lock()
			defer f.mu.Unlock()
			if len(f.reqs) != len(tt.want) {
				t.Fatalf("requests = %+v, want %+v", f.reqs, tt.want)
			}
			for i, w := range tt.want {
				r := f.reqs[i]
				if r.method != w.method || r.path != w.path || (w.query != "" && r.query != w.query) || r.token != "tok" {
					t.Errorf("request %d = %+v, want %+v", i, r, w)
				}
			}
			if tt.mode != "plex" && !strings.Contains(f.reqs[0].body, `"Path":"/media/movies/A (2000)"`) {
				t.Errorf("body = %s", f.reqs[0].body)
			}
		})
	}
}

func TestSyncMaxWait(t *testing.T) {
	spec, _ := Lookup("jellyfin")
	f := newFake(t, "X-Emby-Token", map[string]string{"POST /Library/Media/Updated": ""})
	c, _ := NewClient(spec, f.srv.URL, "tok")
	s := NewSyncer(c, 40*time.Millisecond, jobs.NewHistory(10), discard)
	defer s.Stop()
	// Requests every 20ms would postpone a plain debounce forever.
	deadline := time.Now().Add(400 * time.Millisecond)
	for time.Now().Before(deadline) {
		s.Request("/data/media/movies/A")
		time.Sleep(20 * time.Millisecond)
	}
	f.mu.Lock()
	n := len(f.reqs)
	f.mu.Unlock()
	if n < 1 {
		t.Error("a steady stream of requests postponed the refresh indefinitely")
	}
}

// GetItem fetches the item as the server knows it now; a missing item (an
// empty result or a 404) is ErrNotFound, and ids are validated first.
func TestGetItem(t *testing.T) {
	embyOne := `{"Items":[{"Id":"a1","Name":"Inception","Type":"Movie","Path":"/media/movies/Inception (2010)/Inception (2010).mkv"}],"TotalRecordCount":1}`
	plexOne := `{"MediaContainer":{"Metadata":[{"ratingKey":"a1","title":"Inception","type":"movie","Media":[{"Part":[{"file":"/m/a.mkv"}]}]}]}}`
	tests := []struct {
		mode, header, path string
		routes             map[string]string
		wantPath           string
	}{
		{"jellyfin", "X-Emby-Token", "/Items", map[string]string{"GET /Items": embyOne}, "/media/movies/Inception (2010)/Inception (2010).mkv"},
		{"emby", "X-Emby-Token", "/emby/Items", map[string]string{"GET /emby/Items": embyOne}, "/media/movies/Inception (2010)/Inception (2010).mkv"},
		{"plex", "X-Plex-Token", "/library/metadata/a1", map[string]string{"GET /library/metadata/a1": plexOne}, "/m/a.mkv"},
	}
	for _, tt := range tests {
		t.Run(tt.mode, func(t *testing.T) {
			spec, _ := Lookup(tt.mode)
			f := newFake(t, tt.header, tt.routes)
			c, _ := NewClient(spec, f.srv.URL, "tok")
			it, err := c.GetItem(context.Background(), "a1")
			if err != nil || it.ID != "a1" || len(it.Paths) != 1 || it.Paths[0] != tt.wantPath {
				t.Fatalf("GetItem = %+v, %v", it, err)
			}
			if r := f.last(); r.path != tt.path || r.token != "tok" {
				t.Errorf("request = %+v", r)
			}
			if tt.mode != "plex" && !strings.Contains(f.last().query, "Ids=a1") {
				t.Errorf("query = %s", f.last().query)
			}
			// Another id in the answer, or a 404, means the item is gone.
			if _, err := c.GetItem(context.Background(), "b2"); !errors.Is(err, ErrNotFound) {
				t.Errorf("missing item err = %v", err)
			}
			if _, err := c.GetItem(context.Background(), "../x"); !errors.Is(err, ErrInvalidItemID) {
				t.Errorf("invalid id err = %v", err)
			}
		})
	}
}
