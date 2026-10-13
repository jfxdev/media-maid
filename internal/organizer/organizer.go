// Package organizer turns downloaded files into a media-server friendly
// library layout using hardlinks.
package organizer

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jfxdev/media-maid/internal/assist"
	"github.com/jfxdev/media-maid/internal/integration"
	"github.com/jfxdev/media-maid/internal/jobs"
	"github.com/jfxdev/media-maid/internal/metadata"
	"github.com/jfxdev/media-maid/internal/naming"
	"github.com/jfxdev/media-maid/internal/parser"
	"github.com/jfxdev/media-maid/internal/safepath"
)

// ErrNotFound is returned when the requested path does not exist.
var ErrNotFound = errors.New("not found")

// Errors of a manual identification (OrganizeOptions.Override).
var (
	// ErrInvalidOverride: the override cannot be applied (a TMDB id without
	// TMDB_API_KEY and without a title).
	ErrInvalidOverride = errors.New("invalid override")
	// ErrOverrideNotFound: TMDB has no entry with the given id and kind.
	ErrOverrideNotFound = errors.New("tmdb id not found")
	// ErrMetadataUnavailable: TMDB could not be reached to check the override.
	ErrMetadataUnavailable = errors.New("metadata provider unavailable")
)

// KindExtra marks a result for a file from a release's extras folder.
const KindExtra = "extra"

// errUnauthorizedMsg is what callers see; the real target is never exposed.
const errUnauthorizedMsg = "unauthorized_volume"

// errRemovedMsg and errSupersededMsg explain a skipped file whose library
// copy was deleted, or replaced by another release (an upgrade or overwrite).
const (
	errRemovedMsg    = "previously organized; removed from the library"
	errSupersededMsg = "previously organized; replaced in the library by another release"
)

// errArchiveMsg reports a compressed release, which Media Maid does not
// extract: it needs attention instead of being ignored silently.
const errArchiveMsg = "archive: extract it first (compressed releases are not supported)"

// leftAlone reports a quiet skip: a file automatic runs leave alone on
// purpose. It is neither recorded in the history nor pending.
func leftAlone(r Result) bool {
	return r.Status == jobs.StatusSkipped && (r.Error == errRemovedMsg || r.Error == errSupersededMsg)
}

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
	hasMeta   bool
	assist    assist.Resolver
	minConf   float64
	language  string
	platform  integration.PlatformSpec
	syncer    integration.LibrarySyncer
	history   *jobs.History
	log       *slog.Logger
	dryRun    bool
	noCopy    bool
	ledger    *Ledger
	pending   *Pending
	notify    func(Result)
	upgrade   bool
	extras    bool
	// ambiguousBest accepts an unsure TMDB pick (AMBIGUOUS_MATCH=best).
	ambiguousBest bool

	// mu serializes placement (dedupe and writes). Identification, which
	// may wait on TMDB or the LLM, runs outside it.
	mu sync.Mutex
}

// Options configures an Organizer.
type Options struct {
	DownloadsDir string
	Movies, TV   Library
	Filter       Filter
	Guard        *safepath.Guard
	// Meta is the optional TMDB layer; nil disables it.
	Meta metadata.Provider
	// Assist is the optional LLM decision layer; nil disables it.
	Assist assist.Resolver
	// MinConfidence is the parser confidence below which a decision is
	// treated as ambiguous and handed to Assist.
	MinConfidence float64
	// Language (BCP 47) selects localized titles; empty keeps the original.
	Language string
	Platform integration.PlatformSpec
	Syncer   integration.LibrarySyncer
	History  *jobs.History
	Log      *slog.Logger
	DryRun   bool
	// NoCopy turns a file that cannot be hardlinked (another filesystem or
	// mount) into an error instead of a full copy (COPY_FALLBACK=false).
	NoCopy bool
	// Ledger remembers placed files across runs; nil uses a fresh one.
	Ledger *Ledger
	// Pending tracks downloads that need attention; nil uses a fresh one.
	Pending *Pending
	// Notify, when set, receives results worth telling the user about: new
	// or changed pending items and successful placements (not "exists").
	Notify func(Result)
	// AutoUpgrade lets automatic runs replace a library file placed by Media
	// Maid with a better release of the same title (AUTO_UPGRADE).
	AutoUpgrade bool
	// LinkExtras links videos from a release's extras folders into the
	// server's extras folders (LINK_EXTRAS).
	LinkExtras bool
	// AmbiguousBest accepts the best TMDB candidate when it is weak or tied
	// (AMBIGUOUS_MATCH=best); by default such a file waits as pending.
	AmbiguousBest bool
}

