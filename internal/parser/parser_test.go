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
		// Separators, hyphens, acronyms and Unicode noise.
		{"Spider-Man.No.Way.Home.2021.1080p.WEB-DL.DDP5.1.x264-GROUP.mkv", Media{Kind: Movie, Title: "Spider-Man No Way Home", Year: 2021}},
		{"Marvels.Agents.of.S.H.I.E.L.D.S01E01.720p.mkv", Media{Kind: Episode, Title: "Marvels Agents of SHIELD", Season: 1, Episodes: []int{1}}},
		{"S.W.A.T.2017.S02E03.mkv", Media{Kind: Episode, Title: "SWAT", Year: 2017, Season: 2, Episodes: []int{3}}},
		{"Movie+Title+2010+720p.mkv", Media{Kind: Movie, Title: "Movie Title", Year: 2010}},
		{"Some Title ~ 2010 ~ 1080p.mkv", Media{Kind: Movie, Title: "Some Title", Year: 2010}},
		{"【Group】Spirited.Away.2001.mkv", Media{Kind: Movie, Title: "Spirited Away", Year: 2001}},
		{"Grey’s.Anatomy.S19E01.mkv", Media{Kind: Episode, Title: "Grey's Anatomy", Season: 19, Episodes: []int{1}}},
		{"The​.Matrix.1999.mkv", Media{Kind: Movie, Title: "The Matrix", Year: 1999}},
		{"www.site.org - Inception.2010.mkv", Media{Kind: Movie, Title: "Inception", Year: 2010}},
		// Years.
		{"1917.2019.1080p.mkv", Media{Kind: Movie, Title: "1917", Year: 2019}},
		{"1917.mkv", Media{Kind: Movie, Title: "1917"}},
		{"Alien (1979) 2003 Director's Cut.mkv", Media{Kind: Movie, Title: "Alien", Year: 1979}},
		{"Future.Movie.2099.mkv", Media{Kind: Movie, Title: "Future Movie 2099"}},
		// Episode formats.
		{"Show.S01.E02.mkv", Media{Kind: Episode, Title: "Show", Season: 1, Episodes: []int{2}}},
		{"Show_S01-E02.mkv", Media{Kind: Episode, Title: "Show", Season: 1, Episodes: []int{2}}},
		{"Show.S01E01-E03.mkv", Media{Kind: Episode, Title: "Show", Season: 1, Episodes: []int{1, 2, 3}}},
		{"Show.S01E01-03.1080p.mkv", Media{Kind: Episode, Title: "Show", Season: 1, Episodes: []int{1, 2, 3}}},
		{"Show.S01E01E02E03.mkv", Media{Kind: Episode, Title: "Show", Season: 1, Episodes: []int{1, 2, 3}}},
		{"Show.S01E01-720p.mkv", Media{Kind: Episode, Title: "Show", Season: 1, Episodes: []int{1}}},
		{"Dark Season 1 Episode 2.mkv", Media{Kind: Episode, Title: "Dark", Season: 1, Episodes: []int{2}}},
		{"Lost Temporada 2 Episodio 7.mkv", Media{Kind: Episode, Title: "Lost", Season: 2, Episodes: []int{7}}},
		{"Lost.T02E07.mkv", Media{Kind: Episode, Title: "Lost", Season: 2, Episodes: []int{7}}},
		{"Show - 1x02-03.mkv", Media{Kind: Episode, Title: "Show", Season: 1, Episodes: []int{2, 3}}},
		{"The.Daily.Show.2024.03.15.Guest.720p.mkv", Media{Kind: Episode, Title: "The Daily Show", Season: 2024, AirDate: "2024-03-15"}},
		{"[SubsPlease] Frieren - 12 (1080p) [ABCD1234].mkv", Media{Kind: Episode, Title: "Frieren", Season: 1, Episodes: []int{12}}},
		{"Planet.Earth.II.Part.2.of.6.mkv", Media{Kind: Episode, Title: "Planet Earth II", Season: 1, Episodes: []int{2}}},
		{"Chernobyl.3of5.mkv", Media{Kind: Episode, Title: "Chernobyl", Season: 1, Episodes: []int{3}}},
		{"Dune.Part.Two.2024.mkv", Media{Kind: Movie, Title: "Dune Part Two", Year: 2024}},
		{"Harry.Potter.and.the.Deathly.Hallows.Part.1.2010.mkv", Media{Kind: Movie, Title: "Harry Potter and the Deathly Hallows Part 1", Year: 2010}},
		{"Some.Movie.1920x1080.mkv", Media{Kind: Movie, Title: "Some Movie 1920x1080"}},
		{"Show.Complete.Season.1.1080p/Show.S01E04.mkv", Media{Kind: Episode, Title: "Show", Season: 1, Episodes: []int{4}}},
		{"Show.Complete.Season.1.1080p/E04.mkv", Media{Kind: Episode, Title: "Show", Season: 1, Episodes: []int{4}}},
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

