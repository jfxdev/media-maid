package organizer

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/jfxdev/media-maid/internal/jobs"
	"github.com/jfxdev/media-maid/internal/metadata"
	"github.com/jfxdev/media-maid/internal/parser"
)

func analyze(t *testing.T, name string) parser.Analysis {
	t.Helper()
	a, ok := parser.Analyze(name)
	if !ok {
		t.Fatalf("not parsed: %s", name)
	}
	return a
}

// A manual identification is remembered for the name's reading: the next
// episode of the same show is placed with the same TMDB entry, while a
// preview remembers nothing.
func TestAliasRemembersManualIdentification(t *testing.T) {
	e := newEnv(t, "jellyfin")
	ov := &Override{Kind: parser.Episode, TMDBID: 1396}
	write(t, filepath.Join(e.downloads, "Breaking.Bad.S01E01.720p.mkv"))
	if _, err := e.org.Organize(context.Background(), "Breaking.Bad.S01E01.720p.mkv", OrganizeOptions{Explicit: true, Preview: true, Override: ov}); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.org.aliases.Get(aliasKey(analyze(t, "Breaking.Bad.S01E01.720p.mkv"))); ok {
		t.Fatal("a preview remembered the identification")
	}
	res, err := e.org.Organize(context.Background(), "Breaking.Bad.S01E01.720p.mkv", OrganizeOptions{Explicit: true, Override: ov})
	if err != nil || len(res) != 1 || res[0].Status != jobs.StatusLinked {
		t.Fatalf("manual = %+v, %v", res, err)
	}

	write(t, filepath.Join(e.downloads, "Breaking.Bad.S01E02.1080p.mkv"))
	res, err = e.org.Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var got *Result
	for i := range res {
		if res[i].Source == "Breaking.Bad.S01E02.1080p.mkv" {
			got = &res[i]
		}
	}
	want := "tv/Breaking Bad (2008) [tmdbid-1396]/Season 01/Breaking Bad S01E02.mkv"
	if got == nil || got.Status != jobs.StatusLinked || got.Dest != want || got.ResolvedBy != ByManual {
		t.Fatalf("next episode = %+v, want %s", got, want)
	}

	// Another reading (a year in the name) is not covered.
	if r := organizeOne(t, e, "Breaking.Bad.2008.S01E03.mkv"); r.ResolvedBy == ByManual {
		t.Errorf("different reading used the alias: %+v", r)
	}
}

// A title the user typed is remembered as typed; one that TMDB matched is
// remembered as that TMDB entry.
func TestRemembered(t *testing.T) {
	typed := &manual{fixed: true, ov: Override{Kind: parser.Movie, Title: "Home Video", Year: 2001}}
	if got := remembered(typed); got != typed.ov {
		t.Errorf("typed = %+v", got)
	}
	m := &manual{ov: Override{Kind: parser.Movie, Title: "matrix"}}
	m.match = &metadata.Result{Title: "The Matrix", Year: 1999, TMDBID: 603}
	if got := remembered(m); got != (Override{Kind: parser.Movie, TMDBID: 603}) {
		t.Errorf("matched = %+v", got)
	}
}

func TestAliasesExportImport(t *testing.T) {
	a := NewAliases()
	changes := 0
	a.OnChange = func() { changes++ }
	a.Put("episode|shameless|0|", Override{Kind: parser.Episode, TMDBID: 34307})
	a.Put("episode|shameless|0|", Override{Kind: parser.Episode, TMDBID: 34307})
	if changes != 1 {
		t.Errorf("changes = %d, want 1 (same identification again)", changes)
	}
	data, err := a.Export()
	if err != nil {
		t.Fatal(err)
	}
	b := NewAliases()
	if err := b.Import(data); err != nil {
		t.Fatal(err)
	}
	if ov, ok := b.Get("episode|shameless|0|"); !ok || ov.TMDBID != 34307 {
		t.Errorf("imported = %+v, %v", ov, ok)
	}
	bad := `{"movie|x|0|":{"kind":"book"},"movie|y|0|":{"kind":"movie","title":"a\u0000b"},"movie|z|0|":{"kind":"movie","tmdb_id":5}}`
	if err := b.Import([]byte(bad)); err != nil {
		t.Fatal(err)
	}
	if _, ok := b.Get("movie|x|0|"); ok {
		t.Error("invalid kind kept")
	}
	if _, ok := b.Get("movie|y|0|"); ok {
		t.Error("control character kept")
	}
	if _, ok := b.Get("movie|z|0|"); !ok {
		t.Error("valid entry dropped")
	}
}
