// Package parser extracts title, year, season and episode information from
// release-style file and folder names.
package parser

import (
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
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
	// AirDate (YYYY-MM-DD) identifies date-based episodes; Season is then the year.
	AirDate string `json:"air_date,omitempty"`
}

// Analysis is a parse result plus how much it can be trusted.
type Analysis struct {
	Media
	Quality Quality `json:"quality"`
	// Confidence is a deterministic 0..1 score built from Signals.
	Confidence float64 `json:"confidence"`
	// Signals name the evidence the result was built from.
	Signals []string `json:"signals,omitempty"`
	// Alternatives are competing interpretations of the same name.
	Alternatives []Media `json:"alternatives,omitempty"`
}

// Signals recorded in Analysis.Signals.
const (
	SigSxxEyy         = "sxxeyy"
	SigNxNN           = "nxnn"
	SigAirDate        = "air_date"
	SigSeasonDir      = "season_dir"
	SigPartOf         = "part_of"
	SigAbsolute       = "absolute_episode"
	SigYear           = "year"
	SigYearInParens   = "year_in_parens"
	SigReleaseTokens  = "release_tokens"
	SigTitleFromDir   = "title_from_parent"
	SigSingleWord     = "single_word_title"
	SigNumericTitle   = "numeric_title"
	SigNoYear         = "no_year"
	maxEpisodesPerHit = 20
)

var signalWeight = map[string]float64{
	SigSxxEyy:        0.4,
	SigNxNN:          0.3,
	SigAirDate:       0.3,
	SigSeasonDir:     0.3,
	SigPartOf:        0.1,
	SigAbsolute:      -0.2,
	SigYear:          0.3,
	SigYearInParens:  0.1,
	SigReleaseTokens: 0.1,
	SigTitleFromDir:  -0.1,
	SigSingleWord:    -0.15,
	SigNumericTitle:  -0.2,
	SigNoYear:        -0.15,
}

var (
	reSxxEyy    = regexp.MustCompile(`(?i)\b[ST](\d{1,2}) ?-? ?E(\d{1,3})((?:(?: ?-? ?E\d{1,3})|-\d{1,3}\b)*)\b`)
	reMultiEp   = regexp.MustCompile(`(-?) ?(?:[Ee])?(\d{1,3})`)
	reVerbose   = regexp.MustCompile(`(?i)\b(?:Season|Temporada|Temp)\s?(\d{1,2})\s?-?\s?(?:Episode|Epis[oó]dio|Ep|E)\s?(\d{1,3})\b`)
	reNxNN      = regexp.MustCompile(`(?i)\b(\d{1,2})x(\d{2,3})(?:-(\d{2,3}))?\b`)
	reAirDate   = regexp.MustCompile(`\b((?:19|20)\d{2})[ -](\d{2})[ -](\d{2})\b`)
	rePartOf    = regexp.MustCompile(`(?i)\b(?:(?:Part|Pt) ?(\d{1,2}) ?of ?(\d{1,2})|(\d{1,2}) ?of ?(\d{1,2}))\b`)
	reAbsolute  = regexp.MustCompile(`^(.+?) - (\d{1,4})(?:v\d)?(?: |$)`)
	reEpOnly    = regexp.MustCompile(`(?i)^(?:E|Ep|Episode|Episodio|Epis[oó]dio)\s?(\d{1,3})\b`)
	reSeasonDir = regexp.MustCompile(`(?i)^(?:Season|Temporada|Series|S)\s?(\d{1,2})$`)
	reSeasonIn  = regexp.MustCompile(`(?i)\b(?:Season|Temporada|S)\s?(\d{1,2})\b`)
	reSeasonTag = regexp.MustCompile(`(?i)\b(?:Complete\s)?(?:S\d{1,2}|Season\s?\d{1,2}|Temporada\s?\d{1,2})\b.*$`)
	reYear      = regexp.MustCompile(`\b(19\d{2}|20\d{2})\b`)
	reParenYear = regexp.MustCompile(`[(\[](19\d{2}|20\d{2})[)\]]`)
	reBrackets  = regexp.MustCompile(`^\s*[\[{][^\]}]*[\]}]\s*`)
	reSite      = regexp.MustCompile(`(?i)^\s*www[. ][a-z0-9-]+[. ][a-z]{2,}\s*-?\s*`)
	reSpaces    = regexp.MustCompile(`\s+`)
	reReleaseTk = regexp.MustCompile(`(?i)\b(2160p|1080p|1080i|720p|576p|480p|4k|uhd|hdr|hdr10|dv|bluray|blu-ray|brrip|bdrip|remux|web[- ]?dl|web[- ]?rip|web|hdtv|dvdrip|dvd|hdrip|x264|x265|h ?26[45]|hevc|avc|xvid|aac|ac3|dts|ddp?5 ?1|ddp|atmos|10bit|proper|repack|extended|unrated|remastered|dubbed|dual|multi|nf|amzn|dsnp|hmax|atvp)\b`)
)

