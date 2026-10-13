package organizer

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/jfxdev/media-maid/internal/jobs"
)

func withExtras(o *Options) { o.LinkExtras = true }

func TestExtras(t *testing.T) {
	tests := []struct {
		name  string
		mode  string
		files []string
		want  map[string]string // source -> library path (movies/ or tv/), "" = not linked
	}{
		{
			name: "movie on jellyfin",
			mode: "jellyfin",
			files: []string{
				"Inception.2010.1080p/Inception.2010.1080p.mkv",
				"Inception.2010.1080p/Featurettes/Dream.Levels.mkv",
				"Inception.2010.1080p/Behind.The.Scenes/Making Of.mkv",
				"Inception.2010.1080p/Sample/inception-sample.mkv",
				"Inception.2010.1080p/Extras/Sample.mkv",
			},
			want: map[string]string{
				"Inception.2010.1080p/Featurettes/Dream.Levels.mkv":    "movies/Inception (2010) [tmdbid-27205]/Featurettes/Dream.Levels.mkv",
				"Inception.2010.1080p/Behind.The.Scenes/Making Of.mkv": "movies/Inception (2010) [tmdbid-27205]/Behind The Scenes/Making Of.mkv",
				"Inception.2010.1080p/Sample/inception-sample.mkv":     "",
				"Inception.2010.1080p/Extras/Sample.mkv":               "",
			},
		},
		{
			name: "movie on plex",
			mode: "plex",
			files: []string{
				"Inception.2010.1080p/Inception.2010.1080p.mkv",
				"Inception.2010.1080p/Bonus/Gag Reel.mkv",
			},
			want: map[string]string{
				"Inception.2010.1080p/Bonus/Gag Reel.mkv": "movies/Inception (2010) {tmdb-27205}/Other/Gag Reel.mkv",
			},
		},
		{
			name: "season pack goes to the show folder",
			mode: "jellyfin",
			files: []string{
				"Show.S01.1080p/Show.S01E01.1080p.mkv",
				"Show.S01.1080p/Show.S01E02.1080p.mkv",
				"Show.S01.1080p/Extras/Bloopers.mkv",
			},
			want: map[string]string{
				"Show.S01.1080p/Extras/Bloopers.mkv": "tv/Show/Extras/Bloopers.mkv",
			},
		},
		{
			name: "extras of a folder holding several titles",
			mode: "jellyfin",
			files: []string{
				"Inception.2010.mkv",
				"Dune.2021.mkv",
				"Extras/Interview.mkv",
			},
			want: map[string]string{"Extras/Interview.mkv": ""},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t, tt.mode, withExtras)
			for _, f := range tt.files {
				write(t, filepath.Join(e.downloads, filepath.FromSlash(f)))
			}
			res, err := e.org.Scan(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			got := map[string]Result{}
			for _, r := range res {
				got[r.Source] = r
			}
			for src, dest := range tt.want {
				r, ok := got[src]
				if dest == "" {
					if ok && r.Status != jobs.StatusSkipped {
						t.Errorf("%s: %+v, want not linked", src, r)
					}
					continue
				}
				if r.Status != jobs.StatusLinked || r.Dest != dest || r.Kind != KindExtra {
					t.Errorf("%s: %+v, want linked to %s", src, r, dest)
					continue
				}
				lib, rel, _ := cutLib(dest)
				root := e.movies
				if lib == "tv" {
					root = e.tv
				}
				if !sameInode(t, filepath.Join(e.downloads, filepath.FromSlash(src)), filepath.Join(root, filepath.FromSlash(rel))) {
					t.Errorf("%s: not hardlinked", src)
				}
			}
		})
	}
}

func cutLib(dest string) (string, string, bool) {
	for i := 0; i < len(dest); i++ {
		if dest[i] == '/' {
			return dest[:i], dest[i+1:], true
		}
	}
	return dest, "", false
}

// Extras deleted from the library are not linked again by automatic runs,
// and are ignored altogether without LINK_EXTRAS.
func TestExtrasRespectDeletion(t *testing.T) {
	e := newEnv(t, "jellyfin", withExtras)
	write(t, filepath.Join(e.downloads, "Inception.2010", "Inception.2010.mkv"))
	write(t, filepath.Join(e.downloads, "Inception.2010", "Trailers", "Teaser.mkv"))
	extra := filepath.Join(e.movies, "Inception (2010) [tmdbid-27205]", "Trailers", "Teaser.mkv")
	e.org.Scan(context.Background())
	if _, err := os.Stat(extra); err != nil {
		t.Fatalf("extra not linked: %v", err)
	}
	os.Remove(extra)
	res, _ := e.org.Scan(context.Background())
	for _, r := range res {
		if r.Kind == KindExtra && r.Status != jobs.StatusSkipped {
			t.Errorf("deleted extra: %+v", r)
		}
	}
	if _, err := os.Stat(extra); err == nil {
		t.Error("deleted extra linked again")
	}

	off := newEnv(t, "jellyfin")
	write(t, filepath.Join(off.downloads, "Inception.2010", "Inception.2010.mkv"))
	write(t, filepath.Join(off.downloads, "Inception.2010", "Trailers", "Teaser.mkv"))
	res, _ = off.org.Scan(context.Background())
	if len(res) != 1 {
		t.Errorf("without LINK_EXTRAS: %+v", res)
	}
}

// An extra stays in place (exists) on later scans, also after the video of
// its release was upgraded away.
func TestExtrasSurviveUpgrade(t *testing.T) {
	e := newEnv(t, "jellyfin", withExtras, withUpgrade)
	write(t, filepath.Join(e.downloads, "Inception.2010.720p", "Inception.2010.720p.mkv"))
	write(t, filepath.Join(e.downloads, "Inception.2010.720p", "Trailers", "Teaser.mkv"))
	e.org.Scan(context.Background())
	write(t, filepath.Join(e.downloads, "Inception.2010.2160p", "Inception.2010.2160p.mkv"))
	e.org.Scan(context.Background())
	res, _ := e.org.Scan(context.Background())
	for _, r := range res {
		if r.Kind == KindExtra && r.Status != jobs.StatusExists {
			t.Errorf("extra after upgrade: %+v", r)
		}
	}
}
