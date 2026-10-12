package watcher

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestWatcherWaitsForStableFiles(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "Existing.2000.mkv"), []byte("x"), 0o644)

	var mu sync.Mutex
	var calls []string
	w, err := New(root, 1500*time.Millisecond, func(_ context.Context, p string) {
		mu.Lock()
		calls = append(calls, p)
		mu.Unlock()
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	rel := filepath.Join(root, "Release.2020")
	os.MkdirAll(rel, 0o755)
	f, _ := os.Create(filepath.Join(rel, "Release.2020.mkv"))
	// Keep writing for a while: nothing must be organized meanwhile.
	for i := 0; i < 4; i++ {
		f.Write([]byte("data"))
		time.Sleep(500 * time.Millisecond)
	}
	f.Close()
	mu.Lock()
	for _, c := range calls {
		if c == rel {
			t.Error("organized while still being written")
		}
	}
	mu.Unlock()

	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		got := map[string]int{}
		for _, c := range calls {
			got[c]++
		}
		mu.Unlock()
		if got[rel] == 1 && got[filepath.Join(root, "Existing.2000.mkv")] == 1 {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	t.Fatalf("calls = %v", calls)
}
