package naming

import (
	"strings"
	"testing"

	"github.com/jfxdev/media-maid/internal/parser"
)

func TestSanitize(t *testing.T) {
	tests := map[string]string{
		"Mission: Impossible":      "Mission - Impossible",
		"../../etc/passwd":         "etc passwd",
		"..":                       "Unknown",
		"a/b\\c":                   "a b c",
		"Ação\x00\x1f":             "Ação",
		"  ...hidden  ":            "hidden",
		"AC/DC: Let There Be Rock": "AC DC - Let There Be Rock",
	}
	for in, want := range tests {
		if got := Sanitize(in); got != want {
			t.Errorf("Sanitize(%q) = %q, want %q", in, got, want)
		}
	}
	if got := Sanitize(strings.Repeat("a", 500)); len(got) > maxComponent {
		t.Errorf("not truncated: %d", len(got))
	}
}

func TestPaths(t *testing.T) {
	movie := parser.Media{Kind: parser.Movie, Title: "Inception", Year: 2010}
	if got := MoviePath(movie, "[tmdbid-27205]", "", ".MKV"); got != "Inception (2010) [tmdbid-27205]/Inception (2010) [tmdbid-27205].mkv" {
		t.Errorf("MoviePath = %q", got)
	}
	if got := MoviePath(movie, "", "", "mkv"); got != "Inception (2010)/Inception (2010).mkv" {
		t.Errorf("MoviePath no tag = %q", got)
	}
	ep := parser.Media{Kind: parser.Episode, Title: "Breaking Bad", Year: 2008, Season: 1, Episodes: []int{2, 3}}
	if got := EpisodePath(ep, "{tmdb-1396}", ".mkv"); got != "Breaking Bad (2008) {tmdb-1396}/Season 01/Breaking Bad S01E02-E03.mkv" {
		t.Errorf("EpisodePath = %q", got)
	}
	daily := parser.Media{Kind: parser.Episode, Title: "The Daily Show", Season: 2024, AirDate: "2024-03-15"}
	if got := EpisodePath(daily, "", ".mkv"); got != "The Daily Show/Season 2024/The Daily Show 2024-03-15.mkv" {
		t.Errorf("EpisodePath air date = %q", got)
	}
	evil := parser.Media{Kind: parser.Movie, Title: "../../../root/.ssh/x"}
	if got := MoviePath(evil, "", "", ".mkv"); strings.Contains(got, "..") || strings.Count(got, "/") != 1 {
		t.Errorf("escaping path produced: %q", got)
	}
	if got := SubtitlePath("Inception (2010)/Inception (2010).mkv", "pt-BR", true, false, ".SRT"); got != "Inception (2010)/Inception (2010).pt-BR.forced.srt" {
		t.Errorf("SubtitlePath = %q", got)
	}
}

func TestMovieEditionAndPart(t *testing.T) {
	m := parser.Media{Kind: parser.Movie, Title: "Avatar", Year: 2009, Edition: "Extended Cut"}
	tests := []struct {
		media        parser.Media
		idTag, edTag string
		want         string
	}{
		{m, "[tmdbid-19995]", "", "Avatar (2009) [tmdbid-19995]/Avatar (2009) [tmdbid-19995] - Extended Cut.mkv"},
		{m, "{tmdb-19995}", "{edition-Extended Cut}", "Avatar (2009) {tmdb-19995} {edition-Extended Cut}/Avatar (2009) {tmdb-19995} {edition-Extended Cut}.mkv"},
		{parser.Media{Kind: parser.Movie, Title: "Movie", Year: 2001, Part: 2}, "", "", "Movie (2001)/Movie (2001) - part2.mkv"},
		{parser.Media{Kind: parser.Movie, Title: "Movie", Edition: "Director's Cut", Part: 1}, "", "", "Movie/Movie - Director's Cut - part1.mkv"},
	}
	for _, tt := range tests {
		if got := MoviePath(tt.media, tt.idTag, tt.edTag, ".mkv"); got != tt.want {
			t.Errorf("MoviePath(%+v) = %q, want %q", tt.media, got, tt.want)
		}
	}
}
