package organizer

import (
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
	replace := false
	if existing, err := root.Lstat(rel); err == nil {
		if samePlaced(existing, srcInfo) {
			return jobs.StatusExists, nil
		}
		if !overwrite || !existing.Mode().IsRegular() {
			return jobs.StatusConflict, nil
		}
		replace = true
	} else if !errors.Is(err, fs.ErrNotExist) {
		return jobs.StatusError, mapRootErr(err)
	}
	if replace {
		return replaceWith(root, rel, src, srcInfo)
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

func tmpName(rel string) string {
	return path.Join(path.Dir(rel), "."+path.Base(rel)+".mediamaid-tmp")
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
// and renames it over rel, so the destination is swapped atomically. Both
// steps go through the directory descriptor opened by os.Root.
func replaceWith(root *os.Root, rel string, src *os.File, srcInfo os.FileInfo) (string, error) {
	tmp := tmpName(rel)
	d, err := root.Open(path.Dir(rel))
	if err != nil {
		return jobs.StatusError, mapRootErr(err)
	}
	defer d.Close()
	root.Remove(tmp) // a stale temporary from an interrupted run
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
	if err := unix.Renameat(int(d.Fd()), path.Base(tmp), int(d.Fd()), path.Base(rel)); err != nil {
		root.Remove(tmp)
		return jobs.StatusError, fmt.Errorf("replace: %w", err)
	}
	return jobs.StatusReplaced, nil
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
