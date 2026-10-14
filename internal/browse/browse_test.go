package browse

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jfxdev/media-maid/internal/safepath"
)

// setup builds a downloads root with a bit of everything and returns a Lister
// over it plus the directory outside the root.
func setup(t *testing.T) (*Lister, string, string) {
	t.Helper()
	base := t.TempDir()
	dl := filepath.Join(base, "downloads")
	outside := filepath.Join(base, "outside")
	for _, d := range []string{dl, outside, filepath.Join(dl, "Movie.2010"), filepath.Join(dl, ".incomplete"), filepath.Join(dl, "b-folder")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(p, content string) {
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(dl, "Movie.2010", "Movie.2010.mkv"), "SECRET-CONTENT-MARKER")
	write(filepath.Join(dl, "Movie.2010", "Movie.2010.srt"), "1\n00:00:01,000 --> 00:00:02,000\nhi\n")
	write(filepath.Join(dl, "Movie.2010", ".nfo-hidden"), "x")
	write(filepath.Join(dl, "a.txt"), "x")
	write(filepath.Join(outside, "secret.txt"), "SECRET-CONTENT-MARKER")
	if err := os.Link(filepath.Join(dl, "Movie.2010", "Movie.2010.mkv"), filepath.Join(dl, "b-folder", "linked.mkv")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dl, "Movie.2010"), filepath.Join(dl, "inside-link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dl, "Movie.2010", "escape")); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(dl, "pipe"), 0o644); err != nil {
		t.Fatal(err)
	}
	r, err := os.OpenRoot(dl)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	l := New(map[string]*os.Root{"downloads": r}, map[string]bool{".mkv": true}, map[string]bool{".srt": true})
	return l, dl, outside
}

func names(es []Entry) string {
	var out []string
	for _, e := range es {
		out = append(out, e.Name)
	}
	return strings.Join(out, ",")
}

func TestList(t *testing.T) {
	l, _, _ := setup(t)
	ctx := context.Background()

	got, err := l.List(ctx, "downloads", "", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	// Directories first, then by name; hidden entries are listed and marked.
	if want := ".incomplete,b-folder,Movie.2010,a.txt,inside-link,pipe"; names(got.Entries) != want {
		t.Errorf("root entries = %s, want %s", names(got.Entries), want)
	}
	if got.Path != "" || got.Total != 6 || got.Truncated {
		t.Errorf("listing = %+v", got)
	}
	types := map[string]string{}
	for _, e := range got.Entries {
		types[e.Name] = e.Type
	}
	if types["inside-link"] != TypeSymlink || types["pipe"] != TypeOther || types["a.txt"] != TypeFile || types[".incomplete"] != TypeDir {
		t.Errorf("types = %v", types)
	}
	if !got.Entries[0].Hidden || got.Entries[1].Hidden {
		t.Errorf("hidden flags wrong: %+v", got.Entries[:2])
	}

	got, err = l.List(ctx, "downloads", "Movie.2010", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]Entry{}
	for _, e := range got.Entries {
		byName[e.Name] = e
	}
	mkv := byName["Movie.2010.mkv"]
	if mkv.Media != MediaVideo || mkv.Size != int64(len("SECRET-CONTENT-MARKER")) || mkv.Links != 2 || mkv.Modified.IsZero() {
		t.Errorf("video entry = %+v", mkv)
	}
	if byName["Movie.2010.srt"].Media != MediaSubtitle || !byName[".nfo-hidden"].Hidden || byName["escape"].Type != TypeSymlink {
		t.Errorf("entries = %+v", got.Entries)
	}
	if got.Path != "Movie.2010" {
		t.Errorf("path = %q", got.Path)
	}
	// Nothing in a listing comes from file contents.
	b, _ := json.Marshal(got)
	if strings.Contains(string(b), "SECRET-CONTENT-MARKER") || strings.Contains(string(b), "outside") {
		t.Errorf("listing leaks content or targets: %s", b)
	}
}

func TestListErrors(t *testing.T) {
	l, _, _ := setup(t)
	tests := []struct {
		root, rel string
		want      error
	}{
		{"movies", "", ErrUnknownRoot},
		{"downloads", "../outside", safepath.ErrInvalidPath},
		{"downloads", "Movie.2010/../../outside", safepath.ErrInvalidPath},
		{"downloads", "/etc", safepath.ErrInvalidPath},
		{"downloads", "a\x00b", safepath.ErrInvalidPath},
		{"downloads", "missing", ErrNotFound},
		{"downloads", "a.txt", ErrNotDir},
		{"downloads", "Movie.2010/Movie.2010.mkv", ErrNotDir},
		{"downloads", "a.txt/x", ErrNotDir},
		// The final symlink is never followed, even inside the root.
		{"downloads", "inside-link", ErrNotDir},
		{"downloads", "Movie.2010/escape", ErrNotDir},
		// Through an escaping symlink, the root refuses the path.
		{"downloads", "Movie.2010/escape/secret.txt", safepath.ErrUnauthorizedVolume},
		{"downloads", "pipe", ErrNotDir},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%s/%q", tt.root, tt.rel), func(t *testing.T) {
			got, err := l.List(context.Background(), tt.root, tt.rel, 0, 0)
			if !errors.Is(err, tt.want) {
				t.Errorf("err = %v (%+v), want %v", err, got, tt.want)
			}
		})
	}
}

