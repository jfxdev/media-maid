// Package naming builds library-relative destination paths and sanitizes every
// name component so nothing can escape the library root.
package naming

import (
	"fmt"
	"path"
	"strings"
	"unicode"

	"github.com/jfxdev/media-maid/internal/parser"
)

const maxComponent = 120

// Sanitize keeps a conservative allowlist of characters, strips separators,
// control characters and dot-only names, and caps the length.
func Sanitize(s string) string {
	s = strings.ReplaceAll(s, ":", " -")
	var b strings.Builder
	for _, r := range s {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r):
			b.WriteRune(r)
		case strings.ContainsRune(" -_.,'!&()+", r):
			b.WriteRune(r)
		default:
			b.WriteRune(' ')
		}
	}
	out := strings.Join(strings.Fields(b.String()), " ")
	out = strings.Trim(out, " .")
	if r := []rune(out); len(r) > maxComponent {
		out = strings.TrimRight(string(r[:maxComponent]), " .")
	}
	if out == "" || strings.Trim(out, ".") == "" {
		return "Unknown"
	}
	return out
}

func withYear(title string, year int) string {
	t := Sanitize(title)
	if year > 0 {
		t = fmt.Sprintf("%s (%d)", t, year)
	}
	return t
}

func withTag(name, tag string) string {
	if tag == "" {
		return name
	}
	return name + " " + tag
}

// MoviePath returns "Title (Year) tag/Title (Year) tag.ext".
//
// An edition goes into editionTag when the server has a tag for it (Plex's
// "{edition-Extended Cut}", in folder and file names); otherwise it becomes a
// " - Extended Cut" file suffix, which Jellyfin and Emby read as a version of
// the movie in the same folder. A file part becomes " - part1".
func MoviePath(m parser.Media, idTag, editionTag, ext string) string {
	name := withTag(withYear(m.Title, m.Year), idTag)
	if editionTag != "" {
		name = withTag(name, editionTag)
	}
	file := name
	if m.Edition != "" && editionTag == "" {
		file += " - " + Sanitize(m.Edition)
	}
	if m.Part > 0 {
		file += fmt.Sprintf(" - part%d", m.Part)
	}
	return path.Join(name, file+cleanExt(ext))
}

// EpisodePath returns "Show (Year) tag/Season 01/Show S01E02[-E03].ext", or
// "Show (Year) tag/Season 2024/Show 2024-03-15.ext" for date-based episodes.
func EpisodePath(m parser.Media, idTag, ext string) string {
	show := withTag(withYear(m.Title, m.Year), idTag)
	season := fmt.Sprintf("Season %02d", m.Season)
	if m.AirDate != "" {
		return path.Join(show, season, Sanitize(m.Title)+" "+Sanitize(m.AirDate)+cleanExt(ext))
	}
	ep := fmt.Sprintf("S%02d", m.Season)
	for i, e := range m.Episodes {
		if i == 0 {
			ep += fmt.Sprintf("E%02d", e)
		} else {
			ep += fmt.Sprintf("-E%02d", e)
		}
	}
	return path.Join(show, season, Sanitize(m.Title)+" "+ep+cleanExt(ext))
}

// SubtitlePath places a subtitle next to its video: "<video stem>.<lang>[.forced][.sdh].ext".
func SubtitlePath(videoRel, lang string, forced, sdh bool, ext string) string {
	stem := strings.TrimSuffix(videoRel, path.Ext(videoRel))
	if lang != "" {
		stem += "." + Sanitize(lang)
	}
	if forced {
		stem += ".forced"
	}
	if sdh {
		stem += ".sdh"
	}
	return stem + cleanExt(ext)
}

func cleanExt(ext string) string {
	ext = strings.ToLower(strings.TrimPrefix(ext, "."))
	var b strings.Builder
	for _, r := range ext {
		if r < unicode.MaxASCII && (unicode.IsLetter(r) || unicode.IsDigit(r)) {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return ""
	}
	return "." + b.String()
}