// Parse inspects a file path (name plus parent folders) and returns the media
// information. ok is false when nothing usable could be extracted.
func Parse(path string) (Media, bool) {
	a, ok := Analyze(path)
	return a.Media, ok
}

// Analyze is Parse plus quality, confidence and alternative interpretations.
func Analyze(path string) (Analysis, bool) {
	base := filepath.Base(path)
	stem := strings.TrimSuffix(base, filepath.Ext(base))
	parent := filepath.Base(filepath.Dir(path))
	grand := filepath.Base(filepath.Dir(filepath.Dir(path)))

	name := normalize(stem)
	a := Analysis{Quality: detectQuality(name)}
	if a.Quality.Resolution == 0 {
		a.Quality = detectQuality(normalize(parent))
	}
	if reReleaseTk.MatchString(name) || reReleaseTk.MatchString(normalize(parent)) {
		a.add(SigReleaseTokens)
	}

	if m, sig, ok := parseEpisode(name, strings.HasPrefix(strings.TrimSpace(stem), "[")); ok {
		a.add(sig)
		if m.Title == "" || isGeneric(m.Title) {
			m.Title, m.Year = showFromDirs(parent, grand)
			a.add(SigTitleFromDir)
		}
		if m.Title == "" {
			return Analysis{}, false
		}
		a.Media = m
		if sig == SigAbsolute || sig == SigPartOf {
			a.Alternatives = append(a.Alternatives, parseMovieMedia(name))
		}
		return a.finish(), true
	}

	// Episode number only in the file name, season in the parent folder.
	if em := reEpOnly.FindStringSubmatch(name); em != nil {
		np := normalize(parent)
		sm := reSeasonDir.FindStringSubmatch(np)
		if sm == nil {
			// "Show.Complete.Season.1.1080p/E04.mkv"
			sm = reSeasonIn.FindStringSubmatch(np)
		}
		if sm != nil {
			title, year := showFromDirs(parent, grand)
			if title != "" {
				a.Media = Media{Kind: Episode, Title: title, Year: year, Season: atoi(sm[1]), Episodes: []int{atoi(em[1])}}
				a.add(SigSeasonDir, SigTitleFromDir)
				return a.finish(), true
			}
		}
	}

	m, parens := parseMovie(name)
	if m.Title == "" || isGeneric(m.Title) {
		pm, pparens := parseMovie(normalize(parent))
		if pm.Title != "" && !isGeneric(pm.Title) {
			if m.Year != 0 && pm.Year == 0 {
				pm.Year = m.Year
			}
			m, parens = pm, pparens
			a.add(SigTitleFromDir)
		}
	}
	if m.Title == "" {
		return Analysis{}, false
	}
	a.Media = m
	if parens {
		a.add(SigYearInParens)
	}
	return a.finish(), true
}

func (a *Analysis) add(sigs ...string) {
	for _, s := range sigs {
		if s != "" && !slices.Contains(a.Signals, s) {
			a.Signals = append(a.Signals, s)
		}
	}
}

// finish adds signals derived from the final media and computes Confidence.
func (a *Analysis) finish() Analysis {
	if a.Year > 0 {
		a.add(SigYear)
	} else if a.Kind == Movie {
		a.add(SigNoYear)
	}
	if !strings.Contains(a.Title, " ") {
		a.add(SigSingleWord)
		if _, err := strconv.Atoi(a.Title); err == nil {
			a.add(SigNumericTitle)
		}
	}
	// Episodes found via season/episode markers are not weakened by a missing year.
	score := 0.5
	for _, s := range a.Signals {
		if s == SigYear && a.Kind == Episode {
			continue
		}
		score += signalWeight[s]
	}
	if len(a.Alternatives) > 0 {
		score -= 0.2
	}
	switch {
	case score < 0:
		score = 0
	case score > 1:
		score = 1
	}
	a.Confidence = float64(int(score*100+0.5)) / 100
	return *a
}

