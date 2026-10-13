// Package orphans detects library entries that no longer match the
// filesystem: server items whose files are gone, directories without any
// video, and downloads no longer linked into the library. Removal only ever
// happens on explicit request, after revalidation, and never for downloads.
package orphans

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/jfxdev/media-maid/internal/config"
	"github.com/jfxdev/media-maid/internal/integration"
	"github.com/jfxdev/media-maid/internal/jobs"
	"github.com/jfxdev/media-maid/internal/organizer"
	"github.com/jfxdev/media-maid/internal/safepath"
)

// Orphan types.
const (
	TypeServerItemMissingFile = "server_item_missing_file"
	TypeOrphanDirectory       = "orphan_directory"
	// TypeUnlinkedDownload is a downloaded video with no other hard link: it
	// is not (or no longer) in the library but still uses disk space. It is
	// report-only; the torrent client owns those files.
	TypeUnlinkedDownload = "unlinked_download"
)

// errNotRemovable is returned when removal of a report-only type is asked.
const errNotRemovable = "not_removable"

// Orphan is one detected inconsistency.
type Orphan struct {
	ID           string    `json:"id"`
	Type         string    `json:"type"`
	Path         string    `json:"path"`
	ServerItemID string    `json:"server_item_id,omitempty"`
	Title        string    `json:"title,omitempty"`
	DetectedAt   time.Time `json:"detected_at"`

	// Size is the file size of an unlinked download, in bytes.
	Size int64 `json:"size,omitempty"`

	library string   // library name ("movies"/"tv") for directories
	rel     string   // path relative to the library root, for directories
	paths   []string // local paths for server items
}

// ReportError is a problem found while scanning (e.g. unauthorized volume).
type ReportError struct {
	ServerItemID string `json:"server_item_id,omitempty"`
	Title        string `json:"title,omitempty"`
	Error        string `json:"error"`
}

// Report is the result of an orphan scan.
type Report struct {
	GeneratedAt time.Time     `json:"generated_at"`
	ServerCheck string        `json:"server_check"` // "ok", "disabled" or "error"
	Orphans     []Orphan      `json:"orphans"`
	Errors      []ReportError `json:"errors,omitempty"`
}

// RemoveResult is the outcome of removing one orphan.
type RemoveResult struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

// Server is the subset of the integration client used here.
type Server interface {
	ListItems(ctx context.Context) ([]integration.ServerItem, error)
	// GetItem returns the item as the server knows it now (removal checks
	// the current paths, not the report's), or integration.ErrNotFound.
	GetItem(ctx context.Context, id string) (integration.ServerItem, error)
	DeleteItem(ctx context.Context, id string) error
}

// Detector finds and removes orphans.
type Detector struct {
	server    Server // nil when no media server is configured
	pathMap   *config.PathMap
	libraries []organizer.Library
	filter    organizer.Filter
	guard     *safepath.Guard
	history   *jobs.History
	log       *slog.Logger
	dryRun    bool
	downloads string
	ledger    *organizer.Ledger
	exclusive func(func())

	// OnChange, when set, is called after the report changes (see Export).
	OnChange func()

	mu     sync.Mutex
	report *Report
	byID   map[string]Orphan
}

// Options configures a Detector.
type Options struct {
	Server    Server
	PathMap   *config.PathMap
	Libraries []organizer.Library
	Filter    organizer.Filter
	Guard     *safepath.Guard
	History   *jobs.History
	Log       *slog.Logger
	DryRun    bool
	// Downloads, when set, enables the unlinked download report. Ledger tells
	// copies (one link by design) apart from files missing from the library.
	Downloads string
	Ledger    *organizer.Ledger
	// Exclusive, when set, runs a directory removal (with its revalidation)
	// while the organizer places nothing, so a file linked into the folder in
	// between is never deleted with it.
	Exclusive func(func())
}

// New builds a Detector.
func New(o Options) *Detector {
	return &Detector{
		server: o.Server, pathMap: o.PathMap, libraries: o.Libraries, filter: o.Filter,
		guard: o.Guard, history: o.History, log: o.Log, dryRun: o.DryRun,
		downloads: o.Downloads, ledger: o.Ledger, exclusive: o.Exclusive,
		byID: map[string]Orphan{},
	}
}

