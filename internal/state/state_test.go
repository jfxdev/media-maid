package state

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestStore(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if data, err := s.Load("cache"); data != nil || err != nil {
		t.Fatalf("missing document = %q %v", data, err)
	}
	if err := s.Save("cache", []byte(`{"a":1}`)); err != nil {
		t.Fatal(err)
	}
	if err := s.Save("cache", []byte(`{"a":2}`)); err != nil {
		t.Fatal(err)
	}
	if data, _ := s.Load("cache"); string(data) != `{"a":2}` {
		t.Errorf("load = %s", data)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 || entries[0].Name() != "cache.json" {
		t.Errorf("leftover files: %v", entries)
	}
	for _, bad := range []string{"../x", "a/b", "", "Upper"} {
		if err := s.Save(bad, nil); err == nil {
			t.Errorf("Save(%q) accepted", bad)
		}
	}

	var none *Store
	if data, err := none.Load("x"); data != nil || err != nil || none.Save("x", nil) != nil {
		t.Error("nil store must be a no-op")
	}
	none.Saver("x", time.Millisecond, nil, nil).Touch()

	if _, err := Open(filepath.Join(dir, "missing")); err == nil {
		t.Error("missing dir accepted")
	}
}

func TestSaver(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var n atomic.Int32
	v := s.Saver("doc", 50*time.Millisecond, func() ([]byte, error) {
		n.Add(1)
		return []byte(`[]`), nil
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	v.Touch()
	v.Touch()
	v.Touch()
	time.Sleep(200 * time.Millisecond)
	if n.Load() != 1 {
		t.Errorf("exports = %d, want 1 (coalesced)", n.Load())
	}
	v.Flush() // clean: nothing to write
	v.Touch()
	v.Flush()
	if n.Load() != 2 {
		t.Errorf("exports = %d, want 2", n.Load())
	}
}
