package organizer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/jfxdev/media-maid/internal/jobs"
	"github.com/jfxdev/media-maid/internal/parser"
)

// A wrong automatic placement is corrected by a manual identification: the
// file moves to the right title and the wrong entry is removed.
func TestOverrideMovesWrongPlacement(t *testing.T) {
	e := newEnv(t, "jellyfin")
	src := filepath.Join(e.downloads, "Inception.2010.1080p", "Inception.2010.1080p.mkv")
	write(t, src)
	wrong := filepath.Join(e.movies, "Inception (2010) [tmdbid-27205]", "Inception (2010) [tmdbid-27205].mkv")
	right := filepath.Join(e.movies, "The Matrix (1999) [tmdbid-603]", "The Matrix (1999) [tmdbid-603].mkv")
	if _, err := e.org.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !sameInode(t, src, wrong) {
		t.Fatal("setup: not placed")
	}
	ov := &Override{Kind: parser.Movie, TMDBID: 603}

	res, err := e.org.Organize(context.Background(), "Inception.2010.1080p", OrganizeOptions{Explicit: true, Preview: true, Override: ov})
	if err != nil || len(res) != 1 {
		t.Fatalf("preview = %+v, %v", res, err)
	}
	if r := res[0]; r.Status != jobs.StatusPlanned || r.ResolvedBy != ByManual || r.Previous != "movies/Inception (2010) [tmdbid-27205]/Inception (2010) [tmdbid-27205].mkv" {
		t.Errorf("preview = %+v", r)
	}
	if _, err := os.Stat(wrong); err != nil {
		t.Fatal("preview removed the old placement")
	}

	e.syncer.paths = nil
	res, err = e.org.Organize(context.Background(), "Inception.2010.1080p", OrganizeOptions{Explicit: true, Override: ov})
	if err != nil || len(res) != 1 {
		t.Fatalf("organize = %+v, %v", res, err)
	}
	if r := res[0]; r.Status != jobs.StatusLinked || r.Previous == "" || r.Dest != "movies/The Matrix (1999) [tmdbid-603]/The Matrix (1999) [tmdbid-603].mkv" {
		t.Errorf("organize = %+v", r)
	}
	if !sameInode(t, src, right) {
		t.Error("not placed under the manual identification")
	}
	if _, err := os.Lstat(wrong); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("wrong placement kept: %v", err)
	}
	fi, _ := os.Stat(src)
	if le, ok := e.org.ledger.Lookup(fi); !ok || le.ResolvedBy != ByManual || le.Dest != "The Matrix (1999) [tmdbid-603]/The Matrix (1999) [tmdbid-603].mkv" {
		t.Errorf("ledger = %+v, %v", le, ok)
	}
	if len(e.syncer.paths) != 2 {
		t.Errorf("sync paths = %v, want the new and the old folder", e.syncer.paths)
	}
	// Automatic runs keep the corrected placement.
	res, _ = e.org.Scan(context.Background())
	if len(res) != 1 || res[0].Status != jobs.StatusExists {
		t.Errorf("rescan = %+v", res)
	}
}

// The old placement is only removed while it still is the same file.
func TestOverrideKeepsForeignFile(t *testing.T) {
	e := newEnv(t, "jellyfin")
	src := filepath.Join(e.downloads, "Inception.2010.mkv")
	write(t, src)
	wrong := filepath.Join(e.movies, "Inception (2010) [tmdbid-27205]", "Inception (2010) [tmdbid-27205].mkv")
	e.org.Scan(context.Background())
	os.Remove(wrong)
	write(t, wrong) // the user put another file there
	res, err := e.org.Organize(context.Background(), "Inception.2010.mkv", OrganizeOptions{Explicit: true, Override: &Override{Kind: parser.Movie, TMDBID: 603}})
	if err != nil || len(res) != 1 || res[0].Status != jobs.StatusLinked || res[0].Previous != "" {
		t.Fatalf("organize = %+v, %v", res, err)
	}
	if b, _ := os.ReadFile(wrong); string(b) != wrong {
		t.Error("a file that is not this download was removed")
	}
}