// metadataExts are files a media server or a user keeps next to a video
// (artwork, nfo, notes). A folder holding only these, or nothing, is an
// orphan; any other file (an ISO, a Blu-ray stream, a .strm, theme music)
// is content and keeps the folder.
var metadataExts = map[string]bool{
	".nfo": true, ".jpg": true, ".jpeg": true, ".png": true, ".webp": true, ".gif": true,
	".tbn": true, ".bif": true, ".xml": true, ".txt": true,
	".srt": true, ".ass": true, ".ssa": true, ".sub": true, ".idx": true, ".vtt": true, ".sup": true,
}

// metadataNames are system files that never count as content.
var metadataNames = map[string]bool{"thumbs.db": true, "desktop.ini": true, ".ds_store": true}

// isMetadata reports whether the library file p is only metadata: artwork,
// nfo, notes or a subtitle.
func (d *Detector) isMetadata(p string) bool {
	base := strings.ToLower(path.Base(p))
	ext := path.Ext(base)
	return metadataNames[base] || metadataExts[ext] || d.filter.Subtitle[ext]
}

func makeID(kind, key string) string {
	sum := sha256.Sum256([]byte(kind + "\x00" + key))
	return hex.EncodeToString(sum[:8])
}

// Report returns the latest report, or nil before the first scan.
func (d *Detector) Report() *Report {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.report == nil {
		return nil
	}
	r := *d.report
	r.Orphans = append([]Orphan(nil), d.report.Orphans...)
	return &r
}

// Scan builds a fresh report, replacing the previous one. It never removes.
func (d *Detector) Scan(ctx context.Context) (*Report, error) {
	now := time.Now().UTC()
	rep := &Report{GeneratedAt: now, ServerCheck: "disabled", Orphans: []Orphan{}}

	if d.server != nil {
		items, err := d.server.ListItems(ctx)
		if err != nil {
			rep.ServerCheck = "error"
			rep.Errors = append(rep.Errors, ReportError{Error: err.Error()})
			d.log.Warn("orphan scan: listing server items failed", "err", err)
		} else {
			rep.ServerCheck = "ok"
			for _, it := range items {
				o, rerr := d.checkItem(it)
				if rerr != nil {
					rep.Errors = append(rep.Errors, *rerr)
					continue
				}
				if o != nil {
					o.DetectedAt = now
					rep.Orphans = append(rep.Orphans, *o)
				}
			}
		}
	}

	for _, lib := range d.libraries {
		dirs, err := d.orphanDirs(lib.Root)
		if err != nil {
			rep.Errors = append(rep.Errors, ReportError{Error: lib.Name + ": " + err.Error()})
			continue
		}
		for _, rel := range dirs {
			rep.Orphans = append(rep.Orphans, Orphan{
				ID:         makeID(TypeOrphanDirectory, lib.Name+"/"+rel),
				Type:       TypeOrphanDirectory,
				Path:       lib.Name + "/" + rel,
				DetectedAt: now,
				library:    lib.Name,
				rel:        rel,
			})
		}
	}

	if d.downloads != "" {
		unlinked, err := d.unlinkedDownloads(ctx, now)
		if err != nil {
			rep.Errors = append(rep.Errors, ReportError{Error: "downloads: " + err.Error()})
		}
		rep.Orphans = append(rep.Orphans, unlinked...)
	}

	d.setReport(rep)
	d.log.Info("orphan scan finished", "orphans", len(rep.Orphans), "errors", len(rep.Errors), "server_check", rep.ServerCheck)
	return d.Report(), nil
}

func (d *Detector) setReport(rep *Report) {
	byID := make(map[string]Orphan, len(rep.Orphans))
	for _, o := range rep.Orphans {
		byID[o.ID] = o
	}
	d.mu.Lock()
	d.report, d.byID = rep, byID
	d.mu.Unlock()
	if d.OnChange != nil {
		d.OnChange()
	}
}

