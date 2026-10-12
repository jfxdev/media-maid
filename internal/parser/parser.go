// Package parser extracts title, year, season and episode information from
// release-style file and folder names.
package parser

import (
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// Kind is the type of media a file represents.
type Kind string

const (
	Movie   Kind = "movie"
	Episode Kind = "episode"
)

// Media is the parsed information about a video file.
type Media struct {
	Kind     Kind   `json:"kind"`
	Title    string `json:"title"`
	Year     int    `json:"year,omitempty"`
	Season   int    `json:"season,omitempty"`
	Episodes []int  `json:"episodes,omitempty"`
}

var (
	reSxxEyy    = regexp.MustCompile(`(?i)\bS(\d{1,2}) ?E(\d{1,3})((?:[ -]?E\d{1,3})*)\b`)
	reMultiEp   = regexp.MustCompile(`(?i)E(\d{1,3})`)
	reNxNN      = regexp.MustCompile(`(?i)\b(\d{1,2})x(\d{2,3})\b`)
	reEpOnly    = regexp.MustCompile(`(?i)^(?:E|Ep|Episode|Episodio|Epis[oó]dio)\s?(\d{1,3})\b`)
	reSeasonDir = regexp.MustCompile(`(?i)^(?:Season|Temporada|Series|S)\s?(\d{1,2})$`)
	reSeasonTag = regexp.MustCompile(`(?i)\b(?:S\d{1,2}|Season\s?\d{1,2}|Temporada\s?\d{1,2})\b.*$`)
	reYear      = regexp.MustCompile(`\b(19\d{2}|20\d{2})\b`)
	reBrackets  = regexp.MustCompile(`^\s*[\[{][^\]}]*[\]}]\s*`)
	reSite      = regexp.MustCompile(`(?i)^\s*www\.[a-z0-9-]+\.[a-z]{2,}\s*-?\s*`)
	reSpaces    = regexp.MustCompile(`\s+`)
	reReleaseTk = regexp.MustCompile(`(?i)\b(2160p|1080p|1080i|720p|576p|480p|4k|uhd|hdr|hdr10|dv|bluray|blu-ray|brrip|bdrip|remux|web-?dl|webrip|web|hdtv|dvdrip|dvd|hdrip|x264|x265|h\.?264|h\.?265|hevc|avc|xvid|aac|ac3|dts|ddp?5\.?1|atmos|10bit|proper|repack|extended|unrated|remastered|dubbed|dual|multi|nf|amzn|dsnp|hmax|atvp)\b`)
)

// Parse inspects a file path (name plus parent folders) and returns the media
// information. ok is false when nothing usable could be extracted.
func Parse(path string) (Media, bool) {
	base := filepath.Base(path)
	stem := strings.TrimSuffix(base, filepath.Ext(base))
	parent := filepath.Base(filepath.Dir(path))
	grand := filepath.Base(filepath.Dir(filepath.Dir(path)))

	name := normalize(stem)
	if m, ok := parseEpisode(name); ok {
		if m.Title == "" || isGeneric(m.Title) {
			m.Title, m.Year = showFromDirs(parent, grand)
		}
		if m.Title == "" {
			return Media{}, false
		}
		return m, true
	}

	// Episode number only in the file name, season in the parent folder.
	if em := reEpOnly.FindStringSubmatch(name); em != nil {
		if sm := reSeasonDir.FindStringSubmatch(normalize(parent)); sm != nil {
			title, year := cleanTitleYear(normalize(grand))
			if title != "" {
				return Media{Kind: Episode, Title: title, Year: year, Season: atoi(sm[1]), Episodes: []int{atoi(em[1])}}, true
			}
		}
	}

	m := parseMovie(name)
	if m.Title == "" || isGeneric(m.Title) {
		pm := parseMovie(normalize(parent))
		if pm.Title != "" && !isGeneric(pm.Title) {
			if m.Year != 0 && pm.Year == 0 {
				pm.Year = m.Year
			}
			m = pm
		}
	}
	if m.Title == "" {
		return Media{}, false
	}
	return m, true
}

func parseEpisode(name string) (Media, bool) {
	if loc := reSxxEyy.FindStringSubmatchIndex(name); loc != nil {
		season := atoi(name[loc[2]:loc[3]])
		eps := []int{atoi(name[loc[4]:loc[5]])}
		if loc[6] >= 0 {
			for _, e := range reMultiEp.FindAllStringSubmatch(name[loc[6]:loc[7]], -1) {
				eps = append(eps, atoi(e[1]))
			}
		}
		title, year := cleanTitleYear(name[:loc[0]])
		return Media{Kind: Episode, Title: title, Year: year, Season: season, Episodes: eps}, true
	}
	if loc := reNxNN.FindStringSubmatchIndex(name); loc != nil {
		// Avoid matching resolutions like 1920x1080.
		title, year := cleanTitleYear(name[:loc[0]])
		return Media{Kind: Episode, Title: title, Year: year, Season: atoi(name[loc[2]:loc[3]]), Episodes: []int{atoi(name[loc[4]:loc[5]])}}, true
	}
	return Media{}, false
}

func parseMovie(name string) Media {
	name = stripPrefixes(name)
	// Use the last year that is not at the very start (e.g. "2001 A Space Odyssey 1968").
	var year int
	cut := -1
	for _, loc := range reYear.FindAllStringIndex(name, -1) {
		if loc[0] == 0 {
			continue
		}
		year = atoi(name[loc[0]:loc[1]])
		cut = loc[0]
	}
	if cut < 0 {
		if loc := reReleaseTk.FindStringIndex(name); loc != nil {
			cut = loc[0]
		}
	}
	title := name
	if cut >= 0 {
		title = name[:cut]
	}
	return Media{Kind: Movie, Title: tidy(title), Year: year}
}

// showFromDirs extracts a show title from the parent folder, or the
// grandparent when the parent is a "Season N" folder.
func showFromDirs(parent, grand string) (string, int) {
	p := normalize(parent)
	if reSeasonDir.MatchString(p) || p == "" || p == "." {
		return cleanTitleYear(normalize(grand))
	}
	return cleanTitleYear(reSeasonTag.ReplaceAllString(p, ""))
}

// cleanTitleYear removes release noise and extracts a trailing year.
func cleanTitleYear(s string) (string, int) {
	s = stripPrefixes(s)
	if loc := reReleaseTk.FindStringIndex(s); loc != nil {
		s = s[:loc[0]]
	}
	var year int
	if locs := reYear.FindAllStringIndex(s, -1); len(locs) > 0 {
		last := locs[len(locs)-1]
		if last[0] > 0 {
			year = atoi(s[last[0]:last[1]])
			s = s[:last[0]]
		}
	}
	return tidy(s), year
}

func stripPrefixes(s string) string {
	for {
		n := reSite.ReplaceAllString(reBrackets.ReplaceAllString(s, ""), "")
		if n == s {
			return s
		}
		s = n
	}
}

// normalize turns dots and underscores into spaces.
func normalize(s string) string {
	s = strings.NewReplacer(".", " ", "_", " ").Replace(s)
	return strings.TrimSpace(reSpaces.ReplaceAllString(s, " "))
}

func tidy(s string) string {
	s = strings.NewReplacer("(", " ", ")", " ", "[", " ", "]", " ").Replace(s)
	s = strings.TrimSpace(reSpaces.ReplaceAllString(s, " "))
	s = strings.Trim(s, " -–:")
	if s != "" && s == strings.ToLower(s) {
		s = titleCase(s)
	}
	return s
}

func titleCase(s string) string {
	words := strings.Fields(s)
	for i, w := range words {
		r := []rune(w)
		r[0] = unicode.ToUpper(r[0])
		words[i] = string(r)
	}
	return strings.Join(words, " ")
}

var genericNames = map[string]bool{
	"movie": true, "video": true, "film": true, "filme": true, "sample": true, "episode": true,
}

func isGeneric(t string) bool {
	l := strings.ToLower(strings.TrimSpace(t))
	if genericNames[l] {
		return true
	}
	// Hash-like names such as "a1b2c3d4e5f6".
	if len(l) >= 8 && !strings.Contains(l, " ") {
		hex := true
		for _, r := range l {
			if !strings.ContainsRune("0123456789abcdef", r) {
				hex = false
				break
			}
		}
		return hex
	}
	return false
}

func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}
