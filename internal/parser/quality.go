package parser

import "regexp"

// Quality describes the release quality advertised in a name.
type Quality struct {
	Resolution int    `json:"resolution,omitempty"` // 480, 576, 720, 1080 or 2160
	Source     string `json:"source,omitempty"`     // remux, bluray, web-dl, webrip, hdtv, dvd
	HDR        bool   `json:"hdr,omitempty"`
}

var (
	reResolution = regexp.MustCompile(`(?i)\b(2160|1080|720|576|480)[pi]\b|\b(4k|uhd)\b`)
	reHDR        = regexp.MustCompile(`(?i)\b(hdr|hdr10|dv|dovi|dolby vision)\b`)
	sources      = []struct {
		name string
		re   *regexp.Regexp
	}{
		{"remux", regexp.MustCompile(`(?i)\bremux\b`)},
		{"bluray", regexp.MustCompile(`(?i)\b(blu-?ray|bdrip|brrip|bd)\b`)},
		{"web-dl", regexp.MustCompile(`(?i)\bweb[- ]?dl\b`)},
		{"webrip", regexp.MustCompile(`(?i)\b(web[- ]?rip|web)\b`)},
		{"hdtv", regexp.MustCompile(`(?i)\bhdtv\b`)},
		{"dvd", regexp.MustCompile(`(?i)\b(dvdrip|dvd)\b`)},
	}
	sourceRank = map[string]int{"remux": 6, "bluray": 5, "web-dl": 4, "webrip": 3, "hdtv": 2, "dvd": 1}
)

func detectQuality(name string) Quality {
	var q Quality
	if m := reResolution.FindStringSubmatch(name); m != nil {
		if m[1] != "" {
			q.Resolution = atoi(m[1])
		} else {
			q.Resolution = 2160
		}
	}
	for _, s := range sources {
		if s.re.MatchString(name) {
			q.Source = s.name
			break
		}
	}
	q.HDR = reHDR.MatchString(name)
	return q
}

// Rank orders qualities: resolution first, then source, then HDR.
func (q Quality) Rank() int {
	r := q.Resolution*100 + sourceRank[q.Source]*10
	if q.HDR {
		r++
	}
	return r
}
