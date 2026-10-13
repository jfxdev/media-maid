package organizer

import (
	"encoding/json"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/jfxdev/media-maid/internal/metadata"
	"github.com/jfxdev/media-maid/internal/parser"
)

// maxAliases bounds the remembered identifications; the oldest go first.
// maxAliasTitle matches the API's bound on a manual title.
const (
	maxAliases    = 20000
	maxAliasTitle = 200
)

// Alias is a manual identification remembered for every file whose name
// reads the same way: once "Shameless.S01E01" was identified as one TMDB
// series, the next episodes are too.
type Alias struct {
	Kind   parser.Kind `json:"kind"`
	Title  string      `json:"title,omitempty"`
	Year   int         `json:"year,omitempty"`
	TMDBID int         `json:"tmdb_id,omitempty"`
	At     time.Time   `json:"at"`
}

// Aliases maps a parsed reading (kind, folded title, year and origin marker)
// to the manual identification last placed for it. It is safe for concurrent
// use.
type Aliases struct {
	mu sync.Mutex
	m  map[string]Alias
	// OnChange, when set, is called (without locks held) after an update.
	OnChange func()
}

// NewAliases returns an empty alias table.
func NewAliases() *Aliases { return &Aliases{m: map[string]Alias{}} }

// aliasKey is the reading a manual identification is remembered under. The
// title is folded like TMDB scoring folds it, so "Show.Name" and "Show Name"
// share it; the year and origin marker keep remakes apart.
func aliasKey(a parser.Analysis) string {
	title, _ := parser.SplitCountry(a.Title)
	norm := metadata.Normalize(title)
	if norm == "" {
		return ""
	}
	return string(a.Kind) + "|" + norm + "|" + strconv.Itoa(a.Year) + "|" + a.Country
}

// Get returns the identification remembered for key.
func (s *Aliases) Get(key string) (Override, bool) {
	if s == nil || key == "" {
		return Override{}, false
	}
	s.mu.Lock()
	a, ok := s.m[key]
	s.mu.Unlock()
	return Override{Kind: a.Kind, Title: a.Title, Year: a.Year, TMDBID: a.TMDBID}, ok
}

// Put remembers ov for key, replacing an earlier identification.
func (s *Aliases) Put(key string, ov Override) {
	if s == nil || key == "" {
		return
	}
	now := time.Now().UTC()
	s.mu.Lock()
	old, had := s.m[key]
	if had && old.Kind == ov.Kind && old.Title == ov.Title && old.Year == ov.Year && old.TMDBID == ov.TMDBID {
		s.mu.Unlock()
		return
	}
	if !had && len(s.m) >= maxAliases {
		oldest, at := "", now
		for k, v := range s.m {
			if v.At.Before(at) {
				oldest, at = k, v.At
			}
		}
		delete(s.m, oldest)
	}
	s.m[key] = Alias{Kind: ov.Kind, Title: ov.Title, Year: ov.Year, TMDBID: ov.TMDBID, At: now}
	s.mu.Unlock()
	if s.OnChange != nil {
		s.OnChange()
	}
}

// Export returns the table as JSON.
func (s *Aliases) Export() ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return json.Marshal(s.m)
}

// Import loads a table saved by Export, replacing the current entries. An
// entry that is not a valid identification is dropped: state is data.
func (s *Aliases) Import(data []byte) error {
	var m map[string]Alias
	if err := json.Unmarshal(data, &m); err != nil {
		return err
	}
	for k, a := range m {
		if k == "" || (a.Kind != parser.Movie && a.Kind != parser.Episode) || a.TMDBID < 0 ||
			len([]rune(a.Title)) > maxAliasTitle || strings.IndexFunc(a.Title, unicode.IsControl) >= 0 {
			delete(m, k)
		}
	}
	if m == nil {
		m = map[string]Alias{}
	}
	s.mu.Lock()
	s.m = m
	s.mu.Unlock()
	return nil
}

// remembered is the identification to remember for a placement made with man:
// the TMDB entry it resolved to when the user gave only a title, otherwise
// exactly what the user asked for.
func remembered(man *manual) Override {
	ov := man.ov
	if ov.TMDBID == 0 && man.match != nil && man.match.TMDBID > 0 && !man.fixed {
		ov = Override{Kind: ov.Kind, TMDBID: man.match.TMDBID}
	}
	return ov
}
