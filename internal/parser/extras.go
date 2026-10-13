package parser

import (
	"regexp"
	"strings"
)

// edition is a recognized cut or edition label.
type edition struct {
	re    *regexp.Regexp
	label string
	// afterYear limits the label to names where it follows the year, for
	// phrases that are also real titles ("The Final Cut (2004)").
	afterYear bool
}

var editions = []edition{
	{regexp.MustCompile(`(?i)\bDirector'?s Cut\b`), "Director's Cut", false},
	{regexp.MustCompile(`(?i)\bExtended (?:Cut|Edition)\b`), "Extended Cut", false},
	{regexp.MustCompile(`(?i)\bExtended\b`), "Extended Cut", false},
	{regexp.MustCompile(`(?i)\bTheatrical (?:Cut|Edition|Version)?\b`), "Theatrical Cut", false},
	{regexp.MustCompile(`(?i)\bUnrated(?: (?:Cut|Edition))?\b`), "Unrated", false},
	{regexp.MustCompile(`(?i)\bUncut\b`), "Uncut", true},
	{regexp.MustCompile(`(?i)\bUltimate (?:Cut|Edition)\b`), "Ultimate Cut", false},
	{regexp.MustCompile(`(?i)\bSpecial Edition\b`), "Special Edition", false},
	{regexp.MustCompile(`(?i)\bCriterion(?: Collection| Edition)?\b`), "Criterion", false},
	{regexp.MustCompile(`(?i)\bIMAX(?: Edition)?\b`), "IMAX", false},
	{regexp.MustCompile(`(?i)\bRemastered\b`), "Remastered", true},
	{regexp.MustCompile(`(?i)\bFinal Cut\b`), "Final Cut", true},
}

// splitEdition finds an edition label in a normalized movie name and returns
// the name without it. A label at the very start of the name is a title.
func splitEdition(name string) (string, string) {
	firstYear := -1
	if loc := reYear.FindStringIndex(name); loc != nil && loc[0] > 0 {
		firstYear = loc[0]
	}
	for _, e := range editions {
		loc := e.re.FindStringIndex(name)
		if loc == nil || loc[0] == 0 {
			continue
		}
		if e.afterYear && (firstYear < 0 || loc[0] < firstYear) {
			continue
		}
		rest := strings.TrimSpace(reSpaces.ReplaceAllString(name[:loc[0]]+" "+name[loc[1]:], " "))
		return rest, e.label
	}
	return name, ""
}

var (
	// rePartDisc matches disc markers anywhere after the first word.
	rePartDisc = regexp.MustCompile(`(?i)\b(?:CD|Disc|Disk) ?(\d{1,2})\b`)
	// rePartWord matches "Part1"/"pt.2"; it is only a file part when it comes
	// after the year or a release token ("Deathly Hallows Part 1" is a title).
	rePartWord = regexp.MustCompile(`(?i)\b(?:Part|Pt) ?(\d{1,2})\b`)
)

// splitPart finds a multi-file part marker in a normalized movie name and
// returns the name without it.
func splitPart(name string) (string, int) {
	if rePartOf.MatchString(name) {
		return name, 0
	}
	cut := func(loc []int) (string, int) {
		n := atoi(name[loc[2]:loc[3]])
		if n < 1 {
			return name, 0
		}
		return strings.TrimSpace(reSpaces.ReplaceAllString(name[:loc[0]]+" "+name[loc[1]:], " ")), n
	}
	if loc := rePartDisc.FindStringSubmatchIndex(name); loc != nil && loc[0] > 0 {
		return cut(loc)
	}
	boundary := -1
	if loc := reYear.FindStringIndex(name); loc != nil && loc[0] > 0 {
		boundary = loc[1]
	}
	if loc := reReleaseTk.FindStringIndex(name); loc != nil && (boundary < 0 || loc[0] < boundary) {
		boundary = loc[0]
	}
	if boundary < 0 {
		return name, 0
	}
	for _, loc := range rePartWord.FindAllStringSubmatchIndex(name, -1) {
		if loc[0] >= boundary {
			return cut(loc)
		}
	}
	return name, 0
}

// countries are the origin markers release names append to remakes
// ("The Office US", "Shameless UK").
var countries = map[string]bool{"US": true, "UK": true, "AU": true, "NZ": true, "CA": true}

// SplitCountry separates a trailing origin marker from a title. It returns the
// title unchanged and "" when there is none or the title is only that word.
func SplitCountry(title string) (string, string) {
	i := strings.LastIndexByte(title, ' ')
	if i <= 0 {
		return title, ""
	}
	if c := title[i+1:]; countries[c] {
		return strings.TrimSpace(title[:i]), c
	}
	return title, ""
}
