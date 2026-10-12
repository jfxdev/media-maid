package organizer

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/jfxdev/media-maid/internal/jobs"
	"github.com/jfxdev/media-maid/internal/safepath"
)

// place hardlinks src (an open, verified regular file) to rel inside root,
// falling back to a copy when the two live on different filesystems.
// The destination is always created through os.Root, so it cannot escape the
// library even if a folder inside it is a symlink pointing elsewhere.
//
// An existing different destination is a conflict, unless overwrite is set and
// the destination is a regular file: it is then atomically replaced.
func place(root *os.Root, rel string, src *os.File, srcInfo os.FileInfo, overwrite bool) (string, error) {
	dir, name := path.Split(rel)
	dir = strings.TrimSuffix(dir, "/")
	if dir == "" {
		dir = "."
	}
	if err := checkEscape(root, dir); err != nil {
		return jobs.StatusError, err
	}
	if err := root.MkdirAll(dir, 0o755); err != nil {
		return jobs.StatusError, mapRootErr(err)
	}
	if existing, err := root.Lstat(rel); err == nil {
		if samePlaced(existing, srcInfo) {
			return jobs.StatusExists, nil
		}
		if !overwrite || !existing.Mode().IsRegular() {
			return jobs.StatusConflict, nil
		}
		return replaceWith(root, rel, src, srcInfo, existing)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return jobs.StatusError, mapRootErr(err)
	}

	d, err := root.Open(dir)
	if err != nil {
		return jobs.StatusError, mapRootErr(err)
	}
	defer d.Close()

	// Link the already-open source descriptor into the already-open destination
	// directory, so neither side can be swapped between check and use.
	srcProc := "/proc/self/fd/" + strconv.Itoa(int(src.Fd()))
	err = unix.Linkat(unix.AT_FDCWD, srcProc, int(d.Fd()), name, unix.AT_SYMLINK_FOLLOW)
	switch {
	case err == nil:
		return jobs.StatusLinked, nil
	case errors.Is(err, unix.EEXIST):
		return jobs.StatusConflict, nil
	case errors.Is(err, unix.EXDEV), errors.Is(err, unix.EPERM), errors.Is(err, unix.EMLINK):
		return copyInto(root, rel, src, srcInfo)
	default:
		return jobs.StatusError, fmt.Errorf("hardlink: %w", err)
	}
}

// tmpName returns a fresh temporary name next to rel. It is unique per
// placement, so an existing file is never reused or removed.
func tmpName(rel string) string {
	return path.Join(path.Dir(rel), "."+path.Base(rel)+"."+rand.Text()[:12]+".mediamaid-tmp")
}

// copyTemp copies src into a new temporary file tmp, keeping the source mtime
// so later runs recognise the copy (see samePlaced). tmp is removed on failure.
func copyTemp(root *os.Root, tmp string, src *os.File, srcInfo os.FileInfo) error {
	dst, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return mapRootErr(err)
	}
	if _, err := src.Seek(0, io.SeekStart); err != nil {
		dst.Close()
		root.Remove(tmp)
		return err
	}
	if _, err := io.Copy(dst, src); err != nil {
		dst.Close()
		root.Remove(tmp)
		return fmt.Errorf("copy: %w", err)
	}
	if err := dst.Close(); err != nil {
		root.Remove(tmp)
		return err
	}
	if err := root.Chtimes(tmp, srcInfo.ModTime(), srcInfo.ModTime()); err != nil {
		root.Remove(tmp)
		return err
	}
	return nil
}

func copyInto(root *os.Root, rel string, src *os.File, srcInfo os.FileInfo) (string, error) {
	tmp := tmpName(rel)
	if err := copyTemp(root, tmp, src, srcInfo); err != nil {
		return jobs.StatusError, err
	}
	// Commit with a hardlink instead of rename: link(2) never replaces an
	// existing destination, so a file created concurrently is not overwritten.
	d, err := root.Open(path.Dir(rel))
	if err != nil {
		root.Remove(tmp)
		return jobs.StatusError, mapRootErr(err)
	}
	defer d.Close()
	err = unix.Linkat(int(d.Fd()), path.Base(tmp), int(d.Fd()), path.Base(rel), 0)
	root.Remove(tmp)
	if errors.Is(err, unix.EEXIST) {
		return jobs.StatusConflict, nil
	}
	if err != nil {
		return jobs.StatusError, fmt.Errorf("commit copy: %w", err)
	}
	return jobs.StatusCopied, nil
}