// TestOpenRefusesNonDirectory: a folder swapped for a FIFO between the Lstat
// and the open must fail at once (O_DIRECTORY) instead of blocking on it.
func TestOpenRefusesNonDirectory(t *testing.T) {
	_, dl, _ := setup(t)
	r, err := os.OpenRoot(dl)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	done := make(chan error, 1)
	go func() {
		f, err := r.OpenFile("pipe", os.O_RDONLY|syscall.O_DIRECTORY, 0)
		if err == nil {
			f.Close()
		}
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(mapErr(err), ErrNotDir) {
			t.Errorf("open fifo err = %v, want ErrNotDir", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("opening a FIFO as a directory blocked")
	}
}

func TestListPagination(t *testing.T) {
	dir := t.TempDir()
	for i := range 25 {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("f%02d", i)), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	r, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	l := New(map[string]*os.Root{"tv": r}, nil, nil)
	ctx := context.Background()

	got, err := l.List(ctx, "tv", "", 20, 10)
	if err != nil || got.Total != 25 || names(got.Entries) != "f20,f21,f22,f23,f24" {
		t.Errorf("page = %+v, %v", got, err)
	}
	got, err = l.List(ctx, "tv", "", 30, 10)
	if err != nil || len(got.Entries) != 0 || got.Entries == nil {
		t.Errorf("past the end = %+v, %v", got, err)
	}
	got, err = l.List(ctx, "tv", "", 0, MaxLimit+1)
	if err != nil || len(got.Entries) != 25 {
		t.Errorf("limit above max falls back to default: %d, %v", len(got.Entries), err)
	}

	l.maxEntries = 10
	got, err = l.List(ctx, "tv", "", 0, 0)
	if err != nil || !got.Truncated || got.Total != 10 || len(got.Entries) != 10 {
		t.Errorf("truncated = %+v, %v", got, err)
	}
}

func TestListPermission(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads any directory")
	}
	dir := t.TempDir()
	locked := filepath.Join(dir, "locked")
	if err := os.Mkdir(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(locked, 0o755) })
	r, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	l := New(map[string]*os.Root{"movies": r}, nil, nil)
	if _, err := l.List(context.Background(), "movies", "locked", 0, 0); !errors.Is(err, fs.ErrPermission) {
		t.Errorf("err = %v, want ErrPermission", err)
	}
}

func TestRoots(t *testing.T) {
	l := New(map[string]*os.Root{"tv": nil, "downloads": nil, "movies": nil}, nil, nil)
	if got := strings.Join(l.Roots(), ","); got != "downloads,movies,tv" {
		t.Errorf("roots = %s", got)
	}
}
