package parser

import (
	"regexp"
	"strings"
	"time"
	"unicode"
)

var (
	// reAcronym matches dotted single-letter acronyms such as "S.H.I.E.L.D." or "U.S.".
	reAcronym = regexp.MustCompile(`\b(?:[A-Za-z]\.){2,}(?:[A-Za-z]\b)?`)
	// separators are characters release names use instead of spaces.
	separators = strings.NewReplacer(".", " ", "_", " ", "+", " ", "~", " ", "|", " ")
)

// unicodeFold maps look-alike punctuation to ASCII and drops invisible runes.
func unicodeFold(r rune) rune {
	switch r {
	case 0x200b, 0x200c, 0x200d, 0x2060, 0xfeff, 0x00ad: // zero-width, BOM, soft hyphen
		return -1
	case '【', '［', '〔', '「':
		return '['
	case '】', '］', '〕', '」':
		return ']'
	case '（':
		return '('
	case '）':
		return ')'
	case '‘', '’', '´', '`':
		return '\''
	case '“', '”':
		return '"'
	case '–', '—', '‐', '‑':
		return '-'
	}
	if unicode.IsControl(r) || unicode.IsSpace(r) {
		return ' '
	}
	return r
}

// normalize folds Unicode noise, collapses dotted acronyms and turns release
// separators (. _ + ~ |) into single spaces. In-word hyphens and apostrophes
// are kept ("Spider-Man", "Grey's Anatomy").
func normalize(s string) string {
	s = strings.Map(unicodeFold, s)
	s = reAcronym.ReplaceAllStringFunc(s, func(m string) string {
		return strings.ReplaceAll(m, ".", "") + " "
	})
	s = separators.Replace(s)
	return strings.TrimSpace(reSpaces.ReplaceAllString(s, " "))
}

// validYear reports whether y is a plausible release year.
func validYear(y int) bool {
	return y >= 1900 && y <= time.Now().Year()+1
}