// unlinkedDownloads lists videos in the downloads folder whose only hard link
// is the download itself. Copies made across filesystems (recorded in the
// ledger, with their library file still present) are not reported.
func (d *Detector) unlinkedDownloads(ctx context.Context, now time.Time) ([]Orphan, error) {
	var out []Orphan
	err := filepath.WalkDir(d.downloads, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if e.IsDir() {
			if p != d.downloads && strings.HasPrefix(e.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(d.downloads, p)
		if err != nil || !e.Type().IsRegular() || !d.filter.IsVideo(rel) {
			return nil
		}
		fi, err := e.Info()
		if err != nil {
			return nil
		}
		st, ok := fi.Sys().(*syscall.Stat_t)
		if !ok {
			return nil
		}
		// Another hard link only proves the file is linked somewhere (a
		// cross-seed folder, say), not in the library: a download whose
		// recorded placement was deleted or replaced is reported anyway.
		// Without a record, doubt keeps it out of the report.
		if st.Nlink != 1 && !d.placementGone(fi) {
			return nil
		}
		if st.Nlink == 1 && d.isCopy(fi) {
			return nil
		}
		rel = filepath.ToSlash(rel)
		out = append(out, Orphan{
			ID:         makeID(TypeUnlinkedDownload, rel),
			Type:       TypeUnlinkedDownload,
			Path:       "downloads/" + rel,
			Size:       fi.Size(),
			DetectedAt: now,
		})
		return nil
	})
	return out, err
}

// isCopy reports whether fi was copied into the library and that copy is
// still there (not replaced by another release).
func (d *Detector) isCopy(fi os.FileInfo) bool {
	e, ok := d.ledger.Lookup(fi)
	if !ok || e.Status != jobs.StatusCopied {
		return false
	}
	for _, lib := range d.libraries {
		if lib.Name != e.Lib {
			continue
		}
		cur, err := lib.Root.Lstat(e.Dest)
		if errors.Is(err, fs.ErrNotExist) {
			return false
		}
		if err != nil {
			return true // doubt means keep it out of the report
		}
		// A copy keeps the source's size and mtime and is the library file
		// recorded when it was made; another file there (an upgrade replaced
		// it, even one with the same size and mtime) means this download is
		// no longer in use.
		return cur.Mode().IsRegular() && cur.Size() == fi.Size() && cur.ModTime().Equal(fi.ModTime()) &&
			(e.DestIno == 0 || e.DestIno == organizer.Inode(cur))
	}
	return false
}

// placementGone reports whether the ledger placed fi in a library and that
// placement is no longer there: deleted, or replaced by another file.
func (d *Detector) placementGone(fi os.FileInfo) bool {
	e, ok := d.ledger.Lookup(fi)
	if !ok {
		return false
	}
	for _, lib := range d.libraries {
		if lib.Name != e.Lib {
			continue
		}
		cur, err := lib.Root.Lstat(e.Dest)
		if errors.Is(err, fs.ErrNotExist) {
			return true
		}
		if err != nil || os.SameFile(cur, fi) {
			return false
		}
		return !(e.Status == jobs.StatusCopied && d.isCopy(fi))
	}
	return false
}

// persisted is the saved form of a report, including what removal needs.
type persisted struct {
	Report  *Report `json:"report"`
	Details []struct {
		ID      string   `json:"id"`
		Library string   `json:"library,omitempty"`
		Rel     string   `json:"rel,omitempty"`
		Paths   []string `json:"paths,omitempty"`
	} `json:"details"`
}

// Export returns the latest report as JSON (null before the first scan).
func (d *Detector) Export() ([]byte, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	p := persisted{Report: d.report}
	if d.report != nil {
		for _, o := range d.report.Orphans {
			p.Details = append(p.Details, struct {
				ID      string   `json:"id"`
				Library string   `json:"library,omitempty"`
				Rel     string   `json:"rel,omitempty"`
				Paths   []string `json:"paths,omitempty"`
			}{o.ID, o.library, o.rel, o.paths})
		}
	}
	return json.Marshal(p)
}

// Import restores a report saved by Export. Removal still revalidates each
// entry, so a report from before a restart is as safe as a fresh one.
func (d *Detector) Import(data []byte) error {
	var p persisted
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	if p.Report == nil {
		return nil
	}
	if p.Report.Orphans == nil {
		p.Report.Orphans = []Orphan{}
	}
	byID := map[string]int{}
	for i, o := range p.Report.Orphans {
		byID[o.ID] = i
	}
	for _, det := range p.Details {
		if i, ok := byID[det.ID]; ok {
			o := &p.Report.Orphans[i]
			o.library, o.rel, o.paths = det.Library, det.Rel, det.Paths
		}
	}
	m := make(map[string]Orphan, len(p.Report.Orphans))
	for _, o := range p.Report.Orphans {
		m[o.ID] = o
	}
	d.mu.Lock()
	d.report, d.byID = p.Report, m
	d.mu.Unlock()
	return nil
}

// locate maps a local absolute path to the managed library containing it.
func (d *Detector) locate(local string) (organizer.Library, string, bool) {
	for _, lib := range d.libraries {
		if safepath.Within(lib.Path, local) && local != lib.Path {
			rel, err := filepath.Rel(lib.Path, local)
			if err == nil {
				return lib, filepath.ToSlash(rel), true
			}
		}
	}
	return organizer.Library{}, "", false
}

// exists checks a library-relative path through os.Root, so a link that
// escapes to an unauthorized volume surfaces as ErrUnauthorizedVolume.
func exists(lib organizer.Library, rel string) (bool, error) {
	_, err := lib.Root.Lstat(rel)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, fs.ErrNotExist):
		return false, nil
	case strings.Contains(err.Error(), "path escapes from parent"):
		return false, safepath.ErrUnauthorizedVolume
	default:
		return false, err
	}
}

