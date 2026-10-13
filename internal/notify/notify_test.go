package notify

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type sink struct {
	mu   sync.Mutex
	reqs []*http.Request
	body []string
}

func (s *sink) handler(code int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.reqs = append(s.reqs, r)
		s.body = append(s.body, string(b))
		s.mu.Unlock()
		w.WriteHeader(code)
	}
}

func TestNotifier(t *testing.T) {
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	on := map[string]bool{"conflict": true, "upgraded": true}

	t.Run("json batch", func(t *testing.T) {
		s := &sink{}
		srv := httptest.NewServer(s.handler(200))
		defer srv.Close()
		n := New(Options{URL: srv.URL, On: on, Delay: time.Hour, Log: quiet})
		n.Notify(Event{Source: "A.mkv", Dest: "movies/A/A.mkv", Status: "conflict", Error: "another release"})
		n.Notify(Event{Source: "B.mkv", Status: "linked"}) // not selected
		n.Notify(Event{Source: "C.mkv", Status: "upgraded"})
		n.Flush()
		if len(s.body) != 1 {
			t.Fatalf("posts = %d, want one batch", len(s.body))
		}
		var p payload
		if err := json.Unmarshal([]byte(s.body[0]), &p); err != nil {
			t.Fatal(err)
		}
		if len(p.Events) != 2 || p.Title != "Media Maid: 2 organize events" ||
			!strings.Contains(p.Body, "conflict: A.mkv -> movies/A/A.mkv (another release)") ||
			s.reqs[0].Header.Get("Content-Type") != "application/json" {
			t.Errorf("payload = %+v", p)
		}
	})

	t.Run("text for ntfy", func(t *testing.T) {
		s := &sink{}
		srv := httptest.NewServer(s.handler(200))
		defer srv.Close()
		n := New(Options{URL: srv.URL, Format: FormatText, On: on, Delay: 10 * time.Millisecond, Log: quiet})
		n.Notify(Event{Source: "Bad\nName.mkv", Status: "conflict"})
		time.Sleep(200 * time.Millisecond)
		n.Flush()
		if len(s.body) != 1 || !strings.HasPrefix(s.body[0], "conflict: Bad") {
			t.Fatalf("bodies = %q", s.body)
		}
		if h := s.reqs[0].Header.Get("Title"); h != "Media Maid: conflict Bad Name.mkv" {
			t.Errorf("Title = %q", h)
		}
	})

	t.Run("failures do not leak the url", func(t *testing.T) {
		s := &sink{}
		srv := httptest.NewServer(s.handler(500))
		defer srv.Close()
		var logs strings.Builder
		n := New(Options{URL: srv.URL + "/secret-token", On: on, Delay: time.Hour, Log: slog.New(slog.NewTextHandler(&logs, nil))})
		n.Notify(Event{Source: "A.mkv", Status: "conflict"})
		n.Flush()
		n2 := New(Options{URL: "http://127.0.0.1:1/secret-token", On: on, Delay: time.Hour, Log: slog.New(slog.NewTextHandler(&logs, nil))})
		n2.Notify(Event{Source: "A.mkv", Status: "conflict"})
		n2.Flush()
		if strings.Contains(logs.String(), "secret-token") || !strings.Contains(logs.String(), "status 500") {
			t.Errorf("logs = %s", logs.String())
		}
	})

	t.Run("large bursts are capped", func(t *testing.T) {
		batch := make([]Event, maxBatch+5)
		for i := range batch {
			batch[i] = Event{Source: "x.mkv", Status: "conflict"}
		}
		p := render(batch)
		if len(p.Events) != maxBatch || !strings.HasSuffix(p.Body, "... and 5 more") {
			t.Errorf("render = %d events, body tail %q", len(p.Events), p.Body[len(p.Body)-20:])
		}
	})

	var nilN *Notifier
	nilN.Notify(Event{Status: "conflict"})
	nilN.Flush()
}