func parseEpisode(name string, groupPrefix bool) (Media, string, bool) {
	if loc := reSxxEyy.FindStringSubmatchIndex(name); loc != nil {
		season := atoi(name[loc[2]:loc[3]])
		eps := []int{atoi(name[loc[4]:loc[5]])}
		if loc[6] >= 0 {
			eps = appendEpisodes(eps, name[loc[6]:loc[7]])
		}
		title, year := cleanTitleYear(name[:loc[0]])
		return Media{Kind: Episode, Title: title, Year: year, Season: season, Episodes: eps}, SigSxxEyy, true
	}
	if loc := reVerbose.FindStringSubmatchIndex(name); loc != nil {
		title, year := cleanTitleYear(name[:loc[0]])
		return Media{Kind: Episode, Title: title, Year: year, Season: atoi(name[loc[2]:loc[3]]), Episodes: []int{atoi(name[loc[4]:loc[5]])}}, SigSxxEyy, true
	}
	if loc := reNxNN.FindStringSubmatchIndex(name); loc != nil {
		title, year := cleanTitleYear(name[:loc[0]])
		eps := []int{atoi(name[loc[4]:loc[5]])}
		if loc[6] >= 0 {
			eps = appendRange(eps, atoi(name[loc[6]:loc[7]]))
		}
		return Media{Kind: Episode, Title: title, Year: year, Season: atoi(name[loc[2]:loc[3]]), Episodes: eps}, SigNxNN, true
	}
	if loc := reAirDate.FindStringSubmatchIndex(name); loc != nil && loc[0] > 0 {
		y, mo, d := atoi(name[loc[2]:loc[3]]), atoi(name[loc[4]:loc[5]]), atoi(name[loc[6]:loc[7]])
		if validYear(y) && mo >= 1 && mo <= 12 && d >= 1 && d <= 31 {
			title, year := cleanTitleYear(name[:loc[0]])
			return Media{Kind: Episode, Title: title, Year: year, Season: y, AirDate: fmt.Sprintf("%04d-%02d-%02d", y, mo, d)}, SigAirDate, true
		}
	}
	if m := rePartOf.FindStringSubmatchIndex(name); m != nil {
		part, total := m[2], m[4]
		if part < 0 {
			part, total = m[6], m[8]
		}
		p, n := atoi(name[part:part+digits(name[part:])]), atoi(name[total:total+digits(name[total:])])
		if p >= 1 && p <= n {
			title, year := cleanTitleYear(name[:m[0]])
			return Media{Kind: Episode, Title: title, Year: year, Season: 1, Episodes: []int{p}}, SigPartOf, true
		}
	}
	// Absolute episode numbers ("[Group] Show - 123 [1080p]") are only trusted
	// in names that start with a group tag, as anime releases do.
	if groupPrefix {
		if m := reAbsolute.FindStringSubmatch(stripPrefixes(name)); m != nil {
			n := atoi(m[2])
			if !(len(m[2]) == 4 && validYear(n)) && n > 0 {
				title, year := cleanTitleYear(m[1])
				return Media{Kind: Episode, Title: title, Year: year, Season: 1, Episodes: []int{n}}, SigAbsolute, true
			}
		}
	}
	return Media{}, "", false
}

// appendEpisodes parses the tail of an SxxEyy match: "E02E03" lists episodes,
// "-E05" or "-05" is a range ending at that episode.
func appendEpisodes(eps []int, tail string) []int {
	for _, e := range reMultiEp.FindAllStringSubmatch(tail, -1) {
		n := atoi(e[2])
		if e[1] == "-" {
			eps = appendRange(eps, n)
		} else if len(eps) < maxEpisodesPerHit {
			eps = append(eps, n)
		}
	}
	return eps
}

func appendRange(eps []int, end int) []int {
	for n := eps[len(eps)-1] + 1; n <= end && len(eps) < maxEpisodesPerHit; n++ {
		eps = append(eps, n)
	}
	return eps
}

func digits(s string) int {
	n := 0
	for n < len(s) && s[n] >= '0' && s[n] <= '9' {
		n++
	}
	return n
}

func parseMovieMedia(name string) Media {
	m, _ := parseMovie(name)
	return m
}

// parseMovie returns the movie interpretation and whether the year came from
// parentheses or brackets.
func parseMovie(name string) (Media, bool) {
	name = stripPrefixes(name)
	// A year in parentheses or brackets wins over a bare one.
	for _, loc := range reParenYear.FindAllStringSubmatchIndex(name, -1) {
		if y := atoi(name[loc[2]:loc[3]]); validYear(y) && loc[0] > 0 {
			return Media{Kind: Movie, Title: tidy(name[:loc[0]]), Year: y}, true
		}
	}
	// Use the last year that is not at the very start (e.g. "2001 A Space Odyssey 1968").
	var year int
	cut := -1
	for _, loc := range reYear.FindAllStringIndex(name, -1) {
		y := atoi(name[loc[0]:loc[1]])
		if loc[0] == 0 || !validYear(y) {
			continue
		}
		year = y
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
	return Media{Kind: Movie, Title: tidy(title), Year: year}, false
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
		if y := atoi(s[last[0]:last[1]]); last[0] > 0 && validYear(y) {
			year = y
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

func tidy(s string) string {
	s = strings.NewReplacer("(", " ", ")", " ", "[", " ", "]", " ").Replace(s)
	s = strings.TrimSpace(reSpaces.ReplaceAllString(s, " "))
	s = strings.Trim(s, " -:")
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
