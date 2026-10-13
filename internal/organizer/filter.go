package organizer

import (
	"path/filepath"
	"regexp"
	"strings"
)

var (
	reSample = regexp.MustCompile(`(?i)(^|[ ._\-\[(])(sample|trailer)([ ._\-\])]|$)`)
	// junkDirs are sample and bonus folders, including the extras folders
	// Jellyfin and Plex recognize. Names are compared lower-cased with dots,
	// underscores and dashes read as spaces ("Behind.The.Scenes"). "Other" is
	// left out: it is also a common torrent client category.
	junkDirs = map[string]bool{
		"sample": true, "samples": true, "extras": true, "featurettes": true, "trailers": true,
		"behind the scenes": true, "deleted scenes": true, "interviews": true, "scenes": true,
		"shorts": true, "clips": true, "bonus": true,
	}
	dirSeparators = strings.NewReplacer(".", " ", "_", " ", "-", " ")
	partialRe     = regexp.MustCompile(`(?i)\.(part|!qb|crdownload|tmp|partial|aria2)$`)
)

// Filter decides which files are eligible, based on the extension allowlists.
type Filter struct {
	Video    map[string]bool
	Subtitle map[string]bool
}

func ext(name string) string { return strings.ToLower(filepath.Ext(name)) }

// IsVideo reports whether path is an allowlisted, non-junk video.
func (f Filter) IsVideo(path string) bool {
	return f.Video[ext(path)] && !isJunk(path)
}

// IsSubtitle reports whether path is an allowlisted subtitle.
func (f Filter) IsSubtitle(path string) bool {
	return f.Subtitle[ext(path)] && !partialRe.MatchString(path)
}

func isJunk(path string) bool {
	base := filepath.Base(path)
	if partialRe.MatchString(base) || strings.HasPrefix(base, ".") {
		return true
	}
	stem := strings.TrimSuffix(base, filepath.Ext(base))
	if reSample.MatchString(stem) {
		return true
	}
	for _, part := range strings.Split(filepath.Dir(path), string(filepath.Separator)) {
		if junkDirs[strings.ToLower(dirSeparators.Replace(part))] {
			return true
		}
	}
	return false
}
