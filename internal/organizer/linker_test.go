package organizer

import (
	"context"
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
	// place recognises the earlier copy as already done, but only when the
	// caller confirms it is the recorded copy: equal size and mtime alone are
	// a conflict (another release may share both).
	ours := func(os.FileInfo) bool { return true }
	if st, err := place(root, "Movie (2000)/Movie (2000).mkv", f, fi, false, false, ours); st != jobs.StatusExists || err != nil {
		t.Errorf("place over recorded copy = %s, %v; want exists", st, err)
	}
	if st, err := place(root, "Movie (2000)/Movie (2000).mkv", f, fi, false, false, nil); st != jobs.StatusConflict || err != nil {
		t.Errorf("place over unrecorded file = %s, %v; want conflict", st, err)
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

	if st, _ := place(root, rel, f, fi, false, false, nil); st != jobs.StatusConflict {
		t.Fatalf("without overwrite = %s, want conflict", st)
	}
	if st, err := place(root, rel, f, fi, true, false, nil); st != jobs.StatusReplaced || err != nil {
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
	if st, _ := place(root, rel, f, fi, true, false, nil); st != jobs.StatusExists {
		t.Errorf("second overwrite = %s, want exists", st)
	}

	// Directories and symlinks at the destination are never replaced.
	os.Remove(old)
	os.Mkdir(old, 0o755)
	if st, _ := place(root, rel, f, fi, true, false, nil); st != jobs.StatusConflict {
		t.Errorf("overwrite of a directory = %s, want conflict", st)
	}
	os.Remove(old)
	os.Symlink("/etc/passwd", old)
	if st, _ := place(root, rel, f, fi, true, false, nil); st != jobs.StatusConflict {
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
	if st, err := place(root, "Movie (2000)/Movie (2000).mkv", f, fi, true, false, nil); st != jobs.StatusReplaced || err != nil {
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

	orig := renameat2
	renameat2 = func(int, string, int, string, uint) error { return unix.EINVAL }
	defer func() { renameat2 = orig }()

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

// With COPY_FALLBACK=false a file that cannot be hardlinked is an error, for
// a new destination and for an overwrite alike, and nothing is written.
func TestPlaceNoCopy(t *testing.T) {
	linkat = func(int, string, int, string, int) error { return unix.EXDEV }
	defer func() { linkat = unix.Linkat }()

	src := filepath.Join(t.TempDir(), "a.mkv")
	os.WriteFile(src, []byte("payload"), 0o644)
	f, _ := os.Open(src)
	defer f.Close()
	fi, _ := f.Stat()
	dst := t.TempDir()
	root, _ := os.OpenRoot(dst)
	defer root.Close()

	st, err := place(root, "New (2000)/New (2000).mkv", f, fi, false, true, nil)
	if st != jobs.StatusError || !errors.Is(err, errNoCopy) {
		t.Errorf("new destination = %s, %v; want error %v", st, err, errNoCopy)
	}
	if entries, _ := os.ReadDir(filepath.Join(dst, "New (2000)")); len(entries) != 0 {
		t.Errorf("something was written: %v", entries)
	}

	old := filepath.Join(dst, "Old (2000)", "Old (2000).mkv")
	os.MkdirAll(filepath.Dir(old), 0o755)
	os.WriteFile(old, []byte("720p"), 0o644)
	st, err = place(root, "Old (2000)/Old (2000).mkv", f, fi, true, true, nil)
	if st != jobs.StatusError || !errors.Is(err, errNoCopy) {
		t.Errorf("overwrite = %s, %v; want error %v", st, err, errNoCopy)
	}
	if b, _ := os.ReadFile(old); string(b) != "720p" {
		t.Errorf("destination changed: %q", b)
	}
	if entries, _ := os.ReadDir(filepath.Dir(old)); len(entries) != 1 {
		t.Errorf("temp file left behind: %v", entries)
	}

	// The default still copies.
	if st, err := place(root, "New (2000)/New (2000).mkv", f, fi, false, false, nil); st != jobs.StatusCopied || err != nil {
		t.Errorf("with copy fallback = %s, %v; want copied", st, err)
	}
}

// removeChecked deletes only the file that was inspected; anything else that
// took its name is put back.
func TestRemoveChecked(t *testing.T) {
	dst := t.TempDir()
	root, _ := os.OpenRoot(dst)
	defer root.Close()
	p := filepath.Join(dst, "Movie.mp4")

	os.WriteFile(p, []byte("old"), 0o644)
	checked, _ := os.Lstat(p)
	if err := removeChecked(root, "Movie.mp4", checked); err != nil {
		t.Fatalf("unchanged file: %v", err)
	}
	if _, err := os.Lstat(p); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("file kept: %v", err)
	}

	os.WriteFile(p, []byte("old"), 0o644)
	checked, _ = os.Lstat(p)
	os.Remove(p)
	os.WriteFile(p, []byte("someone else's"), 0o644)
	if err := removeChecked(root, "Movie.mp4", checked); !errors.Is(err, errChanged) {
		t.Fatalf("changed file: %v", err)
	}
	if b, _ := os.ReadFile(p); string(b) != "someone else's" {
		t.Errorf("changed file not restored: %q", b)
	}
	if entries, _ := os.ReadDir(dst); len(entries) != 1 {
		t.Errorf("temp entry left behind: %v", entries)
	}
}

// A library file is only "already placed" when it is the source's inode or
// the copy recorded in the ledger: a different file sharing size and mtime is
// a conflict, and a recorded copy later swapped for such a file is no longer
// recognised.
func TestSameSizeAndMTimeIsNotSameFile(t *testing.T) {
	e := newEnv(t, "jellyfin")
	src := filepath.Join(e.downloads, "Heat.1995.1080p.mkv")
	dest := filepath.Join(e.movies, "Heat (1995)", "Heat (1995).mkv")
	os.MkdirAll(filepath.Dir(dest), 0o755)
	os.WriteFile(src, []byte("AAAA"), 0o644)
	os.WriteFile(dest, []byte("BBBB"), 0o644)
	fi, _ := os.Stat(src)
	os.Chtimes(dest, fi.ModTime(), fi.ModTime())
	res, err := e.org.Scan(context.Background())
	if err != nil || len(res) != 1 || res[0].Status != jobs.StatusConflict {
		t.Fatalf("different content, same size and mtime = %+v, %v", res, err)
	}

	// A real copy (no hardlink possible) is recognised on the next run.
	os.Remove(dest)
	linkat = func(int, string, int, string, int) error { return unix.EXDEV }
	defer func() { linkat = unix.Linkat }()
	if res, _ := e.org.Scan(context.Background()); len(res) != 1 || res[0].Status != jobs.StatusCopied {
		t.Fatalf("copy = %+v", res)
	}
	if res, _ := e.org.Organize(context.Background(), "Heat.1995.1080p.mkv", OrganizeOptions{Explicit: true}); len(res) != 1 || res[0].Status != jobs.StatusExists {
		t.Fatalf("recorded copy = %+v", res)
	}

	// Swapped for another file with the same size and mtime: not our copy.
	// (Written next to it first, so the old inode number is not reused.)
	other := dest + ".new"
	os.WriteFile(other, []byte("CCCC"), 0o644)
	os.Chtimes(other, fi.ModTime(), fi.ModTime())
	os.Rename(other, dest)
	if res, _ := e.org.Organize(context.Background(), "Heat.1995.1080p.mkv", OrganizeOptions{Explicit: true}); len(res) != 1 || res[0].Status != jobs.StatusConflict {
		t.Fatalf("swapped copy = %+v", res)
	}
}

// A ledger copy entry without the library file's inode does not make a file
// with the source's size and mtime "already placed".
func TestCopyWithoutDestInoIsNotPlaced(t *testing.T) {
	e := newEnv(t, "jellyfin")
	src := filepath.Join(e.downloads, "Ronin.1998.1080p.mkv")
	dest := filepath.Join(e.movies, "Ronin (1998)", "Ronin (1998).mkv")
	os.MkdirAll(filepath.Dir(dest), 0o755)
	os.WriteFile(src, []byte("AAAA"), 0o644)
	os.WriteFile(dest, []byte("BBBB"), 0o644)
	fi, _ := os.Stat(src)
	os.Chtimes(dest, fi.ModTime(), fi.ModTime())
	e.org.ledger.Put(fi, LedgerEntry{Lib: "movies", Dest: "Ronin (1998)/Ronin (1998).mkv", Status: jobs.StatusCopied})
	if res, _ := e.org.Organize(context.Background(), "Ronin.1998.1080p.mkv", OrganizeOptions{Explicit: true}); len(res) != 1 || res[0].Status != jobs.StatusConflict {
		t.Fatalf("copy without DestIno = %+v", res)
	}
}
