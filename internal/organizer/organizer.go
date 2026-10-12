// Package organizer turns downloaded files into a media-server friendly
// library layout using hardlinks.
package organizer

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/jfxdev/media-maid/internal/integration"
	"github.com/jfxdev/media-maid/internal/jobs"
	"github.com/jfxdev/media-maid/internal/metadata"
	"github.com/jfxdev/media-maid/internal/naming"
	"github.com/jfxdev/media-maid/internal/parser"
	"github.com/jfxdev/media-maid/internal/safepath"
)

// ErrNotFound is returned when the requested path does not exist.
var ErrNotFound = errors.New("not found")

// errUnauthorizedMsg is what callers see; the real target is never exposed.
const errUnauthorizedMsg = "unauthorized_volume"

// Library is a destination root (movies or tv).
type Library struct {
	Name string // "movies" or "tv", used as a prefix in results
	Path string
	Root *os.Root
}

// Organizer runs the organize pipeline.
type Organizer struct {
	downloads string
	movies    Library
	tv        Library
	filter    Filter
	guard     *safepath.Guard
	meta      metadata.Provider
	platform  integration.PlatformSpec
	syncer    integration.LibrarySyncer
	history   *jobs.History
	log       *slog.Logger
	dryRun    bool

	mu sync.Mutex
}

// Options configures an Organizer.
type Options struct {
	DownloadsDir string
	Movies, TV   Library
	Filter       Filter
	Guard        *safepath.Guard
	Meta         metadata.Provider
	Platform     integration.PlatformSpec
	Syncer       integration.LibrarySyncer
	History      *jobs.History
	Log          *slog.Logger
	DryRun       bool
}

// New builds an Organizer.
func New(o Options) *Organizer {
	if o.Meta == nil {
		o.Meta = metadata.Noop{}
	}
	return &Organizer{
		downloads: o.DownloadsDir, movies: o.Movies, tv: o.TV, filter: o.Filter,
		guard: o.Guard, meta: o.Meta, platform: o.Platform, syncer: o.Syncer,
		history: o.History, log: o.Log, dryRun: o.DryRun,
	}
}

// Result is the outcome for a single source file.
type Result struct {
	Source string `json:"source"`
	Dest   string `json:"dest,omitempty"`
	Kind   string `json:"kind,omitempty"`
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
	// Replaces is set on a planned result that would overwrite Dest.
	Replaces bool `json:"replaces,omitempty"`
}

// OrganizeOptions controls a single Organize call.
type OrganizeOptions struct {
	// Preview only reports what would happen (as does DRY_RUN).
	Preview bool
	// Overwrite atomically replaces an existing, different regular file at the
	// destination instead of reporting a conflict. Automatic runs (watcher,
	// scan) never set it.
	Overwrite bool
}

// DownloadsDir returns the resolved downloads root.
func (o *Organizer) DownloadsDir() string { return o.downloads }

// Scan organizes the whole downloads directory.
func (o *Organizer) Scan(ctx context.Context) ([]Result, error) {
	return o.Organize(ctx, o.downloads, OrganizeOptions{})
}

// Organize processes a file or directory inside the downloads root. With
// opts.Preview set (or DRY_RUN), it only reports what would happen.
//
// It returns safepath.ErrInvalidPath for malformed or out-of-root input,
// ErrNotFound when nothing exists there and safepath.ErrUnauthorizedVolume
// when the requested path itself is a link to an unauthorized volume.
func (o *Organizer) Organize(ctx context.Context, input string, opts OrganizeOptions) ([]Result, error) {
	p, err := safepath.CleanInput(o.downloads, input)
	if err != nil {
		return nil, err
	}
	dry := opts.Preview || o.dryRun
	if !dry {
		o.mu.Lock()
		defer o.mu.Unlock()
	}

	lst, err := os.Lstat(p)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, ErrNotFound
		}
		return nil, err
	}

	var files []string
	if lst.Mode()&fs.ModeSymlink != 0 {
		if _, err := o.guard.Resolve(p); err != nil {
			if errors.Is(err, safepath.ErrUnauthorizedVolume) {
				o.record(dry, Result{Source: o.rel(p), Status: jobs.StatusError, Error: errUnauthorizedMsg})
				o.log.Warn("link to unauthorized volume rejected", "source", o.rel(p))
				return nil, safepath.ErrUnauthorizedVolume
			}
			return nil, ErrNotFound
		}
		files = []string{p}
	} else if lst.IsDir() {
		files, err = o.collect(ctx, p)
		if err != nil {
			return nil, err
		}
	} else {
		files = []string{p}
	}

	results := o.process(ctx, files, dry, opts.Overwrite)
	changed := false
	for _, r := range results {
		o.record(dry, r)
		if r.Status == jobs.StatusLinked || r.Status == jobs.StatusCopied || r.Status == jobs.StatusReplaced {
			changed = true
		}
	}
	if changed && o.syncer != nil {
		o.syncer.Request()
	}
	return results, nil
}

