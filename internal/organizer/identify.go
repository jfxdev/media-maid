package organizer

import (
	"context"
	"errors"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/jfxdev/media-maid/internal/assist"
	"github.com/jfxdev/media-maid/internal/metadata"
	"github.com/jfxdev/media-maid/internal/parser"
)

// Decision layers reported in Result.ResolvedBy and Result.TitleSource.
const (
	ByParser = "parser"
	ByTMDB   = "tmdb"
	ByLLM    = "llm"
	ByManual = "manual"

	TitleTMDBTranslation = "tmdb_translation"
	TitleLLMLocalized    = "llm_localized"
	TitleOriginal        = "original"
)

// identity is the outcome of the decision layers for one video.
type identity struct {
	media       parser.Media
	match       *metadata.Result // TMDB entry, when one was accepted
	by          string
	titleSource string
}

func metaKind(k parser.Kind) metadata.Kind {
	if k == parser.Episode {
		return metadata.Series
	}
	return metadata.Movie
}

// identify runs the layers in order: parser (always), TMDB (optional), then
// the LLM (optional) only when the first two are unsure. A failure of the LLM
// falls back to what the previous layers decided. Two outcomes are not
// decisions: a TMDB pick that is itself unsure (a tie or a weak score) waits
// for a manual identification unless AMBIGUOUS_MATCH=best, and a TMDB that
// cannot be reached leaves the file pending until RetryUnavailable succeeds.
// why is set when nothing was identified.
func (o *Organizer) identify(ctx context.Context, rel string, a parser.Analysis, parsed bool) (identity, string) {
	id := identity{media: a.Media, by: ByParser}

	var cands []metadata.Result
	var best metadata.Result
	var score, margin float64
	var found bool
	if parsed && o.hasMeta {
		// "The Office US" is searched as "The Office", preferring US entries.
		query, _ := parser.SplitCountry(a.Title)
		var err error
		if cands, err = o.meta.Search(ctx, metaKind(a.Kind), query, a.Year); err != nil {
			// Placing it without the ID now would leave it in a folder of
			// its own once TMDB answers again: it waits and is retried.
			return identity{}, errMetadataUnavailableMsg
		}
		best, score, margin, found = metadata.PickIn(query, a.Year, a.Country, cands)
	}

	// unsure: TMDB answered, but its best candidate is weak or tied.
	unsure := o.hasMeta && parsed && found &&
		(score < metadata.GoodScore || (len(cands) > 1 && margin < metadata.MinMargin))
	ambiguous := !parsed || a.Confidence < o.minConf || unsure
	if o.hasMeta && parsed {
		ambiguous = ambiguous || !found
	}

	decided := false
	if ambiguous && o.assist != nil {
		decided = o.askLLM(ctx, rel, a, cands, &id)
	}
	if !decided {
		if !parsed {
			return identity{}, errUnidentifiedMsg
		}
		if found && score >= metadata.MinScore {
			if unsure && !o.ambiguousBest {
				return identity{}, ambiguousMsg(cands)
			}
			id.match, id.by = &best, ByTMDB
		}
	}
	if id.match != nil {
		id.media.Title = id.match.Title
		if id.match.Year > 0 {
			id.media.Year = id.match.Year
		}
		o.mapAbsolute(ctx, &id)
	}
	if o.language != "" {
		o.localize(ctx, &id)
	}
	return id, ""
}

// errUnidentifiedMsg explains a file whose title could not be read.
const errUnidentifiedMsg = "could not identify title"

// maxListedCandidates bounds the candidates named in an ambiguous result.
const maxListedCandidates = 5

// ambiguousMsg explains an unsure TMDB pick, naming the candidates so the
// user can pick one with a manual identification (tmdb_id).
func ambiguousMsg(cands []metadata.Result) string {
	var b strings.Builder
	b.WriteString("ambiguous title: ")
	for i, c := range cands {
		if i == maxListedCandidates {
			b.WriteString(", ...")
			break
		}
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(c.Title)
		if c.Year > 0 {
			b.WriteString(" (" + strconv.Itoa(c.Year) + ")")
		}
		b.WriteString(" [tmdb " + strconv.Itoa(c.TMDBID) + "]")
	}
	b.WriteString("; identify it manually")
	return b.String()
}

