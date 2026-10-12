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

// While one release is being organized, events for other releases must keep
// being processed so they are ready as soon as the worker is free.
func TestWatcherKeepsProcessingDuringOrganize(t *testing.T) {
	root := t.TempDir()
	a := filepath.Join(root, "A.2001")
	b := filepath.Join(root, "B.2002")
	os.MkdirAll(a, 0o755)
	os.WriteFile(filepath.Join(a, "A.2001.mkv"), []byte("x"), 0o644)

	release := make(chan struct{})
	var mu sync.Mutex
	var releasedAt, bAt time.Time
	w, err := New(root, time.Second, func(_ context.Context, p string) {
		switch p {
		case a:
			<-release
			mu.Lock()
			releasedAt = time.Now()
			mu.Unlock()
		case b:
			mu.Lock()
			bAt = time.Now()
			mu.Unlock()
		}
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	time.Sleep(2500 * time.Millisecond) // A is now blocked in organize
	os.MkdirAll(filepath.Join(b, "sub"), 0o755)
	time.Sleep(200 * time.Millisecond)
	os.WriteFile(filepath.Join(b, "sub", "B.2002.mkv"), []byte("x"), 0o644)
	time.Sleep(2500 * time.Millisecond) // B becomes stable while A is still busy
	close(release)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		done := !bAt.IsZero()
		gap := bAt.Sub(releasedAt)
		mu.Unlock()
		if done {
			if gap > 700*time.Millisecond {
				t.Errorf("B organized %v after A finished; events were not processed during organize", gap)
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("release created during organize was never organized")
}
