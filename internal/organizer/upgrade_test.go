package organizer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jfxdev/media-maid/internal/jobs"
)

func withUpgrade(o *Options) { o.AutoUpgrade = true }

func organizeAuto(t *testing.T, e *env, name string) Result {
	t.Helper()
	write(t, filepath.Join(e.downloads, name))
	res, err := e.org.Organize(context.Background(), name, OrganizeOptions{})
	if err != nil || len(res) == 0 {
		t.Fatalf("Organize(%q) = %+v, %v", name, res, err)
	}
	return res[0]
}

// Releases arriving in later runs are compared with the one in the library.
func TestUpgradeAcrossRuns(t *testing.T) {
	const (
		low  = "The.Matrix.1999.720p.WEB-DL.mkv"
		high = "The.Matrix.1999.1080p.BluRay.mkv"
		top  = "The.Matrix.1999.2160p.REMUX.mkv"
		bad  = "The.Matrix.1999.480p.DVDRip.mkv"
	)
	dest := filepath.Join("The Matrix (1999)", "The Matrix (1999).mkv")
	tests := []struct {
		name    string
		upgrade bool
		second  string
		status  string
		winner  string
	}{
		{"better release without AUTO_UPGRADE", false, high, jobs.StatusConflict, low},
		{"better release with AUTO_UPGRADE", true, high, jobs.StatusUpgraded, high},
		{"worse release", true, bad, jobs.StatusDuplicate, low},
		{"same release quality", true, "The.Matrix.1999.720p.WEB-DL-OTHER.mkv", jobs.StatusDuplicate, low},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t, "jellyfin", func(o *Options) { o.AutoUpgrade = tt.upgrade })
			if r := organizeAuto(t, e, low); r.Status != jobs.StatusLinked {
				t.Fatalf("first = %+v", r)
			}
			r := organizeAuto(t, e, tt.second)
			if r.Status != tt.status {
				t.Fatalf("second = %+v, want %s", r, tt.status)
			}
			if !sameInode(t, filepath.Join(e.downloads, tt.winner), filepath.Join(e.movies, dest)) {
				t.Errorf("library holds the wrong release, want %s", tt.winner)
			}
		})
	}

	// After an upgrade the old download is not linked again, and a later,
	// even better release upgrades once more.
	e := newEnv(t, "jellyfin", withUpgrade)
	organizeAuto(t, e, low)
	organizeAuto(t, e, high)
	res, _ := e.org.Scan(context.Background())
	st := statuses(res)
	if st[low] != jobs.StatusSkipped || st[high] != jobs.StatusExists {
		t.Errorf("rescan = %v", st)
	}
	if r := organizeAuto(t, e, top); r.Status != jobs.StatusUpgraded {
		t.Errorf("third = %+v", r)
	}
	for _, f := range []string{low, high, top} {
		if _, err := os.Stat(filepath.Join(e.downloads, f)); err != nil {
			t.Errorf("download %s touched: %v", f, err)
		}
	}
}

// A release with another extension is the same library entry: it is never
// placed next to the first one, and an upgrade removes the old file.
func TestUpgradeOtherExtension(t *testing.T) {
	low, high := "The.Matrix.1999.720p.mp4", "The.Matrix.1999.1080p.mkv"
	oldDest := filepath.Join("The Matrix (1999)", "The Matrix (1999).mp4")
	newDest := filepath.Join("The Matrix (1999)", "The Matrix (1999).mkv")

	e := newEnv(t, "jellyfin")
	organizeAuto(t, e, low)
	if r := organizeAuto(t, e, high); r.Status != jobs.StatusConflict || !strings.Contains(r.Error, "AUTO_UPGRADE") {
		t.Errorf("without AUTO_UPGRADE = %+v", r)
	}
	if _, err := os.Stat(filepath.Join(e.movies, newDest)); err == nil {
		t.Error("second version placed next to the first")
	}

	e = newEnv(t, "jellyfin", withUpgrade)
	organizeAuto(t, e, low)
	write(t, filepath.Join(e.downloads, high))
	write(t, filepath.Join(e.downloads, "The.Matrix.1999.1080p.en.srt"))
	res, _ := e.org.Organize(context.Background(), e.downloads, OrganizeOptions{})
	st := map[string]Result{}
	for _, r := range res {
		st[r.Source] = r
	}
	if r := st[high]; r.Status != jobs.StatusUpgraded || r.Previous != "movies/The Matrix (1999)/The Matrix (1999).mp4" {
		t.Errorf("upgrade = %+v", r)
	}
	if !sameInode(t, filepath.Join(e.downloads, high), filepath.Join(e.movies, newDest)) {
		t.Error("new release not linked")
	}
	if _, err := os.Lstat(filepath.Join(e.movies, oldDest)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("old release kept: %v", err)
	}

	// The same pair in one run is deduplicated.
	e = newEnv(t, "jellyfin")
	write(t, filepath.Join(e.downloads, low))
	write(t, filepath.Join(e.downloads, high))
	res, _ = e.org.Scan(context.Background())
	if s := statuses(res); s[high] != jobs.StatusLinked || s[low] != jobs.StatusDuplicate {
		t.Errorf("same run = %v", s)
	}
}

// A file Media Maid did not place is never upgraded automatically.
func TestUpgradeLeavesForeignFiles(t *testing.T) {
	e := newEnv(t, "jellyfin", withUpgrade)
	foreign := filepath.Join(e.movies, "The Matrix (1999)", "The Matrix (1999).mp4")
	write(t, foreign)
	r := organizeAuto(t, e, "The.Matrix.1999.2160p.REMUX.mkv")
	if r.Status != jobs.StatusConflict || !strings.Contains(r.Error, "another release") {
		t.Errorf("result = %+v", r)
	}
	if b, _ := os.ReadFile(foreign); string(b) != foreign {
		t.Error("foreign file changed")
	}
	// An explicit overwrite is the user's decision.
	res, _ := e.org.Organize(context.Background(), "The.Matrix.1999.2160p.REMUX.mkv", OrganizeOptions{Explicit: true, Overwrite: true})
	if len(res) != 1 || res[0].Status != jobs.StatusReplaced || res[0].Previous == "" {
		t.Errorf("overwrite = %+v", res)
	}
	if _, err := os.Lstat(foreign); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("foreign file kept after overwrite: %v", err)
	}
}

// An upgrade's subtitles replace the old release's ones.
func TestUpgradeReplacesSubtitles(t *testing.T) {
	e := newEnv(t, "jellyfin", withUpgrade)
	write(t, filepath.Join(e.downloads, "A", "The.Matrix.1999.720p.en.srt"))
	write(t, filepath.Join(e.downloads, "A", "The.Matrix.1999.720p.mkv"))
	e.org.Organize(context.Background(), "A", OrganizeOptions{})
	write(t, filepath.Join(e.downloads, "B", "The.Matrix.1999.1080p.en.srt"))
	write(t, filepath.Join(e.downloads, "B", "The.Matrix.1999.1080p.mkv"))
	res, _ := e.org.Organize(context.Background(), "B", OrganizeOptions{})
	for _, r := range res {
		if r.Status != jobs.StatusUpgraded && r.Status != jobs.StatusReplaced {
			t.Errorf("%+v", r)
		}
	}
	sub := filepath.Join(e.movies, "The Matrix (1999)", "The Matrix (1999).en.srt")
	if !sameInode(t, filepath.Join(e.downloads, "B", "The.Matrix.1999.1080p.en.srt"), sub) {
		t.Error("subtitle of the new release not placed")
	}
}