func TestOverrideResolution(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name   string
		noMeta bool
		file   string
		ov     Override
		err    error
		status string
		dest   string
	}{
		{name: "tmdb id", file: "Some.Garbage.Name.mkv", ov: Override{Kind: parser.Movie, TMDBID: 129},
			status: jobs.StatusPlanned, dest: "movies/Spirited Away (2001) [tmdbid-129]/Spirited Away (2001) [tmdbid-129].mkv"},
		{name: "typed title wins over the tmdb title", file: "Some.Garbage.Name.mkv", ov: Override{Kind: parser.Movie, TMDBID: 129, Title: "Chihiro"},
			status: jobs.StatusPlanned, dest: "movies/Chihiro (2001) [tmdbid-129]/Chihiro (2001) [tmdbid-129].mkv"},
		{name: "title confirmed by search", file: "x.mkv", ov: Override{Kind: parser.Movie, Title: "Spirited Away"},
			status: jobs.StatusPlanned, dest: "movies/Spirited Away (2001) [tmdbid-129]/Spirited Away (2001) [tmdbid-129].mkv"},
		{name: "title unknown to tmdb used as typed", file: "x.mkv", ov: Override{Kind: parser.Movie, Title: "Home Video", Year: 2003},
			status: jobs.StatusPlanned, dest: "movies/Home Video (2003)/Home Video (2003).mkv"},
		{name: "series keeps the parsed episode", file: "BB.S02E03.mkv", ov: Override{Kind: parser.Episode, TMDBID: 1396},
			status: jobs.StatusPlanned, dest: "tv/Breaking Bad (2008) [tmdbid-1396]/Season 02/Breaking Bad S02E03.mkv"},
		{name: "episode needs numbers", file: "Some.Movie.2001.mkv", ov: Override{Kind: parser.Episode, TMDBID: 1396},
			status: jobs.StatusSkipped},
		{name: "episode read as movie", file: "Show.S01E01.mkv", ov: Override{Kind: parser.Movie, TMDBID: 603},
			status: jobs.StatusPlanned, dest: "movies/The Matrix (1999) [tmdbid-603]/The Matrix (1999) [tmdbid-603].mkv"},
		{name: "unknown id", file: "x.mkv", ov: Override{Kind: parser.Movie, TMDBID: 9}, err: ErrOverrideNotFound},
		{name: "tmdb down", file: "x.mkv", ov: Override{Kind: parser.Movie, TMDBID: 500}, err: ErrMetadataUnavailable},
		{name: "bad kind", file: "x.mkv", ov: Override{Kind: "music", Title: "X"}, err: ErrInvalidOverride},
		{name: "id without tmdb needs a title", noMeta: true, file: "x.mkv", ov: Override{Kind: parser.Movie, TMDBID: 603}, err: ErrInvalidOverride},
		{name: "id without tmdb is a tag", noMeta: true, file: "x.mkv", ov: Override{Kind: parser.Movie, TMDBID: 603, Title: "The Matrix", Year: 1999},
			status: jobs.StatusPlanned, dest: "movies/The Matrix (1999) [tmdbid-603]/The Matrix (1999) [tmdbid-603].mkv"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t, "jellyfin", func(o *Options) {
				if tt.noMeta {
					o.Meta = nil
				}
			})
			write(t, filepath.Join(e.downloads, tt.file))
			ov := tt.ov
			res, err := e.org.Organize(ctx, tt.file, OrganizeOptions{Explicit: true, Preview: true, Override: &ov})
			if tt.err != nil {
				if !errors.Is(err, tt.err) {
					t.Fatalf("err = %v, want %v", err, tt.err)
				}
				return
			}
			if err != nil || len(res) != 1 {
				t.Fatalf("Organize = %+v, %v", res, err)
			}
			if res[0].Status != tt.status || res[0].Dest != tt.dest {
				t.Errorf("result = %+v, want %s %s", res[0], tt.status, tt.dest)
			}
		})
	}
}
