package organizer

import (
	"os"
	"path/filepath"
	"testing"
)

// A title goes into the folder the library already has for its TMDB id,
// whatever its name, instead of a parallel folder the server would list as a
// second entry.
func TestAnchorToExistingFolder(t *testing.T) {
	tests := []struct {
		name, mode string
		dirs       []string // existing folders: "movies/..." or "tv/..."
		file       string
		want       string
	}{
		{"movie folder without year", "jellyfin", []string{"movies/Inception [tmdbid-27205]"},
			"Inception.2010.1080p.mkv", "movies/Inception [tmdbid-27205]/Inception [tmdbid-27205].mkv"},
		{"movie part follows the folder", "jellyfin", []string{"movies/Inception [tmdbid-27205]"},
			"Inception.2010.CD2.mkv", "movies/Inception [tmdbid-27205]/Inception [tmdbid-27205] - part2.mkv"},
		{"show tagged by another tool", "jellyfin", []string{"tv/One Piece (1999) {tmdb-37854}"},
			"[Sub] One Piece - 05 [1080p].mkv", "tv/One Piece (1999) {tmdb-37854}/Season 01/One Piece S01E05.mkv"},
		{"same id twice: no choice", "jellyfin", []string{"movies/Inception [tmdbid-27205]", "movies/Inception (2010) {tmdb-27205}"},
			"Inception.2010.1080p.mkv", "movies/Inception (2010) [tmdbid-27205]/Inception (2010) [tmdbid-27205].mkv"},
		{"another id", "jellyfin", []string{"movies/Inception [tmdbid-1]"},
			"Inception.2010.1080p.mkv", "movies/Inception (2010) [tmdbid-27205]/Inception (2010) [tmdbid-27205].mkv"},
		{"plex edition keeps its own folder", "plex", []string{"movies/Inception (2010) {tmdb-27205} {edition-Extended Cut}"},
			"Inception.2010.1080p.mkv", "movies/Inception (2010) {tmdb-27205}/Inception (2010) {tmdb-27205}.mkv"},
		{"plex same edition", "plex", []string{"movies/Inception {tmdb-27205} {edition-Extended Cut}"},
			"Inception.2010.Extended.Cut.1080p.mkv", "movies/Inception {tmdb-27205} {edition-Extended Cut}/Inception {tmdb-27205} {edition-Extended Cut}.mkv"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t, tt.mode)
			for _, d := range tt.dirs {
				os.MkdirAll(filepath.Join(filepath.Dir(e.movies), filepath.FromSlash(d)), 0o755)
			}
			if r := organizeOne(t, e, tt.file); r.Dest != tt.want {
				t.Errorf("dest = %q (%s %s), want %q", r.Dest, r.Status, r.Error, tt.want)
			}
		})
	}
}
