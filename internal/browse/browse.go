// Package browse lists the folders of the configured roots (downloads and
// libraries) for the web UI. It only reads directory entries and their
// metadata: a regular file is never opened, a final symlink is never
// followed, and every access goes through an os.Root.
package browse

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/jfxdev/media-maid/internal/safepath"
)

// Entry types.
const (
	TypeDir     = "dir"
	TypeFile    = "file"
	TypeSymlink = "symlink"
	TypeOther   = "other"
)

// Media classes, from the extension allowlists.
const (
	MediaVideo    = "video"
	MediaSubtitle = "subtitle"
)

var (
	// ErrUnknownRoot is returned for a root name that is not configured.
	ErrUnknownRoot = errors.New("unknown root")
	// ErrNotFound is returned when the folder does not exist.
	ErrNotFound = errors.New("not found")
	// ErrNotDir is returned for a file, a symlink or anything that is not a
	// directory: only folders are listed, nothing is read.
	ErrNotDir = errors.New("not a directory")
)

const (
	// DefaultLimit and MaxLimit bound a page of entries.
	DefaultLimit = 200
	MaxLimit     = 1000
	// defaultMaxEntries bounds how many names of one folder are read (and
	// sorted) per request; a larger folder is reported as truncated.
	defaultMaxEntries = 10000
	readBatch         = 1000
)

// Entry is one item of a folder. Nothing in it comes from file contents.
type Entry struct {
	Name     string    `json:"name"`
	Type     string    `json:"type"`
	Size     int64     `json:"size,omitempty"`
	Modified time.Time `json:"modified"`
	// Hidden is a name starting with a dot.
	Hidden bool `json:"hidden"`
	// Links is the hard link count of a regular file: 1 means no other copy
	// (a download not in the library, or a library file not from downloads).
	Links uint64 `json:"links,omitempty"`
	// Media is "video" or "subtitle" by extension, else empty.
	Media string `json:"media,omitempty"`
}

// Listing is one page of a folder, directories first, then by name.
type Listing struct {
	Root    string  `json:"root"`
	Path    string  `json:"path"`
	Entries []Entry `json:"entries"`
	// Total is the number of entries read; Truncated means the folder holds
	// more than could be read in one request.
	Total     int  `json:"total"`
	Truncated bool `json:"truncated"`
}

// Lister lists folders of named roots.
type Lister struct {
	roots      map[string]*os.Root
	video      map[string]bool
	subtitle   map[string]bool
	maxEntries int
}

// New returns a Lister over roots (by name, e.g. "downloads", "movies",
// "tv"). video and subtitle are extension allowlists (".mkv") used to mark
// entries; either may be nil.
func New(roots map[string]*os.Root, video, subtitle map[string]bool) *Lister {
	return &Lister{roots: roots, video: video, subtitle: subtitle, maxEntries: defaultMaxEntries}
}

// Roots returns the configured root names, sorted.
func (l *Lister) Roots() []string {
	out := make([]string, 0, len(l.roots))
	for n := range l.roots {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// List returns entries [offset, offset+limit) of the folder rel inside root.
func (l *Lister) List(ctx context.Context, root, rel string, offset, limit int) (*Listing, error) {
	r, ok := l.roots[root]
	if !ok {
		return nil, ErrUnknownRoot
	}
	rel, err := safepath.CleanRel(rel)
	if err != nil {
		return nil, err
	}
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 || limit > MaxLimit {
		limit = DefaultLimit
	}
	fi, err := r.Lstat(rel)
	if err != nil {
		return nil, mapErr(err)
	}
	if !fi.IsDir() {
		// A file, or a symlink: the final symlink is never followed.
		return nil, ErrNotDir
	}
	// O_DIRECTORY: if the folder was swapped for a FIFO or a file since the
	// Lstat, the open fails (ENOTDIR) instead of blocking or opening it.
	d, err := r.OpenFile(rel, os.O_RDONLY|syscall.O_DIRECTORY, 0)
	if err != nil {
		return nil, mapErr(err)
	}
	defer d.Close()

	var names []os.DirEntry
	truncated := false
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		batch, err := d.ReadDir(readBatch)
		names = append(names, batch...)
		if len(names) > l.maxEntries {
			names, truncated = names[:l.maxEntries], true
			break
		}
		if errors.Is(err, io.EOF) || (err == nil && len(batch) == 0) {
			break
		}
		if err != nil {
			return nil, mapErr(err)
		}
	}
	sort.Slice(names, func(i, j int) bool {
		di, dj := names[i].IsDir(), names[j].IsDir()
		if di != dj {
			return di
		}
		a, b := strings.ToLower(names[i].Name()), strings.ToLower(names[j].Name())
		if a != b {
			return a < b
		}
		return names[i].Name() < names[j].Name()
	})

	out := &Listing{Root: root, Path: rel, Entries: []Entry{}, Total: len(names), Truncated: truncated}
	if rel == "." {
		out.Path = ""
	}
	if offset >= len(names) {
		return out, nil
	}
	end := min(offset+limit, len(names))
	for _, de := range names[offset:end] {
		out.Entries = append(out.Entries, l.entry(r, rel, de))
	}
	return out, nil
}

// entry describes de. Its metadata comes from Lstat through the os.Root, never
// from DirEntry.Info, which stats by absolute path outside the root.
func (l *Lister) entry(r *os.Root, dir string, de os.DirEntry) Entry {
	name := de.Name()
	e := Entry{Name: name, Hidden: strings.HasPrefix(name, "."), Type: typeOf(de.Type())}
	fi, err := r.Lstat(path.Join(dir, name))
	if err != nil {
		// Gone since it was listed, or unreadable: keep the name only.
		return e
	}
	e.Type = typeOf(fi.Mode().Type())
	e.Modified = fi.ModTime().UTC()
	if e.Type == TypeFile {
		e.Size = fi.Size()
		if st, ok := fi.Sys().(*syscall.Stat_t); ok {
			e.Links = uint64(st.Nlink)
		}
		ext := strings.ToLower(path.Ext(name))
		switch {
		case l.video[ext]:
			e.Media = MediaVideo
		case l.subtitle[ext]:
			e.Media = MediaSubtitle
		}
	}
	return e
}

func typeOf(m fs.FileMode) string {
	switch {
	case m.IsDir():
		return TypeDir
	case m.IsRegular():
		return TypeFile
	case m&fs.ModeSymlink != 0:
		return TypeSymlink
	}
	return TypeOther
}

// mapErr turns filesystem errors into the package's errors.
func mapErr(err error) error {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return ErrNotFound
	case errors.Is(err, syscall.ENOTDIR):
		return ErrNotDir
	case errors.Is(err, fs.ErrPermission):
		return fs.ErrPermission
	case strings.Contains(err.Error(), "path escapes from parent"):
		return safepath.ErrUnauthorizedVolume
	}
	return err
}
