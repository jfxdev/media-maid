package watcher

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
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

func TestUnitOf(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"movies/Done.2001/Subs", "tv/Show.S01/Season 01", "Release.2020/Sample", "loose"} {
		os.MkdirAll(filepath.Join(root, d), 0o755)
	}
	w := &Watcher{root: root}
	tests := map[string]string{
		"File.2000.mkv":                         "File.2000.mkv",
		"movies/Done.2001/Done.2001.mkv":        "movies/Done.2001",
		"movies/Done.2001/Subs/en.srt":          "movies/Done.2001",
		"movies/Single.File.2003.mkv":           "movies/Single.File.2003.mkv", // category with releases
		"tv/Show.S01/Season 01/Show.S01E01.mkv": "tv/Show.S01",
		"Release.2020/Sample/sample.mkv":        "Release.2020",
		"loose/Movie.2004.mkv":                  "loose",
	}
	for rel, want := range tests {
		if got := w.unitOf(filepath.Join(root, rel), map[string]bool{}); got != filepath.Join(root, want) {
			t.Errorf("unitOf(%s) = %s, want %s", rel, got, want)
		}
	}
	if got := w.unitOf(filepath.Join(filepath.Dir(root), "elsewhere.mkv"), map[string]bool{}); got != "" {
		t.Errorf("outside root = %q", got)
	}
}

// A finished release must not wait for another download in the same
// torrent client category.
func TestWatcherCategoryFolders(t *testing.T) {
	root := t.TempDir()
	done := filepath.Join(root, "movies", "Done.2001")
	active := filepath.Join(root, "movies", "Active.2002")
	os.MkdirAll(done, 0o755)
	os.MkdirAll(active, 0o755)

	var mu sync.Mutex
	var calls []string
	w, err := New(root, time.Second, func(_ context.Context, p string) {
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

	time.Sleep(200 * time.Millisecond)
	os.WriteFile(filepath.Join(done, "Done.2001.mkv"), []byte("x"), 0o644)
	f, _ := os.Create(filepath.Join(active, "Active.2002.mkv"))
	defer f.Close()
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		f.Write([]byte("data"))
		mu.Lock()
		got := append([]string(nil), calls...)
		mu.Unlock()
		for _, c := range got {
			if c == active {
				t.Fatal("organized while still being written")
			}
			if c == done {
				return
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("finished release was held back by an active one: calls = %v", calls)
}

// When the kernel event queue overflows, files whose events were dropped
// must still be picked up.
func TestWatcherRescansOnOverflow(t *testing.T) {
	root := t.TempDir()
	w, err := New(root, time.Second, func(context.Context, string) {}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer w.fsw.Close()
	// Run is not started, so these events are never handled: they are "lost".
	rel := filepath.Join(root, "Lost.2020")
	os.MkdirAll(rel, 0o755)
	f := filepath.Join(rel, "Lost.2020.mkv")
	os.WriteFile(f, []byte("x"), 0o644)
	if _, ok := w.pending[f]; ok {
		t.Fatal("file pending before the overflow")
	}
	w.handleError(fsnotify.ErrEventOverflow)
	if _, ok := w.pending[f]; !ok {
		t.Errorf("file not rescanned after overflow: pending = %v", w.pending)
	}
	w.handleError(errors.New("other"))
}