// checkItem returns an orphan only when every path of the item lies inside a
// managed library and none of them exist. An item with any path outside the
// managed libraries is ignored: removing it on the server could also remove
// media Media Maid does not manage (e.g. a Plex item with several parts).
func (d *Detector) checkItem(it integration.ServerItem) (*Orphan, *ReportError) {
	var managed []string
	var display string
	for _, p := range it.Paths {
		local := d.pathMap.Translate(p)
		if !filepath.IsAbs(local) {
			return nil, nil
		}
		lib, rel, ok := d.locate(local)
		if !ok {
			return nil, nil
		}
		ok, err := exists(lib, rel)
		if err != nil {
			msg := err.Error()
			if errors.Is(err, safepath.ErrUnauthorizedVolume) {
				msg = "unauthorized_volume"
			}
			return nil, &ReportError{ServerItemID: it.ID, Title: it.Title, Error: msg}
		}
		if ok {
			return nil, nil
		}
		managed = append(managed, local)
		if display == "" {
			display = lib.Name + "/" + rel
		}
	}
	if len(managed) == 0 {
		return nil, nil
	}
	return &Orphan{
		ID:           makeID(TypeServerItemMissingFile, it.ID),
		Type:         TypeServerItemMissingFile,
		Path:         display,
		ServerItemID: it.ID,
		Title:        it.Title,
		paths:        managed,
	}, nil
}

