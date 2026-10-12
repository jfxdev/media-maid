package assist

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/jfxdev/media-maid/internal/metadata"
	"github.com/jfxdev/media-maid/internal/parser"
)

const (
	maxSeason   = 100
	maxEpisode  = 2000
	maxEpisodes = 20
)

var reDigits = regexp.MustCompile(`\d+`)

func reject(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrRejected, fmt.Sprintf(format, a...))
}

// numbersIn returns every number written in the path ("S01E02" -> 1, 2).
func numbersIn(p string) map[int]bool {
	out := map[int]bool{}
	for _, d := range reDigits.FindAllString(p, -1) {
		if len(d) > 6 {
			continue
		}
		n, _ := strconv.Atoi(d)
		out[n] = true
	}
	return out
}

// validTitle rejects empty, oversized and path-like titles. naming.Sanitize
// still runs afterwards; this only refuses answers that are clearly not a title.
func validTitle(t string) error {
	if strings.TrimSpace(t) == "" {
		return reject("empty title")
	}
	if len([]rune(t)) > maxTitleRunes {
		return reject("title too long")
	}
	if strings.ContainsAny(t, "/\\") || strings.Contains(t, "..") {
		return reject("title looks like a path")
	}
	for _, r := range t {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return reject("title has control characters")
		}
	}
	return nil
}

// validateDecision checks a model answer against the request. The model is
// never the last word on numbers: season, episodes and year must come from
// the path, the parser or the chosen TMDB candidate.
func validateDecision(d *Decision, r Request, minConf float64, maxYear int) error {
	if d.Confidence < 0 || d.Confidence > 1 {
		return reject("confidence out of range")
	}
	if d.Confidence < minConf {
		return reject("low confidence %.2f", d.Confidence)
	}
	if d.Choice < -1 || d.Choice >= len(r.Candidates) || d.Choice >= maxCandidates {
		return reject("choice %d out of range", d.Choice)
	}
	var cand *metadata.Result
	if d.Choice >= 0 {
		cand = &r.Candidates[d.Choice]
		d.Title = cand.Title
	}
	if err := validTitle(d.Title); err != nil {
		return err
	}

	nums := numbersIn(r.Path)
	p := r.Parsed
	if d.Year != 0 {
		if d.Year < 1900 || d.Year > maxYear {
			return reject("year %d out of range", d.Year)
		}
		if !nums[d.Year] && d.Year != p.Year && (cand == nil || d.Year != cand.Year) {
			return reject("year %d not supported by the name or metadata", d.Year)
		}
	}

	switch d.Kind {
	case parser.Movie:
		d.Season, d.Episodes = 0, nil
	case parser.Episode:
		if d.Season < 0 || d.Season > maxSeason {
			return reject("season %d out of range", d.Season)
		}
		if !nums[d.Season] && d.Season != p.Season {
			return reject("season %d not in the name", d.Season)
		}
		if len(d.Episodes) == 0 || len(d.Episodes) > maxEpisodes {
			return reject("episode count %d", len(d.Episodes))
		}
		for _, e := range d.Episodes {
			if e < 1 || e > maxEpisode {
				return reject("episode %d out of range", e)
			}
			if !nums[e] && !slices.Contains(p.Episodes, e) {
				return reject("episode %d not in the name", e)
			}
		}
	default:
		return reject("unknown kind %q", d.Kind)
	}
	return nil
}

// validateLocalized checks a localization answer. With options, only a
// listed TMDB title can be returned; without, the free answer must be a
// plausible title with enough confidence. "" means no official title.
func validateLocalized(title string, choice int, conf float64, options []metadata.Title, minConf float64) (string, error) {
	if conf < 0 || conf > 1 {
		return "", reject("confidence out of range")
	}
	if len(options) > 0 {
		if choice == -1 {
			return "", nil
		}
		if choice < 0 || choice >= len(options) || choice >= maxOptions {
			return "", reject("choice %d out of range", choice)
		}
		if conf < minConf {
			return "", reject("low confidence %.2f", conf)
		}
		title = options[choice].Title
	} else {
		if strings.TrimSpace(title) == "" {
			return "", nil
		}
		if conf < minConf {
			return "", reject("low confidence %.2f", conf)
		}
	}
	if err := validTitle(title); err != nil {
		return "", err
	}
	return title, nil
}