func (o *Organizer) record(dry bool, r Result) {
	if dry || r.Status == jobs.StatusExists {
		return
	}
	o.history.Add(jobs.Entry{Action: jobs.ActionOrganize, Source: r.Source, Dest: r.Dest, Status: r.Status, Error: r.Error})
	if r.Status == jobs.StatusError || r.Status == jobs.StatusConflict {
		o.log.Warn("organize", "source", r.Source, "dest", r.Dest, "status", r.Status, "err", r.Error)
	} else if r.Status != jobs.StatusSkipped {
		o.log.Info("organize", "source", r.Source, "dest", r.Dest, "status", r.Status)
	}
}

// collect walks dir without following symlinked directories.
func (o *Organizer) collect(ctx context.Context, dir string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if d.IsDir() {
			if p != dir && strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type().IsRegular() || d.Type()&fs.ModeSymlink != 0 {
			out = append(out, p)
		}
		return nil
	})
	return out, err
}

func (o *Organizer) rel(p string) string {
	r, err := filepath.Rel(o.downloads, p)
	if err != nil {
		return filepath.Base(p)
	}
	return filepath.ToSlash(r)
}

type videoPlan struct {
	src     string
	media   parser.Media
	quality parser.Quality
	lib     Library
	dest    string // relative to lib root
	result  Result // set when planning already decided the outcome
}

var subsDirs = map[string]bool{"subs": true, "subtitles": true, "legendas": true, "sub": true}