// replaceWith stages src next to rel (hardlink, or copy across filesystems)
// and swaps it with rel. Both steps go through the directory descriptor
// opened by os.Root. Only the file checked by the caller (checked) is ever
// replaced: see swapIn.
func replaceWith(root *os.Root, rel string, src *os.File, srcInfo, checked os.FileInfo) (string, error) {
	tmp := tmpName(rel)
	d, err := root.Open(path.Dir(rel))
	if err != nil {
		return jobs.StatusError, mapRootErr(err)
	}
	defer d.Close()
	srcProc := "/proc/self/fd/" + strconv.Itoa(int(src.Fd()))
	err = unix.Linkat(unix.AT_FDCWD, srcProc, int(d.Fd()), path.Base(tmp), unix.AT_SYMLINK_FOLLOW)
	switch {
	case err == nil:
	case errors.Is(err, unix.EXDEV), errors.Is(err, unix.EPERM), errors.Is(err, unix.EMLINK):
		if err := copyTemp(root, tmp, src, srcInfo); err != nil {
			return jobs.StatusError, err
		}
	default:
		return jobs.StatusError, fmt.Errorf("hardlink: %w", err)
	}
	return swapIn(root, d, tmp, rel, checked)
}

// errNoAtomicReplace is returned where the filesystem cannot exchange two
// entries atomically: overwrite is refused rather than raced.
var errNoAtomicReplace = errors.New("overwrite needs renameat2 RENAME_EXCHANGE, which this filesystem does not support")

// Test hooks.
var (
	renameat2        = unix.Renameat2
	testHookRollback = func() {}
)

// swapIn atomically exchanges the staged tmp with rel, then checks that what
// came out is still the regular file the caller inspected; only then is it
// removed. Otherwise the exchange is undone and the result is a conflict.
// Nothing is ever deleted unless it is provably the checked file or our own
// staged file: if a writer races the rollback, both entries are kept.
func swapIn(root *os.Root, d *os.File, tmp, rel string, checked os.FileInfo) (string, error) {
	staged, err := root.Lstat(tmp)
	if err != nil {
		return jobs.StatusError, mapRootErr(err)
	}
	dfd, t, n := int(d.Fd()), path.Base(tmp), path.Base(rel)
	switch err := renameat2(dfd, t, dfd, n, unix.RENAME_EXCHANGE); {
	case err == nil:
	case errors.Is(err, unix.EINVAL), errors.Is(err, unix.ENOTSUP), errors.Is(err, unix.ENOSYS):
		root.Remove(tmp) // untouched, still ours
		return jobs.StatusError, errNoAtomicReplace
	case errors.Is(err, unix.ENOENT):
		root.Remove(tmp)
		return jobs.StatusConflict, nil // the destination disappeared
	default:
		root.Remove(tmp)
		return jobs.StatusError, fmt.Errorf("replace: %w", err)
	}

	// tmp has a random name nobody else writes, so what it holds now is
	// exactly what was at rel.
	if out, err := root.Lstat(tmp); err == nil && unchanged(out, checked) {
		root.Remove(tmp)
		return jobs.StatusReplaced, nil
	}

	// Someone else's entry came out: put it back.
	testHookRollback()
	if err := renameat2(dfd, t, dfd, n, unix.RENAME_EXCHANGE); err != nil {
		return jobs.StatusError, fmt.Errorf("replace interrupted, previous entry kept as %s: %w", t, err)
	}
	if back, err := root.Lstat(tmp); err == nil && unchanged(back, staged) {
		root.Remove(tmp)
		return jobs.StatusConflict, nil
	}
	// rel changed again during the rollback: tmp now holds that writer's
	// entry. Keep it.
	return jobs.StatusError, fmt.Errorf("destination changed during replace, entry kept as %s", t)
}

// unchanged reports whether cur is still the regular file seen as checked.
// Size and mtime guard against an inode number reused by a new file.
func unchanged(cur, checked os.FileInfo) bool {
	return cur.Mode().IsRegular() && os.SameFile(cur, checked) &&
		cur.Size() == checked.Size() && cur.ModTime().Equal(checked.ModTime())
}

// samePlaced reports whether dest already holds src: the same inode for a
// hardlink, or a regular file of identical size and mtime for a copy.
func samePlaced(dest, src os.FileInfo) bool {
	if os.SameFile(dest, src) {
		return true
	}
	return dest.Mode().IsRegular() && dest.Size() == src.Size() && dest.ModTime().Equal(src.ModTime())
}

// checkEscape reports ErrUnauthorizedVolume when an existing component of dir
// is a symlink leading outside the library root.
func checkEscape(root *os.Root, dir string) error {
	cur := ""
	for _, part := range strings.Split(dir, "/") {
		if part == "" || part == "." {
			continue
		}
		cur = path.Join(cur, part)
		fi, err := root.Lstat(cur)
		if err != nil {
			return nil // does not exist yet (or cannot be inspected); MkdirAll decides
		}
		if fi.Mode()&fs.ModeSymlink != 0 {
			if _, err := root.Stat(cur); err != nil {
				if mapped := mapRootErr(err); errors.Is(mapped, safepath.ErrUnauthorizedVolume) {
					return mapped
				}
			}
		}
	}
	return nil
}

// mapRootErr turns os.Root escape errors into ErrUnauthorizedVolume.
func mapRootErr(err error) error {
	if err != nil && strings.Contains(err.Error(), "path escapes from parent") {
		return safepath.ErrUnauthorizedVolume
	}
	return err
}
