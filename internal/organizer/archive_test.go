package organizer

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/jfxdev/media-maid/internal/jobs"
)

func TestFilterArchive(t *testing.T) {
	var f Filter
	for _, tt := range []struct {
		path string
		want bool
	}{
		{"Movie.2010.1080p/movie.rar", true},
		{"Movie.2010.1080p/movie.part1.rar", true},
		{"Movie.2010.1080p/movie.part01.rar", true},
		{"Movie.2010.1080p/movie.part001.rar", true},
		{"Movie.2010.1080p/movie.part02.rar", false},
		{"Movie.2010.1080p/movie.part10.rar", false},
		{"Movie.2010.1080p/movie.r00", false},
		{"Movie.2010.1080p/movie.r01", false},
		{"Movie.2010.1080p/MOVIE.ZIP", true},
		{"Movie.2010.1080p/movie.7z", true},
		{"Movie.2010.1080p/movie.7z.001", true},
		{"Movie.2010.1080p/movie.7z.002", false},
		{"Movie.2010.1080p/movie.zip.001", true},
		{"Movie.2010.1080p/movie.rar.!qB", false},
		{"Movie.2010.1080p/.movie.rar", false},
		{"Movie.2010.1080p/Sample/movie-sample.rar", false},
		{"Movie.2010.1080p/Featurettes/making.of.rar", false},
		{"Movie.2010.1080p/movie.mkv", false},
		{"Movie.2010.1080p/movie.001", false},
	} {
		if got := f.Archive(tt.path); got != tt.want {
			t.Errorf("Archive(%q) = %v, want %v", tt.path, got, tt.want)
		}
	}
}

// A compressed release is not extracted: it fails explicitly (pending and
// notified once) instead of being ignored, unless a video was already
// extracted next to it or it does not look like a video release.
func TestArchiveFailsExplicitly(t *testing.T) {
	var told []Result
	e := newEnv(t, "jellyfin", func(o *Options) { o.Notify = func(r Result) { told = append(told, r) } })
	packed := "Movie.2010.1080p.BluRay.x264-GRP"
	for _, n := range []string{"grp-movie.part01.rar", "grp-movie.part02.rar", "grp-movie.r00", "Sample/grp-movie-sample.mkv"} {
		write(t, filepath.Join(e.downloads, packed, n))
	}
	extracted := "Other.2011.1080p.WEB-DL.x264-GRP"
	for _, n := range []string{"other.rar", "other.r00", extracted + ".mkv"} {
		write(t, filepath.Join(e.downloads, extracted, n))
	}
	write(t, filepath.Join(e.downloads, "Show.S01E01.720p.HDTV.x264.zip"))
	write(t, filepath.Join(e.downloads, "Some.Software.v2.rar"))

	for i := 0; i < 2; i++ {
		if _, err := e.org.Scan(context.Background()); err != nil {
			t.Fatal(err)
		}
	}

	failed := map[string]bool{}
	for _, it := range e.org.Pending() {
		if it.Status != jobs.StatusError || it.Error != errArchiveMsg {
			t.Errorf("pending item = %+v", it)
		}
		failed[it.Source] = true
	}
	want := []string{packed + "/grp-movie.part01.rar", "Show.S01E01.720p.HDTV.x264.zip"}
	if len(failed) != len(want) {
		t.Errorf("pending = %v, want %v", failed, want)
	}
	for _, w := range want {
		if !failed[w] {
			t.Errorf("%s is not pending: %v", w, failed)
		}
	}
	var errs, linked int
	for _, r := range told {
		switch r.Status {
		case jobs.StatusError:
			errs++
		case jobs.StatusLinked:
			linked++
		}
	}
	// Each archive told once across both scans; the extracted video placed.
	if errs != 2 || linked != 1 {
		t.Errorf("notifications = %+v", told)
	}
}

// Previews report the archive without recording it.
func TestArchivePreview(t *testing.T) {
	e := newEnv(t, "jellyfin")
	write(t, filepath.Join(e.downloads, "Movie.2010.1080p.BluRay.x264-GRP", "grp-movie.rar"))
	res, err := e.org.Organize(context.Background(), e.downloads, OrganizeOptions{Preview: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0].Status != jobs.StatusError || res[0].Error != errArchiveMsg {
		t.Errorf("results = %+v", res)
	}
	if items := e.org.Pending(); len(items) != 0 {
		t.Errorf("pending = %+v", items)
	}
}
