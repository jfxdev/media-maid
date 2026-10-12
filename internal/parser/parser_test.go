package parser

import (
	"reflect"
	"testing"
)

func TestParse(t *testing.T) {
	tests := []struct {
		path string
		want Media
	}{
		{"Inception.2010.1080p.BluRay.x264-GROUP.mkv", Media{Kind: Movie, Title: "Inception", Year: 2010}},
		{"The Matrix (1999) [1080p]/The.Matrix.1999.mkv", Media{Kind: Movie, Title: "The Matrix", Year: 1999}},
		{"Blade.Runner.2049.2017.2160p.WEB-DL.mkv", Media{Kind: Movie, Title: "Blade Runner 2049", Year: 2017}},
		{"2001.A.Space.Odyssey.1968.mkv", Media{Kind: Movie, Title: "2001 A Space Odyssey", Year: 1968}},
		{"[YTS] Parasite (2019) [720p].mp4", Media{Kind: Movie, Title: "Parasite", Year: 2019}},
		{"Some.Movie.Without.Year.1080p.WEBRip.mkv", Media{Kind: Movie, Title: "Some Movie Without Year"}},
		{"Dune.Part.Two.2024.1080p/movie.mkv", Media{Kind: Movie, Title: "Dune Part Two", Year: 2024}},
		{"Breaking.Bad.S01E02.720p.HDTV.x264.mkv", Media{Kind: Episode, Title: "Breaking Bad", Season: 1, Episodes: []int{2}}},
		{"The.Office.US.S02E01E02.1080p.mkv", Media{Kind: Episode, Title: "The Office US", Season: 2, Episodes: []int{1, 2}}},
		{"Doctor Who (2005) - 3x05 - Evolution.avi", Media{Kind: Episode, Title: "Doctor Who", Year: 2005, Season: 3, Episodes: []int{5}}},
		{"Severance.S02.1080p.WEB/S02E03.mkv", Media{Kind: Episode, Title: "Severance", Season: 2, Episodes: []int{3}}},
		{"Dark/Season 1/E04.mkv", Media{Kind: Episode, Title: "Dark", Season: 1, Episodes: []int{4}}},
		{"Lost/Temporada 2/Episodio 07.mkv", Media{Kind: Episode, Title: "Lost", Season: 2, Episodes: []int{7}}},
		{"shogun.2024.s01e01.mkv", Media{Kind: Episode, Title: "Shogun", Year: 2024, Season: 1, Episodes: []int{1}}},
	}
	for _, tt := range tests {
		got, ok := Parse(tt.path)
		if !ok {
			t.Errorf("Parse(%q) not ok", tt.path)
			continue
		}
		if !reflect.DeepEqual(got, tt.want) {
			t.Errorf("Parse(%q) = %+v, want %+v", tt.path, got, tt.want)
		}
	}
}

func TestParseUnknown(t *testing.T) {
	if _, ok := Parse("1080p.mkv"); ok {
		t.Error("expected no result for a name with only release tags")
	}
}