// New builds an Organizer.
func New(o Options) *Organizer {
	hasMeta := o.Meta != nil
	if !hasMeta {
		o.Meta = metadata.Noop{}
	}
	if o.Ledger == nil {
		o.Ledger = NewLedger()
	}
	if o.Pending == nil {
		o.Pending = NewPending()
	}
	return &Organizer{
		pending: o.Pending, notify: o.Notify, upgrade: o.AutoUpgrade, extras: o.LinkExtras, ambiguousBest: o.AmbiguousBest,
		hasMeta: hasMeta, assist: o.Assist, minConf: o.MinConfidence, language: o.Language,
		downloads: o.DownloadsDir, movies: o.Movies, tv: o.TV, filter: o.Filter,
		guard: o.Guard, meta: o.Meta, platform: o.Platform, syncer: o.Syncer,
		history: o.History, log: o.Log, dryRun: o.DryRun, noCopy: o.NoCopy, ledger: o.Ledger,
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
	// Previous is a library file this result removed (or would remove, in a
	// preview): the lower-quality release it upgraded under another file
	// name, or the earlier placement of the same file that a manual
	// identification corrected.
	Previous string `json:"previous,omitempty"`
	// ResolvedBy names the layer that identified a video: parser, tmdb, llm
	// or manual.
	ResolvedBy string `json:"resolved_by,omitempty"`
	// TitleSource is set with PREFERRED_LANGUAGE: tmdb_translation,
	// llm_localized or original.
	TitleSource string `json:"title_source,omitempty"`
}

// OrganizeOptions controls a single Organize call.
type OrganizeOptions struct {
	// Preview only reports what would happen (as does DRY_RUN).
	Preview bool
	// Overwrite atomically replaces an existing, different regular file at the
	// destination instead of reporting a conflict. Automatic runs (watcher,
	// scan) never set it.
	Overwrite bool
	// Explicit marks a request for specific files (the organize API). It
	// ignores the ledger: files are identified again, and re-linked even if
	// their library copy was deleted. Automatic runs leave such files alone.
	Explicit bool
	// MinAge skips files modified more recently than this, so a periodic
	// scan never picks up a download still being written.
	MinAge time.Duration
	// Override replaces automatic identification for every video of the
	// request (an explicit organize or preview).
	Override *Override
}

// Override is a manual identification. Kind is required; TMDBID names the
// entry exactly (its title and year are used unless Title and Year are
// given); Title alone is searched on TMDB and used as typed when TMDB has no
// confident match. Season and episode numbers still come from the name.
type Override struct {
	Kind   parser.Kind
	Title  string
	Year   int
	TMDBID int
}

// DownloadsDir returns the resolved downloads root.

func (o *Organizer) DownloadsDir() string { return o.downloads }

// Exclusive runs fn while no placement is in progress (orphan removal uses it
// so a folder is never deleted while a file is being linked into it).
func (o *Organizer) Exclusive(fn func()) {
	o.mu.Lock()
	defer o.mu.Unlock()
	fn()
}

// Ledger returns the organize ledger.
func (o *Organizer) Ledger() *Ledger { return o.ledger }

// Pending returns the downloads that need attention, oldest first.
func (o *Organizer) Pending() []PendingItem { return o.pending.List() }

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
	man, err := o.resolveOverride(ctx, opts.Override)
	if err != nil {
		return nil, err
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
		// Entries recorded from here on (another run placing a file this
		// listing missed) are never pruned.
		listed := time.Now()
		files, err = o.collect(ctx, p)
		if err != nil {
			return nil, err
		}
		if p == o.downloads && !dry && ctx.Err() == nil {
			o.pruneLedger(files, listed)
			o.prunePending(files, listed)
		}
	} else {
		files = []string{p}
	}
	if opts.MinAge > 0 {
		files = settled(files, opts.MinAge)
	}

	results := o.process(ctx, files, dry, opts, man)
	var changed []string
	for _, r := range results {
		o.record(dry, r)
		if placed(r.Status) && r.Status != jobs.StatusExists && !dry {
			changed = append(changed, o.libraryDir(r.Dest))
		}
		if r.Previous != "" && !dry {
			changed = append(changed, o.libraryDir(r.Previous))
		}
	}
	if len(changed) > 0 && o.syncer != nil {
		o.syncer.Request(changed...)
	}
	return results, nil
}