// manual is an Override resolved once per request.
type manual struct {
	kind  parser.Kind
	title string // empty keeps the parser's title
	year  int    // zero keeps the parser's year
	match *metadata.Result
	// fixed is set when the user typed the title: it is not localized.
	fixed bool
	// ov is the identification as requested, remembered as an Alias.
	ov Override
}

// resolveOverride checks an Override against TMDB (when enabled) once for the
// whole request.
func (o *Organizer) resolveOverride(ctx context.Context, ov *Override) (*manual, error) {
	if ov == nil {
		return nil, nil
	}
	if ov.Kind != parser.Movie && ov.Kind != parser.Episode {
		return nil, ErrInvalidOverride
	}
	m := &manual{kind: ov.Kind, title: strings.TrimSpace(ov.Title), year: ov.Year, ov: *ov}
	m.fixed = m.title != ""
	kind := metaKind(ov.Kind)
	switch {
	case ov.TMDBID > 0 && o.hasMeta:
		r, err := o.meta.Lookup(ctx, kind, ov.TMDBID)
		if errors.Is(err, metadata.ErrNotFound) {
			return nil, ErrOverrideNotFound
		}
		if err != nil {
			return nil, ErrMetadataUnavailable
		}
		m.match = &r
		if m.title == "" {
			m.title = r.Title
		}
		if m.year == 0 {
			m.year = r.Year
		}
	case ov.TMDBID > 0:
		// Without TMDB the id cannot be checked: it is only a tag, and the
		// title must be given.
		if m.title == "" {
			return nil, ErrInvalidOverride
		}
		m.match = &metadata.Result{Title: m.title, Year: m.year, TMDBID: ov.TMDBID}
	case m.title != "" && o.hasMeta:
		res, err := o.meta.Search(ctx, kind, m.title, m.year)
		if err != nil {
			break // the title is used as typed
		}
		if best, score, _, ok := metadata.Pick(m.title, m.year, res); ok && score >= metadata.GoodScore {
			m.match, m.title, m.fixed = &best, best.Title, false
			if m.year == 0 {
				m.year = best.Year
			}
		}
	}
	return m, nil
}

// manualIdentity applies a resolved override to one parsed file. why is set
// when the file cannot take it.
func (o *Organizer) manualIdentity(ctx context.Context, a parser.Analysis, parsed bool, man *manual) (identity, string) {
	m := a.Media
	if !parsed {
		m = parser.Media{}
	}
	switch man.kind {
	case parser.Episode:
		if m.Kind != parser.Episode {
			return identity{}, "kind episode needs a season and episode number (or air date) in the name"
		}
	case parser.Movie:
		if m.Kind == parser.Episode {
			m = parser.Media{Title: m.Title, Year: m.Year}
		}
		m.Kind = parser.Movie
	}
	if man.title != "" {
		m.Title = man.title
	}
	if man.year > 0 {
		m.Year = man.year
	}
	if m.Title == "" {
		return identity{}, errUnidentifiedMsg
	}
	id := identity{media: m, match: man.match, by: ByManual}
	if id.match != nil && o.hasMeta {
		o.mapAbsolute(ctx, &id)
	}
	if o.language != "" && !man.fixed {
		o.localize(ctx, &id)
	}
	return id, ""
}

