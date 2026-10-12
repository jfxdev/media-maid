package organizer

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/jfxdev/media-maid/internal/jobs"
)

// The EXDEV fallback is exercised directly: the test environment has no
// second filesystem to force a cross-device link.
func TestCopyFallback(t *testing.T) {
	src := filepath.Join(t.TempDir(), "a.mkv")
	os.WriteFile(src, []byte("payload"), 0o644)
	dst := t.TempDir()
	root, _ := os.OpenRoot(dst)
	defer root.Close()
	os.MkdirAll(filepath.Join(dst, "Movie (2000)"), 0o755)

	f, _ := os.Open(src)
	defer f.Close()
	f.Read(make([]byte, 3)) // copy must rewind
	fi, _ := f.Stat()
	st, err := copyInto(root, "Movie (2000)/Movie (2000).mkv", f, fi)
	if err != nil || st != jobs.StatusCopied {
		t.Fatalf("copyInto = %s, %v", st, err)
	}
	b, _ := os.ReadFile(filepath.Join(dst, "Movie (2000)", "Movie (2000).mkv"))
	if string(b) != "payload" {
		t.Errorf("content = %q", b)
	}
	entries, _ := os.ReadDir(filepath.Join(dst, "Movie (2000)"))
	if len(entries) != 1 {
		t.Errorf("temp file left behind: %v", entries)
	}
	st, _ = copyInto(root, "Movie (2000)/Movie (2000).mkv", f, fi)
	if st != jobs.StatusConflict {
		t.Errorf("second copy = %s, want conflict", st)
	}
	// place recognises the earlier copy as already done.
	if st, err := place(root, "Movie (2000)/Movie (2000).mkv", f, fi, false); st != jobs.StatusExists || err != nil {
		t.Errorf("place over copy = %s, %v; want exists", st, err)
	}
}

func TestPlaceOverwrite(t *testing.T) {
	dst := t.TempDir()
	root, _ := os.OpenRoot(dst)
	defer root.Close()
	rel := "Movie (2000)/Movie (2000).mkv"
	old := filepath.Join(dst, "Movie (2000)", "Movie (2000).mkv")
	os.MkdirAll(filepath.Dir(old), 0o755)
	os.WriteFile(old, []byte("720p"), 0o644)

	src := filepath.Join(t.TempDir(), "new.mkv")
	os.WriteFile(src, []byte("1080p"), 0o644)
	f, _ := os.Open(src)
	defer f.Close()
	fi, _ := f.Stat()

	if st, _ := place(root, rel, f, fi, false); st != jobs.StatusConflict {
		t.Fatalf("without overwrite = %s, want conflict", st)
	}
	if st, err := place(root, rel, f, fi, true); st != jobs.StatusReplaced || err != nil {
		t.Fatalf("overwrite = %s, %v; want replaced", st, err)
	}
	got, _ := os.Stat(old)
	if !os.SameFile(got, fi) {
		t.Error("destination is not a hardlink of the new source")
	}
	entries, _ := os.ReadDir(filepath.Dir(old))
	if len(entries) != 1 {
		t.Errorf("temp file left behind: %v", entries)
	}
	if st, _ := place(root, rel, f, fi, true); st != jobs.StatusExists {
		t.Errorf("second overwrite = %s, want exists", st)
	}

	// Directories and symlinks at the destination are never replaced.
	os.Remove(old)
	os.Mkdir(old, 0o755)
	if st, _ := place(root, rel, f, fi, true); st != jobs.StatusConflict {
		t.Errorf("overwrite of a directory = %s, want conflict", st)
	}
	os.Remove(old)
	os.Symlink("/etc/passwd", old)
	if st, _ := place(root, rel, f, fi, true); st != jobs.StatusConflict {
		t.Errorf("overwrite of a symlink = %s, want conflict", st)
	}
	if b, _ := os.ReadFile("/etc/passwd"); string(b) == "1080p" {
		t.Fatal("overwrite followed a symlink")
	}
}