func TestAnalyzeConfidence(t *testing.T) {
	tests := []struct {
		path     string
		min, max float64
		alt      bool
	}{
		{"Breaking.Bad.S01E02.720p.HDTV.x264.mkv", 0.9, 1, false},
		{"Inception.2010.1080p.BluRay.x264-GROUP.mkv", 0.7, 0.8, false},
		{"The Matrix (1999).mkv", 0.85, 1, false},
		{"Some.Movie.Without.Year.1080p.WEBRip.mkv", 0.4, 0.5, false},
		{"1917.mkv", 0, 0.2, false},
		{"[SubsPlease] Frieren - 12 (1080p).mkv", 0, 0.3, true},
		{"Planet.Earth.II.Part.2.of.6.mkv", 0.3, 0.5, true},
	}
	for _, tt := range tests {
		a, ok := Analyze(tt.path)
		if !ok {
			t.Errorf("Analyze(%q) not ok", tt.path)
			continue
		}
		if a.Confidence < tt.min || a.Confidence > tt.max {
			t.Errorf("Analyze(%q).Confidence = %.2f (signals %v), want [%.2f, %.2f]", tt.path, a.Confidence, a.Signals, tt.min, tt.max)
		}
		if (len(a.Alternatives) > 0) != tt.alt {
			t.Errorf("Analyze(%q).Alternatives = %v, want present=%v", tt.path, a.Alternatives, tt.alt)
		}
	}
}

func TestQuality(t *testing.T) {
	tests := []struct {
		path string
		want Quality
	}{
		{"The.Matrix.1999.720p.HDTV.mkv", Quality{Resolution: 720, Source: "hdtv"}},
		{"The.Matrix.1999.1080p.BluRay.x264.mkv", Quality{Resolution: 1080, Source: "bluray"}},
		{"The.Matrix.1999.2160p.UHD.BluRay.REMUX.HDR.mkv", Quality{Resolution: 2160, Source: "remux", HDR: true}},
		{"The.Matrix.1999.4K.WEB-DL.mkv", Quality{Resolution: 2160, Source: "web-dl"}},
		{"The.Matrix.1999.1080p/movie.mkv", Quality{Resolution: 1080}},
		{"The.Matrix.1999.mkv", Quality{}},
		{"The.Matrix.1999.BluRay.mkv", Quality{Source: "bluray"}},
		{"The.Matrix.1999.1080p/The.Matrix.1999.REMUX.HDR.mkv", Quality{Resolution: 1080, Source: "remux", HDR: true}},
		{"The.Matrix.1999.1080p.WEB-DL/The.Matrix.1999.BluRay.mkv", Quality{Resolution: 1080, Source: "bluray"}},
	}
	for _, tt := range tests {
		a, _ := Analyze(tt.path)
		if a.Quality != tt.want {
			t.Errorf("Analyze(%q).Quality = %+v, want %+v", tt.path, a.Quality, tt.want)
		}
	}
	low, _ := Analyze("The.Matrix.1999.720p.BluRay.mkv")
	high, _ := Analyze("The.Matrix.1999.1080p.HDTV.mkv")
	if low.Quality.Rank() >= high.Quality.Rank() {
		t.Error("resolution must outrank source")
	}
}