// lib returns the library named name ("movies" or "tv").
func (o *Organizer) lib(name string) Library {
	if name == o.tv.Name {
		return o.tv
	}
	return o.movies
}

// libraryDir turns a result destination ("movies/A (2000)/A (2000).mkv") into
// the absolute folder holding it, for a targeted library refresh.
func (o *Organizer) libraryDir(dest string) string {
	name, rel, _ := strings.Cut(dest, "/")
	return filepath.Join(o.lib(name).Path, filepath.FromSlash(path.Dir(rel)))
}

// settled drops files modified within the last minAge.
func settled(files []string, minAge time.Duration) []string {
	cutoff := time.Now().Add(-minAge)
	out := files[:0:0]
	for _, f := range files {
		if fi, err := os.Stat(f); err == nil && fi.ModTime().Before(cutoff) {
			out = append(out, f)
		}
	}
	return out
}

// pruneLedger drops entries for files no longer in the downloads folder
// (listed at since).
func (o *Organizer) pruneLedger(files []string, since time.Time) {
	infos := make([]os.FileInfo, 0, len(files))
	for _, f := range files {
		if fi, err := os.Stat(f); err == nil {
			infos = append(infos, fi)
		}
	}
	o.ledger.Prune(infos, since)
}

// prunePending drops pending items whose file left the downloads folder
// (listed at since).
func (o *Organizer) prunePending(files []string, since time.Time) {
	keep := make(map[string]bool, len(files))
	for _, f := range files {
		keep[o.rel(f)] = true
	}
	o.pending.Prune(keep, since)
}

