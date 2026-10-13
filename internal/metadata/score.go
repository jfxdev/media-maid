package metadata

import (
	"strings"
	"unicode"
)

// Thresholds used by callers to decide whether a pick can be trusted.
const (
	// MinScore is the lowest score at which a candidate is accepted at all.
	MinScore = 0.3
	// GoodScore is the score at which a candidate is a confident match.
	GoodScore = 0.5
	// MinMargin is the lead over the runner-up below which a pick is a tie.
	MinMargin = 0.1
)

var accents = strings.NewReplacer(
	"á", "a", "à", "a", "â", "a", "ã", "a", "ä", "a", "å", "a",
	"é", "e", "è", "e", "ê", "e", "ë", "e",
	"í", "i", "ì", "i", "î", "i", "ï", "i",
	"ó", "o", "ò", "o", "ô", "o", "õ", "o", "ö", "o", "ø", "o",
	"ú", "u", "ù", "u", "û", "u", "ü", "u",
	"ç", "c", "ñ", "n", "ý", "y", "ÿ", "y", "ß", "ss", "æ", "ae", "œ", "oe",
	"&", " and ",
)

// fold lowercases, strips accents and punctuation ("Marvel's S.H.I.E.L.D." and
// "marvels shield" fold to the same tokens).
func fold(s string) []string {
	s = accents.Replace(strings.ToLower(s))
	var b strings.Builder
	for _, r := range s {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r):
			b.WriteRune(r)
		case r == '\'' || r == '.' || r == '’':
			// "marvel's" -> "marvels", "s.h.i.e.l.d" -> "shield"
		default:
			b.WriteRune(' ')
		}
	}
	return strings.Fields(b.String())
}

// similarity is the Jaccard index of the folded token sets, with a bonus when
// one title is a prefix of the other ("Dune" vs "Dune Part One").
func similarity(a, b string) float64 {
	ta, tb := fold(a), fold(b)
	if len(ta) == 0 || len(tb) == 0 {
		return 0
	}
	ja, jb := strings.Join(ta, " "), strings.Join(tb, " ")
	if ja == jb {
		return 1
	}
	set := map[string]int{}
	for _, t := range ta {
		set[t] |= 1
	}
	for _, t := range tb {
		set[t] |= 2
	}
	inter := 0
	for _, v := range set {
		if v == 3 {
			inter++
		}
	}
	s := float64(inter) / float64(len(set))
	if strings.HasPrefix(ja+" ", jb+" ") || strings.HasPrefix(jb+" ", ja+" ") {
		s += 0.1
	}
	return min(s, 1)
}

// Score rates how well r matches a parsed title and year, from 0 to 1.
func Score(title string, year int, r Result) float64 {
	s := max(similarity(title, r.Title), similarity(title, r.OriginalTitle))
	if year > 0 && r.Year > 0 {
		switch d := year - r.Year; {
		case d == 0:
			s += 0.2
		case d == 1 || d == -1:
			s += 0.1
		default:
			s -= 0.2
		}
	}
	return max(0, min(s, 1))
}

// Pick returns the best candidate, its score and its lead over the runner-up.
// On equal scores the earlier (more relevant) candidate wins.
func Pick(title string, year int, cands []Result) (best Result, score, margin float64, ok bool) {
	return PickIn(title, year, "", cands)
}

// Popularity tie-break: among candidates scoring within MinMargin of the
// best, one with at least minVotes TMDB votes and voteDominance times the
// votes of every other such candidate is the clear pick ("Shameless" the US
// series over a little-known namesake). Comparable entries (Dune 1984 and
// 2021) stay a tie.
const (
	minVotes      = 100
	voteDominance = 10
)

// PickIn is Pick for a title with an origin marker ("The Office US"):
// candidates from that country are preferred over remakes from elsewhere.
// A tie settled by popularity reports a margin of exactly MinMargin.
func PickIn(title string, year int, country string, cands []Result) (best Result, score, margin float64, ok bool) {
	if len(cands) == 0 {
		return Result{}, 0, 0, false
	}
	scores := make([]float64, len(cands))
	top := 0
	for i, c := range cands {
		s := Score(title, year, c)
		if country != "" && c.OriginCountry != "" {
			if strings.EqualFold(c.OriginCountry, country) || (country == "UK" && c.OriginCountry == "GB") {
				s = min(s+0.15, 1)
			} else {
				s = max(s-0.15, 0)
			}
		}
		scores[i] = s
		if s > scores[top] {
			top = i
		}
	}
	second := 0.0
	for i, s := range scores {
		if i != top && s > second {
			second = s
		}
	}
	if i, ok := popular(cands, scores, scores[top]); ok {
		return cands[i], scores[i], MinMargin, true
	}
	return cands[top], scores[top], scores[top] - second, true
}

// popular returns the candidate that settles a tie by popularity (see
// minVotes), if any. A tie needs at least two candidates within MinMargin
// of the top score.
func popular(cands []Result, scores []float64, topScore float64) (int, bool) {
	var tied []int
	for i, s := range scores {
		if topScore-s < MinMargin {
			tied = append(tied, i)
		}
	}
	if len(tied) < 2 {
		return 0, false
	}
	lead := tied[0]
	for _, i := range tied[1:] {
		if cands[i].VoteCount > cands[lead].VoteCount {
			lead = i
		}
	}
	if cands[lead].VoteCount < minVotes {
		return 0, false
	}
	for _, i := range tied {
		if i != lead && cands[i].VoteCount*voteDominance > cands[lead].VoteCount {
			return 0, false
		}
	}
	return lead, true
}

// Normalize folds a title the way scoring compares titles (lower case, no
// accents or punctuation), for keys that must match the same title written
// differently.
func Normalize(title string) string { return strings.Join(fold(title), " ") }

// Localized returns the official translated title for lang ("pt-BR" or "pt").
// With a country, only a translation for that country (or one without a
// country) is accepted: pt-PT titles often differ from pt-BR ones. Alternative
// titles are not used here: picking one needs a judgement call.
func Localized(titles []Title, lang string) (string, bool) {
	base, country, _ := strings.Cut(lang, "-")
	fallback := ""
	for _, t := range titles {
		if t.Source != "translation" || !strings.EqualFold(t.Language, base) || t.Title == "" {
			continue
		}
		switch {
		case country == "" || strings.EqualFold(t.Country, country):
			return t.Title, true
		case t.Country == "" && fallback == "":
			fallback = t.Title
		}
	}
	return fallback, fallback != ""
}
