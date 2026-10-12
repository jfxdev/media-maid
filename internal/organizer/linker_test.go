package organizer

import (
	"os"
	"path/filepath"
	"testing"

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
	if st, err := place(root, "Movie (2000)/Movie (2000).mkv", f, fi); st != jobs.StatusExists || err != nil {
		t.Errorf("place over copy = %s, %v; want exists", st, err)
	}
}