func (o *Organizer) record(dry bool, r Result) {
	if dry {
		return
	}
	// Repeated outcomes (a conflict seen on every scan) are told once.
	changed := o.pending.Update(r)
	if o.notify != nil && (changed || (placed(r.Status) && r.Status != jobs.StatusExists && r.Kind != "subtitle" && r.Kind != KindExtra)) {
		o.notify(r)
	}
	// Unchanged outcomes would flood the history on every scan.
	if r.Status == jobs.StatusExists || leftAlone(r) {
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
	// identified is set when a TMDB entry backs the destination.
	identified bool
	// resolvedBy and titleSource explain the decision (see Result).
	resolvedBy, titleSource string
	// prev is the earlier placement of this file, kept when a manual
	// identification may move it.
	prev *LedgerEntry
	// upgraded is set when this video replaced a lower-quality release; its
	// subtitles then replace the old release's ones.
	upgraded bool
}

// extraFile is a video from a release's extras folder.
type extraFile struct {
	src      string
	category string // normalized extras folder name ("featurettes")
	release  string // the release folder holding the extras folder
}

var subsDirs = map[string]bool{"subs": true, "subtitles": true, "legendas": true, "sub": true}

func (o *Organizer) process(ctx context.Context, files []string, dry bool, opts OrganizeOptions, man *manual) []Result {
	overwrite := opts.Overwrite
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
	var extras []extraFile
	var archives []string

	sort.Strings(files)
	for _, f := range files {
		rel := o.rel(f)
		switch {
		case o.filter.IsVideo(rel):
			get(filepath.Dir(f)).videos = append(get(filepath.Dir(f)).videos, &videoPlan{src: f})
		case o.filter.IsSubtitle(rel):
			get(subtitleGroup(f)).subs = append(get(subtitleGroup(f)).subs, f)
		case o.filter.Archive(rel):
			archives = append(archives, f)
		case o.extras:
			if cat, dir, ok := o.filter.Extra(rel); ok {
				extras = append(extras, extraFile{src: f, category: cat, release: filepath.Join(o.downloads, dir)})
			}
		}
	}

	keys := make([]string, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	// Plan every video before touching the disk, so releases of the same title
	// in different qualities can be compared first.
	var planned, all []*videoPlan
	for _, k := range keys {
		for _, v := range groups[k].videos {
			if ctx.Err() != nil {
				return results
			}
			v.result = o.planVideo(ctx, v, opts, man)
			if v.result.Status == "" {
				planned = append(planned, v)
			}
			all = append(all, v)
		}
	}
	o.dedupe(planned)

	if !dry {
		o.mu.Lock()
		defer o.mu.Unlock()
	}

	for _, k := range keys {
		g := groups[k]
		for _, v := range g.videos {
			if ctx.Err() != nil {
				return results
			}
			r := v.result
			if r.Status == "" {
				r = o.placeVideo(v, dry, opts)
				if !dry {
					o.remember(v, r)
				}
			}
			// Subtitles and extras never follow a video that was not placed
			// by this run's decision (an error, a conflict, or a release that
			// lost the dedupe).
			switch r.Status {
			case jobs.StatusError, jobs.StatusDuplicate, jobs.StatusSkipped, jobs.StatusConflict:
				v.dest = ""
			}
			results = append(results, r)
		}
		for _, s := range g.subs {
			results = append(results, o.subtitle(s, g.videos, dry, overwrite))
		}
	}
	for _, x := range extras {
		if ctx.Err() != nil {
			return results
		}
		results = append(results, o.extra(x, all, dry, opts))
	}
	for _, a := range archives {
		if ctx.Err() != nil {
			return results
		}
		if r, ok := o.archive(a); ok {
			results = append(results, r)
		} else if !dry {
			// Extracted since it was reported: no longer pending.
			o.pending.Resolve(o.rel(a))
		}
	}
	return results
}

// archive reports a compressed video release as an error, so it shows up as
// pending (and is notified) instead of being ignored. An archive is left
// alone, like any other file, when its folder already holds a video (it was
// extracted next to it) or when its name does not look like a video release
// (software, music).
func (o *Organizer) archive(src string) (Result, bool) {
	rel := o.rel(src)
	a, ok := parser.Analyze(rel)
	if !ok || (a.Kind != parser.Episode && !slices.Contains(a.Signals, parser.SigReleaseTokens)) {
		return Result{}, false
	}
	dir := filepath.Dir(src)
	if entries, err := os.ReadDir(dir); err == nil {
		for _, e := range entries {
			if !e.IsDir() && o.filter.IsVideo(o.rel(filepath.Join(dir, e.Name()))) {
				return Result{}, false
			}
		}
	}
	return Result{Source: rel, Status: jobs.StatusError, Error: errArchiveMsg}, true
}

// stem drops the extension: releases in .mkv and .mp4 of the same title are
// the same library entry.
func stem(dest string) string { return strings.TrimSuffix(dest, path.Ext(dest)) }

// dedupe keeps a single video per destination: the best quality, then the
// largest file (placeVideo ranks a release already in the library the same
// way). The others are reported as duplicates and left untouched.
func (o *Organizer) dedupe(videos []*videoPlan) {
	best := map[string]*videoPlan{}
	for _, v := range videos {
		key := v.lib.Name + "/" + stem(v.dest)
		if cur, ok := best[key]; !ok || better(v, cur) {
			best[key] = v
		}
	}
	for _, v := range videos {
		w := best[v.lib.Name+"/"+stem(v.dest)]
		if w == v {
			continue
		}
		v.result = Result{
			Source: o.rel(v.src), Dest: v.lib.Name + "/" + v.dest, Kind: string(v.media.Kind),
			Status: jobs.StatusDuplicate, Error: "superseded by " + o.rel(w.src),
		}
		// Without a TMDB entry, two releases of equal quality under the same
		// name may be different works (remakes sharing title and year): that
		// is not a quality decision to make silently.
		if !v.identified && !w.identified && v.quality.Rank() == w.quality.Rank() {
			v.result.Status = jobs.StatusConflict
			v.result.Error = "same name as " + o.rel(w.src) + " and not identified by TMDB; may be a different title"
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

// peer is a video already in the library under the destination of a new
// release, or next to it with another extension.
type peer struct {
	rel     string // relative to the library root
	fi      os.FileInfo
	entry   LedgerEntry
	tracked bool // placed by Media Maid, so its quality is known
}

// findPeer looks for a regular file at v's destination or with the same name
// and another video extension. A file that already is v's source is no peer.
func (o *Organizer) findPeer(v *videoPlan, src os.FileInfo) *peer {
	dir, base := path.Split(v.dest)
	dir = strings.TrimSuffix(dir, "/")
	if dir == "" {
		dir = "."
	}
	d, err := v.lib.Root.Open(dir)
	if err != nil {
		return nil
	}
	entries, err := d.ReadDir(-1)
	d.Close()
	if err != nil {
		return nil
	}
	want := stem(base)
	for _, e := range entries {
		name := e.Name()
		if name != base && (stem(name) != want || !o.filter.Video[strings.ToLower(path.Ext(name))]) {
			continue
		}
		rel := path.Join(dir, name)
		fi, err := v.lib.Root.Lstat(rel)
		if err != nil || !fi.Mode().IsRegular() || os.SameFile(fi, src) || o.copyOf(v.lib, rel, fi, src) {
			continue
		}
		p := &peer{rel: rel, fi: fi}
		if le, ok := o.ledger.Lookup(fi); ok && le.Lib == v.lib.Name && le.Dest == rel {
			p.entry, p.tracked = le, true // a hardlink: same inode as its source
		} else if le, ok := o.ledger.ByDest(v.lib.Name, rel); ok && le.Status == jobs.StatusCopied &&
			le.Size == fi.Size() && le.MTime.Equal(fi.ModTime()) && (le.DestIno == 0 || le.DestIno == Inode(fi)) {
			p.entry, p.tracked = le, true // our copy: the recorded file, same size and mtime
		}
		return p
	}
	return nil
}

// placeVideo writes one planned video, first comparing it with a release of
// the same title already in the library: a lower-quality one placed by Media
// Maid is upgraded with AUTO_UPGRADE (or replaced on an explicit overwrite),
// an equal or better one makes this a duplicate, anything else a conflict.
func (o *Organizer) placeVideo(v *videoPlan, dry bool, opts OrganizeOptions) Result {
	srcFi, err := os.Stat(v.src)
	if err != nil {
		r := o.apply(v.src, v.lib, v.dest, dry, false)
		r.Kind = string(v.media.Kind)
		return r
	}
	base := Result{
		Source: o.rel(v.src), Dest: v.lib.Name + "/" + v.dest, Kind: string(v.media.Kind),
		ResolvedBy: v.resolvedBy, TitleSource: v.titleSource,
	}
	p := o.findPeer(v, srcFi)
	overwrite, upgrade := false, false
	if p != nil {
		inLib := v.lib.Name + "/" + p.rel
		// The same ranking as dedupe within a run (see better): quality,
		// then file size.
		rank, known := v.quality.Rank(), p.tracked && p.entry.Quality > 0
		wins := known && (rank > p.entry.Quality || (rank == p.entry.Quality && srcFi.Size() > p.fi.Size()))
		switch {
		case opts.Overwrite:
			overwrite = true
		case wins && o.upgrade:
			overwrite, upgrade = true, true
		case known && !wins:
			base.Status, base.Error = jobs.StatusDuplicate, "the library already has "+inLib+" at the same or better quality"
			return base
		case known:
			base.Status = jobs.StatusConflict
			base.Error = "better than " + inLib + " in the library; set AUTO_UPGRADE=true or organize it with overwrite"
			return base
		case p.rel != v.dest:
			base.Status, base.Error = jobs.StatusConflict, "another release is in the library as "+inLib
			return base
		}
	}

	r := o.apply(v.src, v.lib, v.dest, dry, overwrite)
	r.Kind, r.ResolvedBy, r.TitleSource = base.Kind, base.ResolvedBy, base.TitleSource
	if p != nil && overwrite && p.rel != v.dest {
		// The new release has another extension: it was placed next to the
		// old one, which goes now.
		switch {
		case dry && r.Status == jobs.StatusPlanned:
			r.Previous = v.lib.Name + "/" + p.rel
		case r.Status == jobs.StatusLinked || r.Status == jobs.StatusCopied:
			if err := removeChecked(v.lib.Root, p.rel, p.fi); err != nil {
				r.Error = "placed, but " + v.lib.Name + "/" + p.rel + " was kept: " + describe(err)
				o.log.Warn("previous release kept", "dest", v.lib.Name+"/"+p.rel, "err", err)
			} else {
				r.Previous = v.lib.Name + "/" + p.rel
				r.Status = jobs.StatusReplaced
			}
		}
	}
	if upgrade && r.Status == jobs.StatusReplaced {
		r.Status = jobs.StatusUpgraded
	}
	v.upgraded = r.Status == jobs.StatusReplaced || r.Status == jobs.StatusUpgraded
	o.movePrevious(v, srcFi, dry, &r)
	return r
}

// movePrevious removes the earlier placement of a file that a manual
// identification placed elsewhere, but only while it still is that file
// (the same inode, or the recorded copy).
func (o *Organizer) movePrevious(v *videoPlan, src os.FileInfo, dry bool, r *Result) {
	prev := v.prev
	if prev == nil || (prev.Lib == v.lib.Name && prev.Dest == v.dest) {
		return
	}
	if !placed(r.Status) && !(dry && r.Status == jobs.StatusPlanned) {
		return
	}
	lib := o.lib(prev.Lib)
	cur, err := lib.Root.Lstat(prev.Dest)
	if err != nil || !cur.Mode().IsRegular() {
		return
	}
	if !os.SameFile(cur, src) && !o.copyOf(lib, prev.Dest, cur, src) {
		return // something else lives there now
	}
	old := lib.Name + "/" + prev.Dest
	if dry {
		r.Previous = old
		return
	}
	if err := removeChecked(lib.Root, prev.Dest, cur); err != nil {
		o.log.Warn("previous placement kept", "dest", old, "err", err)
		return
	}
	r.Previous = old
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

// remember records a placed video in the ledger.
func (o *Organizer) remember(v *videoPlan, r Result) {
	o.rememberFile(v.src, v.lib, v.dest, r.Status, LedgerEntry{
		Kind: string(v.media.Kind), ResolvedBy: v.resolvedBy, TitleSource: v.titleSource, Quality: v.quality.Rank(),
	})
}

// rememberFile records src as placed at dest in lib; e carries the details.
func (o *Organizer) rememberFile(src string, lib Library, dest, status string, e LedgerEntry) {
	if !placed(status) {
		return
	}
	fi, err := os.Stat(src)
	if err != nil {
		return
	}
	if status == jobs.StatusExists {
		// Keep how it was first placed: a copy stays a copy.
		if old, ok := o.ledger.Lookup(fi); ok {
			status = old.Status
		} else if dfi, err := lib.Root.Stat(dest); err == nil && os.SameFile(fi, dfi) {
			status = jobs.StatusLinked
		} else {
			status = jobs.StatusCopied
		}
	}
	if status == jobs.StatusReplaced || status == jobs.StatusUpgraded {
		// The file in the library is this source's hardlink or copy.
		if dfi, err := lib.Root.Stat(dest); err == nil && os.SameFile(fi, dfi) {
			status = jobs.StatusLinked
		} else {
			status = jobs.StatusCopied
		}
	}
	if status == jobs.StatusCopied {
		if dfi, err := lib.Root.Lstat(dest); err == nil {
			e.DestIno = Inode(dfi)
		}
	}
	e.Lib, e.Dest, e.Status = lib.Name, dest, status
	o.ledger.Put(fi, e)
}

// fromLedger reuses the recorded placement of v, if any. A file whose library
// copy is gone was removed on purpose: automatic runs leave it alone.
func (o *Organizer) fromLedger(v *videoPlan) (Result, bool) {
	fi, err := os.Stat(v.src)
	if err != nil {
		return Result{}, false
	}
	e, ok := o.ledger.Lookup(fi)
	if !ok {
		return Result{}, false
	}
	lib := o.lib(e.Lib)
	if why := o.placementGone(lib, e, fi); why != "" {
		return Result{
			Source: o.rel(v.src), Dest: lib.Name + "/" + e.Dest, Kind: e.Kind, Status: jobs.StatusSkipped,
			Error: why,
		}, true
	}
	v.lib, v.dest, v.resolvedBy, v.titleSource = lib, e.Dest, e.ResolvedBy, e.TitleSource
	v.media.Kind = parser.Kind(e.Kind)
	v.identified = true // the decision was already made; never re-deduped as unidentified
	return Result{}, true
}

// placementGone tells why the recorded placement e of src is no longer in
// the library ("" while it is): deleted, or replaced by another file.
func (o *Organizer) placementGone(lib Library, e LedgerEntry, src os.FileInfo) string {
	cur, err := lib.Root.Lstat(e.Dest)
	if err != nil {
		return errRemovedMsg
	}
	if !os.SameFile(cur, src) && !o.copyOf(lib, e.Dest, cur, src) {
		return errSupersededMsg
	}
	return ""
}

// copyOf reports whether cur, the file at dest in lib, is the copy of src
// Media Maid made: the ledger records a copy of src at dest, cur still has
// src's size and mtime, and it is the library file recorded then (entries
// from before DestIno existed are trusted on the ledger record alone). An
// inode number reused by a later file with the same size and mtime is not
// told apart: that needs a content check, which costs a full read.
func (o *Organizer) copyOf(lib Library, dest string, cur, src os.FileInfo) bool {
	if !cur.Mode().IsRegular() || cur.Size() != src.Size() || !cur.ModTime().Equal(src.ModTime()) {
		return false
	}
	e, ok := o.ledger.Lookup(src)
	if !ok || e.Status != jobs.StatusCopied || e.Lib != lib.Name || e.Dest != dest {
		return false
	}
	return e.DestIno == 0 || e.DestIno == Inode(cur)
}

func (o *Organizer) planVideo(ctx context.Context, v *videoPlan, opts OrganizeOptions, man *manual) Result {
	rel := o.rel(v.src)
	a, ok := parser.Analyze(rel)
	// The parser's reading is kept for subtitle matching even when the
	// ledger supplies the destination.
	v.quality, v.media = a.Quality, a.Media
	if !opts.Explicit {
		if r, ok := o.fromLedger(v); ok {
			return r
		}
	}
	var id identity
	if man != nil {
		if fi, err := os.Stat(v.src); err == nil {
			if e, ok := o.ledger.Lookup(fi); ok {
				v.prev = &e
			}
		}
		var why string
		if id, why = o.manualIdentity(ctx, a, ok, man); why != "" {
			return Result{Source: rel, Status: jobs.StatusSkipped, Error: why}
		}
	} else {
		var why string
		if id, why = o.identify(ctx, rel, a, ok); why != "" {
			return Result{Source: rel, Status: jobs.StatusSkipped, Error: why}
		}
	}
	m := id.media
	var tag string
	if id.match != nil {
		tag = o.platform.IDTag(id.match.TMDBID)
	}
	switch m.Kind {
	case parser.Episode:
		v.lib = o.tv
		v.dest = naming.EpisodePath(m, tag, filepath.Ext(v.src))
	default:
		v.lib = o.movies
		v.dest = naming.MoviePath(m, tag, o.platform.EditionTag(m.Edition), filepath.Ext(v.src))
	}
	v.media, v.resolvedBy, v.titleSource, v.identified = m, id.by, id.titleSource, id.match != nil
	return Result{}
}

// extra links a video from a release's extras folder into the extras folder
// of the title its release was placed as: the movie folder, or the show
// folder. It is skipped when the release did not place exactly one title.
func (o *Organizer) extra(x extraFile, videos []*videoPlan, dry bool, opts OrganizeOptions) Result {
	rel := o.rel(x.src)
	folder, ok := o.platform.ExtrasDir(x.category)
	if !ok {
		return Result{Source: rel, Kind: KindExtra, Status: jobs.StatusSkipped, Error: "extras folder not read by the media server"}
	}
	if !opts.Explicit {
		if fi, err := os.Stat(x.src); err == nil {
			if e, ok := o.ledger.Lookup(fi); ok {
				lib := o.lib(e.Lib)
				if why := o.placementGone(lib, e, fi); why != "" {
					return Result{Source: rel, Dest: lib.Name + "/" + e.Dest, Kind: KindExtra, Status: jobs.StatusSkipped, Error: why}
				}
				// Already placed: stays where it is, even once its release's
				// video was upgraded away.
				r := o.apply(x.src, lib, e.Dest, dry, false)
				r.Kind = KindExtra
				return r
			}
		}
	}
	var lib Library
	base := ""
	for _, v := range videos {
		if v.dest == "" || !safepath.Within(x.release, v.src) {
			continue
		}
		b := path.Dir(v.dest)
		if v.media.Kind == parser.Episode {
			b, _, _ = strings.Cut(v.dest, "/")
		}
		if base == "" {
			base, lib = b, v.lib
		} else if b != base || v.lib.Name != lib.Name {
			return Result{Source: rel, Kind: KindExtra, Status: jobs.StatusSkipped, Error: "the release holds several titles"}
		}
	}
	if base == "" {
		return Result{Source: rel, Kind: KindExtra, Status: jobs.StatusSkipped, Error: "no matching video"}
	}
	dest := naming.ExtraPath(base, folder, filepath.Base(x.src))
	r := o.apply(x.src, lib, dest, dry, opts.Overwrite)
	r.Kind = KindExtra
	if !dry {
		o.rememberFile(x.src, lib, dest, r.Status, LedgerEntry{Kind: KindExtra})
	}
	return r
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
	ourCopy := func(cur os.FileInfo) bool { return o.copyOf(lib, dest, cur, fi) }

	if dry {
		if existing, err := lib.Root.Lstat(dest); err == nil {
			switch {
			case samePlaced(existing, fi, ourCopy):
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

	status, err := place(lib.Root, dest, f, fi, overwrite, o.noCopy, ourCopy)
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
	// An upgraded video's subtitles replace the old release's ones.
	r := o.apply(sub, target.lib, dest, dry, overwrite || target.upgraded)
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