// askLLM applies a validated LLM decision to id. It reports false when the
// layer had nothing usable, leaving id untouched.
func (o *Organizer) askLLM(ctx context.Context, rel string, a parser.Analysis, cands []metadata.Result, id *identity) bool {
	if a.Kind == "" {
		// Nothing parsed: give the model the bare name as a movie guess.
		stem := filepath.Base(rel)
		a.Media = parser.Media{Kind: parser.Movie, Title: strings.TrimSuffix(stem, filepath.Ext(stem))}
	}
	d, err := o.assist.Resolve(ctx, assist.Request{Path: rel, Parsed: a, Candidates: cands})
	if err != nil {
		if !errors.Is(err, assist.ErrRejected) && !errors.Is(err, assist.ErrUnavailable) && ctx.Err() == nil {
			o.log.Warn("llm unavailable, keeping parser/tmdb decision", "source", rel, "err", err)
		}
		return false
	}
	m := parser.Media{Kind: d.Kind, Title: d.Title, Year: d.Year, Season: d.Season, Episodes: d.Episodes}
	if d.Kind == a.Kind && d.Kind == parser.Episode && a.AirDate != "" {
		m.AirDate, m.Season, m.Episodes = a.AirDate, a.Season, nil
	}
	// Facts the model is not asked about are kept from the parser.
	if d.Kind == a.Kind {
		m.Part, m.Edition = a.Part, a.Edition
		if d.Kind == parser.Episode && a.Absolute > 0 && len(d.Episodes) == 1 && d.Episodes[0] == a.Absolute {
			m.Absolute = a.Absolute
		}
	}
	var match *metadata.Result
	switch {
	case d.Choice >= 0:
		// Candidates were searched for the parser's kind.
		if d.Kind != a.Kind {
			return false
		}
		c := cands[d.Choice]
		match = &c
	case o.hasMeta:
		// A title the candidates did not cover must be confirmed by TMDB.
		res, err := o.meta.Search(ctx, metaKind(d.Kind), d.Title, d.Year)
		best, score, _, ok := metadata.Pick(d.Title, d.Year, res)
		if err != nil || !ok || score < metadata.GoodScore {
			return false
		}
		match = &best
	}
	id.media, id.match, id.by = m, match, ByLLM
	o.log.Info("llm decision", "source", rel, "kind", d.Kind, "title", m.Title, "confidence", d.Confidence)
	return true
}

// mapAbsolute turns an anime-style absolute episode number into the season
// and episode of the accepted TMDB series ("One Piece - 1080" is not S01E1080
// on TMDB). When the seasons are unknown or too short, the parse is kept.
func (o *Organizer) mapAbsolute(ctx context.Context, id *identity) {
	m := &id.media
	if m.Kind != parser.Episode || m.Absolute <= 0 || m.AirDate != "" {
		return
	}
	seasons, err := o.meta.Seasons(ctx, id.match.TMDBID)
	if err != nil {
		return
	}
	if season, ep, ok := metadata.AbsoluteToSeason(seasons, m.Absolute); ok {
		m.Season, m.Episodes = season, []int{ep}
	}
}

// localize replaces the title with the official one in o.language: a TMDB
// translation first, then an LLM choice among TMDB titles (or, without TMDB,
// a confident free answer), else the original title.
func (o *Organizer) localize(ctx context.Context, id *identity) {
	id.titleSource = TitleOriginal
	kind := metaKind(id.media.Kind)
	req := assist.LocalizeRequest{Kind: kind, Title: id.media.Title, Year: id.media.Year, Language: o.language}
	if id.match != nil {
		titles, err := o.meta.Titles(ctx, kind, id.match.TMDBID)
		if err != nil {
			return
		}
		if t, ok := metadata.Localized(titles, o.language); ok {
			id.media.Title, id.titleSource = t, TitleTMDBTranslation
			return
		}
		req.OriginalTitle, req.Options = id.match.OriginalTitle, relevantTitles(titles, o.language)
		if len(req.Options) == 0 {
			return
		}
	} else if o.hasMeta {
		// TMDB is on but did not confirm the work: a free answer is not allowed.
		return
	}
	if o.assist == nil {
		return
	}
	title, err := o.assist.Localize(ctx, req)
	if err != nil || title == "" {
		return
	}
	id.media.Title, id.titleSource = title, TitleLLMLocalized
}

// relevantTitles keeps TMDB titles in the target language or market.
func relevantTitles(titles []metadata.Title, lang string) []metadata.Title {
	base, country, _ := strings.Cut(lang, "-")
	var out []metadata.Title
	for _, t := range titles {
		if strings.EqualFold(t.Language, base) || (country != "" && strings.EqualFold(t.Country, country)) {
			out = append(out, t)
		}
	}
	return out
}