// Regression: a file at the old fixed temporary name is neither reused nor
// removed.
func TestPlaceKeepsForeignTemp(t *testing.T) {
	dst := t.TempDir()
	root, _ := os.OpenRoot(dst)
	defer root.Close()
	dir := filepath.Join(dst, "Movie (2000)")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "Movie (2000).mkv"), []byte("old 720p"), 0o644)
	foreign := filepath.Join(dir, ".Movie (2000).mkv.mediamaid-tmp")
	os.WriteFile(foreign, []byte("not ours"), 0o644)

	src := filepath.Join(t.TempDir(), "new.mkv")
	os.WriteFile(src, []byte("new 1080p"), 0o644)
	f, _ := os.Open(src)
	defer f.Close()
	fi, _ := f.Stat()
	if st, err := place(root, "Movie (2000)/Movie (2000).mkv", f, fi, true); st != jobs.StatusReplaced || err != nil {
		t.Fatalf("overwrite = %s, %v", st, err)
	}
	if b, _ := os.ReadFile(foreign); string(b) != "not ours" {
		t.Error("an unrelated temporary file was removed or overwritten")
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 2 {
		t.Errorf("entries = %v", entries)
	}
}

// Regression: if the destination changes after it was checked (here: to a
// symlink, then to a different file), the swap is undone.
func TestSwapInRechecksDestination(t *testing.T) {
	dst := t.TempDir()
	root, _ := os.OpenRoot(dst)
	defer root.Close()
	d, _ := root.Open(".")
	defer d.Close()
	dest := filepath.Join(dst, "Movie.mkv")
	os.WriteFile(dest, []byte("checked"), 0o644)
	checked, _ := os.Lstat(dest)

	for name, swap := range map[string]func(){
		"symlink":    func() { os.Remove(dest); os.Symlink("/etc/passwd", dest) },
		"other file": func() { os.Remove(dest); os.WriteFile(dest, []byte("a newer file"), 0o644) },
	} {
		swap()
		before, _ := os.Lstat(dest)
		os.WriteFile(filepath.Join(dst, ".staged"), []byte("staged"), 0o644)
		st, err := swapIn(root, d, ".staged", "Movie.mkv", checked)
		if st != jobs.StatusConflict || err != nil {
			t.Errorf("%s: swapIn = %s, %v; want conflict", name, st, err)
		}
		after, _ := os.Lstat(dest)
		if !os.SameFile(before, after) {
			t.Errorf("%s: destination was replaced", name)
		}
		os.Remove(filepath.Join(dst, ".staged"))
	}
}

// Regression: a writer that replaces the destination again during the
// rollback must not lose its file.
func TestSwapInRollbackRace(t *testing.T) {
	dst := t.TempDir()
	root, _ := os.OpenRoot(dst)
	defer root.Close()
	d, _ := root.Open(".")
	defer d.Close()
	dest := filepath.Join(dst, "Movie.mkv")
	os.WriteFile(dest, []byte("checked"), 0o644)
	checked, _ := os.Lstat(dest)
	os.Remove(dest)
	os.Symlink("/etc/passwd", dest) // changed after the check
	os.WriteFile(filepath.Join(dst, ".staged"), []byte("staged"), 0o644)

	testHookRollback = func() {
		os.Remove(dest)
		os.WriteFile(dest, []byte("writer's file"), 0o644)
	}
	defer func() { testHookRollback = func() {} }()

	st, err := swapIn(root, d, ".staged", "Movie.mkv", checked)
	if st != jobs.StatusError || err == nil {
		t.Fatalf("swapIn = %s, %v; want error", st, err)
	}
	if b, _ := os.ReadFile(filepath.Join(dst, ".staged")); string(b) != "writer's file" {
		t.Errorf("the writer's file was lost: %q", b)
	}
	if target, _ := os.Readlink(dest); target != "/etc/passwd" {
		t.Errorf("the symlink was not restored: %q", target)
	}
}

// Without RENAME_EXCHANGE, overwrite is refused instead of raced.
func TestSwapInNoExchange(t *testing.T) {
	dst := t.TempDir()
	root, _ := os.OpenRoot(dst)
	defer root.Close()
	d, _ := root.Open(".")
	defer d.Close()
	dest := filepath.Join(dst, "Movie.mkv")
	os.WriteFile(dest, []byte("checked"), 0o644)
	checked, _ := os.Lstat(dest)
	os.WriteFile(filepath.Join(dst, ".staged"), []byte("staged"), 0o644)

	renameat2 = func(int, string, int, string, uint) error { return unix.EINVAL }
	defer func() { renameat2 = unix.Renameat2 }()

	if st, err := swapIn(root, d, ".staged", "Movie.mkv", checked); st != jobs.StatusError || !errors.Is(err, errNoAtomicReplace) {
		t.Fatalf("swapIn = %s, %v", st, err)
	}
	if b, _ := os.ReadFile(dest); string(b) != "checked" {
		t.Error("destination changed")
	}
	if entries, _ := os.ReadDir(dst); len(entries) != 1 {
		t.Errorf("staged file left behind: %v", entries)
	}
}