// orphanDirs returns the topmost directories (relative to root) that hold
// nothing but metadata (see isMetadata) anywhere in their tree. The root
// itself is never reported. Symlinked directories are not followed.
func (d *Detector) orphanDirs(root *os.Root) ([]string, error) {
	fsys := root.FS()
	hasContent := map[string]bool{}
	var dirs []string
	err := fs.WalkDir(fsys, ".", func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if e.IsDir() {
			if p != "." {
				dirs = append(dirs, p)
			}
			return nil
		}
		if !d.isMetadata(p) {
			for dir := path.Dir(p); ; dir = path.Dir(dir) {
				hasContent[dir] = true
				if dir == "." {
					break
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(dirs)
	var out []string
	for _, dir := range dirs {
		if hasContent[dir] {
			continue
		}
		parentOrphan := false
		for _, o := range out {
			if strings.HasPrefix(dir, o+"/") {
				parentOrphan = true
				break
			}
		}
		if !parentOrphan {
			out = append(out, dir)
		}
	}
	return out, nil
}

// Remove deletes the given orphans from the current report after
// revalidating each one. Unknown or no-longer-orphan IDs are "stale".
func (d *Detector) Remove(ctx context.Context, ids []string) []RemoveResult {
	out := make([]RemoveResult, 0, len(ids))
	for _, id := range ids {
		d.mu.Lock()
		o, ok := d.byID[id]
		d.mu.Unlock()
		if !ok {
			out = append(out, RemoveResult{ID: id, Status: jobs.StatusStale})
			continue
		}
		res := d.removeOne(ctx, o)
		if res.Status == jobs.StatusRemoved || res.Status == jobs.StatusStale {
			d.mu.Lock()
			delete(d.byID, id)
			if d.report != nil {
				kept := d.report.Orphans[:0:0]
				for _, x := range d.report.Orphans {
					if x.ID != id {
						kept = append(kept, x)
					}
				}
				d.report.Orphans = kept
			}
			d.mu.Unlock()
			if d.OnChange != nil {
				d.OnChange()
			}
		}
		d.history.Add(jobs.Entry{Action: jobs.ActionOrphanRm, Source: o.Path, Status: res.Status, Error: res.Error})
		out = append(out, res)
	}
	return out
}

func (d *Detector) removeOne(ctx context.Context, o Orphan) RemoveResult {
	res := RemoveResult{ID: o.ID}
	switch o.Type {
	case TypeServerItemMissingFile:
		if d.server == nil {
			res.Status, res.Error = jobs.StatusError, "sync_disabled"
			return res
		}
		// The report may be old: check the item as the server has it now.
		// Gone, moved, given a file, or with any path outside the managed
		// libraries, it is no longer the orphan that was reported.
		it, err := d.server.GetItem(ctx, o.ServerItemID)
		if errors.Is(err, integration.ErrNotFound) {
			res.Status = jobs.StatusStale
			return res
		}
		if err != nil {
			res.Status, res.Error = jobs.StatusError, err.Error()
			return res
		}
		if cur, rerr := d.checkItem(it); rerr != nil || cur == nil {
			res.Status = jobs.StatusStale
			return res
		}
		if d.dryRun {
			res.Status = jobs.StatusDryRun
			return res
		}
		if err := d.server.DeleteItem(ctx, o.ServerItemID); err != nil {
			if errors.Is(err, integration.ErrNotFound) {
				res.Status = jobs.StatusStale
				return res
			}
			res.Status, res.Error = jobs.StatusError, err.Error()
			return res
		}
		res.Status = jobs.StatusRemoved
		return res

	case TypeOrphanDirectory:
		var lib *organizer.Library
		for i := range d.libraries {
			if d.libraries[i].Name == o.library {
				lib = &d.libraries[i]
			}
		}
		rel := path.Clean(o.rel)
		if lib == nil || rel == "." || rel == "" || strings.HasPrefix(rel, "/") || rel == ".." || strings.HasPrefix(rel, "../") {
			res.Status, res.Error = jobs.StatusError, "invalid_path"
			return res
		}
		run := d.exclusive
		if run == nil {
			run = func(f func()) { f() }
		}
		run(func() { res = d.removeDir(*lib, rel, res) })
		return res

	case TypeUnlinkedDownload:
		res.Status, res.Error = jobs.StatusError, errNotRemovable
		return res
	}
	res.Status, res.Error = jobs.StatusError, "unknown orphan type"
	return res
}

// removeDir revalidates and removes the orphan directory rel of lib.
func (d *Detector) removeDir(lib organizer.Library, rel string, res RemoveResult) RemoveResult {
	fi, err := lib.Root.Lstat(rel)
	if err != nil || !fi.IsDir() {
		// Gone, replaced by a symlink or a file: no longer the orphan we reported.
		res.Status = jobs.StatusStale
		return res
	}
	if d.containsContent(lib.Root, rel) {
		res.Status = jobs.StatusStale
		return res
	}
	if d.dryRun {
		res.Status = jobs.StatusDryRun
		return res
	}
	if err := lib.Root.RemoveAll(rel); err != nil {
		res.Status, res.Error = jobs.StatusError, err.Error()
		return res
	}
	res.Status = jobs.StatusRemoved
	return res
}

// containsContent reports whether anything but metadata exists under rel.
// Walk errors count as content so nothing is removed on doubt.
func (d *Detector) containsContent(root *os.Root, rel string) bool {
	found := false
	err := fs.WalkDir(root.FS(), rel, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !e.IsDir() && !d.isMetadata(p) {
			found = true
			return fs.SkipAll
		}
		return nil
	})
	return found || err != nil
}