func (o *Organizer) process(ctx context.Context, files []string, dry, overwrite bool) []Result {
	type group struct {
		videos []*videoPlan
		subs   []string
	}
	groups := map[string]*group{}
	get := func(k string) *group {
		if groups[k] == nil {
			groups[k] = &group{}
		}
		return groups[k]
	}
	var results []Result

	sort.Strings(files)
	for _, f := range files {
		rel := o.rel(f)
		switch {
		case o.filter.IsVideo(rel):
			get(filepath.Dir(f)).videos = append(get(filepath.Dir(f)).videos, &videoPlan{src: f})
		case o.filter.IsSubtitle(rel):
			get(subtitleGroup(f)).subs = append(get(subtitleGroup(f)).subs, f)
		}
	}

	keys := make([]string, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	// Plan every video before touching the disk, so releases of the same title
	// in different qualities can be compared first.
	var planned []*videoPlan
	for _, k := range keys {
		for _, v := range groups[k].videos {
			if ctx.Err() != nil {
				return results
			}
			v.result = o.planVideo(ctx, v)
			if v.result.Status == "" {
				planned = append(planned, v)
			}
		}
	}
	o.dedupe(planned)

	for _, k := range keys {
		g := groups[k]
		for _, v := range g.videos {
			if ctx.Err() != nil {
				return results
			}
			r := v.result
			if r.Status == "" {
				r = o.apply(v.src, v.lib, v.dest, dry, overwrite)
				r.Kind = string(v.media.Kind)
			}
			if r.Status == jobs.StatusError || r.Status == jobs.StatusDuplicate {
				v.dest = ""
			}
			results = append(results, r)
		}
		for _, s := range g.subs {
			results = append(results, o.subtitle(s, g.videos, dry, overwrite))
		}
	}
	return results
}

// dedupe keeps a single video per destination: the best quality, then the
// largest file. The others are reported as duplicates and left untouched.
func (o *Organizer) dedupe(videos []*videoPlan) {
	best := map[string]*videoPlan{}
	for _, v := range videos {
		key := v.lib.Name + "/" + v.dest
		if cur, ok := best[key]; !ok || better(v, cur) {
			best[key] = v
		}
	}
	for _, v := range videos {
		if w := best[v.lib.Name+"/"+v.dest]; w != v {
			v.result = Result{
				Source: o.rel(v.src), Dest: v.lib.Name + "/" + v.dest, Kind: string(v.media.Kind),
				Status: jobs.StatusDuplicate, Error: "superseded by " + o.rel(w.src),
			}
		}
	}
}

func better(a, b *videoPlan) bool {
	if ra, rb := a.quality.Rank(), b.quality.Rank(); ra != rb {
		return ra > rb
	}
	return fileSize(a.src) > fileSize(b.src)
}

func fileSize(p string) int64 {
	fi, err := os.Stat(p)
	if err != nil {
		return 0
	}
	return fi.Size()
}

// subtitleGroup maps a subtitle to the directory of the videos it belongs to,
// handling "Subs/" and "Subs/<video name>/" layouts.
func subtitleGroup(p string) string {
	dir := filepath.Dir(p)
	if subsDirs[strings.ToLower(filepath.Base(dir))] {
		return filepath.Dir(dir)
	}
	if subsDirs[strings.ToLower(filepath.Base(filepath.Dir(dir)))] {
		return filepath.Dir(filepath.Dir(dir))
	}
	return dir
}

func (o *Organizer) planVideo(ctx context.Context, v *videoPlan) Result {
	rel := o.rel(v.src)
	a, ok := parser.Analyze(rel)
	m := a.Media
	v.quality = a.Quality
	if !ok {
		return Result{Source: rel, Status: jobs.StatusSkipped, Error: "could not identify title"}
	}
	var tag string
	switch m.Kind {
	case parser.Episode:
		if res, ok := o.meta.LookupSeries(ctx, m.Title, m.Year); ok {
			m.Title, tag = res.Title, o.platform.IDTag(res.TMDBID)
			if res.Year > 0 {
				m.Year = res.Year
			}
		}
		v.lib = o.tv
		v.dest = naming.EpisodePath(m, tag, filepath.Ext(v.src))
	default:
		if res, ok := o.meta.LookupMovie(ctx, m.Title, m.Year); ok {
			m.Title, tag = res.Title, o.platform.IDTag(res.TMDBID)
			if res.Year > 0 {
				m.Year = res.Year
			}
		}
		v.lib = o.movies
		v.dest = naming.MoviePath(m, tag, filepath.Ext(v.src))
	}
	v.media = m
	return Result{}
}

// apply links src to dest inside lib (or reports what would happen when dry).
func (o *Organizer) apply(src string, lib Library, dest string, dry, overwrite bool) Result {
	r := Result{Source: o.rel(src), Dest: lib.Name + "/" + dest}

	resolved := src
	if fi, err := os.Lstat(src); err == nil && fi.Mode()&fs.ModeSymlink != 0 {
		resolved, err = o.guard.Resolve(src)
		if err != nil {
			r.Status = jobs.StatusError
			if errors.Is(err, safepath.ErrUnauthorizedVolume) {
				r.Error = errUnauthorizedMsg
			} else {
				r.Error = "broken link"
			}
			return r
		}
	}

	f, fi, err := o.guard.OpenRegular(resolved)
	if err != nil {
		r.Status = jobs.StatusError
		r.Error = describe(err)
		return r
	}
	defer f.Close()

	if dry {
		if existing, err := lib.Root.Lstat(dest); err == nil {
			switch {
			case samePlaced(existing, fi):
				r.Status = jobs.StatusExists
			case overwrite && existing.Mode().IsRegular():
				r.Status, r.Replaces = jobs.StatusPlanned, true
			default:
				r.Status = jobs.StatusConflict
			}
		} else {
			r.Status = jobs.StatusPlanned
		}
		return r
	}

	status, err := place(lib.Root, dest, f, fi, overwrite)
	r.Status = status
	if err != nil {
		r.Error = describe(err)
	}
	return r
}

func describe(err error) string {
	switch {
	case errors.Is(err, safepath.ErrUnauthorizedVolume):
		return errUnauthorizedMsg
	case errors.Is(err, safepath.ErrNotRegular):
		return "not a regular file"
	default:
		return err.Error()
	}
}

var reSubEp = regexp.MustCompile(`(?i)S(\d{1,2}) ?E(\d{1,3})`)

func (o *Organizer) subtitle(sub string, videos []*videoPlan, dry, overwrite bool) Result {
	rel := o.rel(sub)
	var planned []*videoPlan
	for _, v := range videos {
		if v.dest != "" {
			planned = append(planned, v)
		}
	}
	target := matchSubtitle(sub, planned)
	if target == nil {
		return Result{Source: rel, Status: jobs.StatusSkipped, Error: "no matching video"}
	}
	stem := strings.TrimSuffix(filepath.Base(sub), filepath.Ext(sub))
	vstem := strings.TrimSuffix(filepath.Base(target.src), filepath.Ext(target.src))
	lang, forced, sdh := detectLang(strings.TrimPrefix(stem, vstem))
	dest := naming.SubtitlePath(target.dest, lang, forced, sdh, filepath.Ext(sub))
	r := o.apply(sub, target.lib, dest, dry, overwrite)
	r.Kind = "subtitle"
	return r
}

func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

func matchSubtitle(sub string, videos []*videoPlan) *videoPlan {
	if len(videos) == 0 {
		return nil
	}
	if len(videos) == 1 {
		return videos[0]
	}
	stem := strings.ToLower(strings.TrimSuffix(filepath.Base(sub), filepath.Ext(sub)))
	hint := strings.ToLower(filepath.Base(filepath.Dir(sub)))
	for _, v := range videos {
		vstem := strings.ToLower(strings.TrimSuffix(filepath.Base(v.src), filepath.Ext(v.src)))
		if strings.HasPrefix(stem, vstem) || hint == vstem {
			return v
		}
	}
	if m := reSubEp.FindStringSubmatch(stem); m != nil {
		for _, v := range videos {
			if v.media.Kind == parser.Episode && len(v.media.Episodes) > 0 &&
				v.media.Season == atoi(m[1]) && v.media.Episodes[0] == atoi(m[2]) {
				return v
			}
		}
	}
	return nil
}

var langs = map[string]string{
	"pt-br": "pt-BR", "ptbr": "pt-BR", "pob": "pt-BR", "brazilian": "pt-BR", "portuguese-br": "pt-BR",
	"pt": "pt", "por": "pt", "portuguese": "pt", "pt-pt": "pt-PT",
	"en": "en", "eng": "en", "english": "en",
	"es": "es", "spa": "es", "spanish": "es", "es-419": "es-419", "latino": "es-419",
	"fr": "fr", "fre": "fr", "fra": "fr", "french": "fr",
	"de": "de", "ger": "de", "deu": "de", "german": "de",
	"it": "it", "ita": "it", "italian": "it",
	"ja": "ja", "jpn": "ja", "japanese": "ja",
	"ko": "ko", "kor": "ko", "korean": "ko",
	"zh": "zh", "chi": "zh", "zho": "zh", "chinese": "zh",
	"ru": "ru", "rus": "ru", "russian": "ru",
	"nl": "nl", "dut": "nl", "nld": "nl", "dutch": "nl",
	"pl": "pl", "pol": "pl", "polish": "pl",
	"ar": "ar", "ara": "ar", "arabic": "ar",
}

// detectLang extracts a language code and flags from the part of a subtitle
// name that follows the video name (or the whole name).
func detectLang(s string) (lang string, forced, sdh bool) {
	tokens := strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return strings.ContainsRune("._ []()", r)
	})
	for _, t := range tokens {
		switch t {
		case "forced", "foreign":
			forced = true
		case "sdh", "hi", "cc":
			sdh = true
		default:
			if l, ok := langs[t]; ok {
				lang = l
			}
		}
	}
	return lang, forced, sdh
}
