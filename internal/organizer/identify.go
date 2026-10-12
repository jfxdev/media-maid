package organizer

import (
	"context"
	"errors"
	"path/filepath"
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
// the LLM (optional) only when the first two are unsure. Any failure of an
// optional layer falls back to what the previous layers decided.
func (o *Organizer) identify(ctx context.Context, rel string, a parser.Analysis, parsed bool) (identity, bool) {
	id := identity{media: a.Media, by: ByParser}

	var cands []metadata.Result
	var best metadata.Result
	var score, margin float64
	var found, tmdbFailed bool
	if parsed && o.hasMeta {
		var err error
		cands, err = o.meta.Search(ctx, metaKind(a.Kind), a.Title, a.Year)
		tmdbFailed = err != nil
		best, score, margin, found = metadata.Pick(a.Title, a.Year, cands)
	}

	ambiguous := !parsed || a.Confidence < o.minConf
	if o.hasMeta && parsed && !tmdbFailed {
		ambiguous = ambiguous || !found || score < metadata.GoodScore ||
			(len(cands) > 1 && margin < metadata.MinMargin)
	}

	decided := false
	if ambiguous && o.assist != nil {
		decided = o.askLLM(ctx, rel, a, cands, &id)
	}
	if !decided {
		if !parsed {
			return identity{}, false
		}
		if found && score >= metadata.MinScore {
			id.match, id.by = &best, ByTMDB
		}
	}
	if id.match != nil {
		id.media.Title = id.match.Title
		if id.match.Year > 0 {
			id.media.Year = id.match.Year
		}
	}
	if o.language != "" {
		o.localize(ctx, &id)
	}
	return id, true
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
