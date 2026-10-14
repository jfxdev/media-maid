// Package safepath validates and confines every filesystem path Media Maid
// touches. Only volumes mounted into the container are considered authorized.
package safepath

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"
)

var (
	// ErrInvalidPath is returned for malformed or out-of-root input paths.
	ErrInvalidPath = errors.New("invalid path")
	// ErrUnauthorizedVolume is returned when a path (usually through a symlink)
	// resolves outside every volume mounted into the container.
	ErrUnauthorizedVolume = errors.New("unauthorized volume")
	// ErrNotRegular is returned for devices, FIFOs, sockets and directories.
	ErrNotRegular = errors.New("not a regular file")
)

// Within reports whether p equals root or lies beneath it. Both must be clean
// absolute paths; the check is purely lexical.
func Within(root, p string) bool {
	if root == "/" {
		return strings.HasPrefix(p, "/")
	}
	return p == root || strings.HasPrefix(p, root+string(filepath.Separator))
}

// Guard knows which volumes are authorized.
type Guard struct {
	volumes []string
	devs    map[uint64]struct{}
}

// NewGuard builds a guard from authorized mount points. Mount points that
// cannot be stat'ed are dropped.
func NewGuard(volumes []string) (*Guard, error) {
	g := &Guard{devs: map[uint64]struct{}{}}
	for _, v := range volumes {
		v = filepath.Clean(v)
		var st syscall.Stat_t
		if err := syscall.Stat(v, &st); err != nil {
			continue
		}
		g.volumes = append(g.volumes, v)
		g.devs[uint64(st.Dev)] = struct{}{}
	}
	if len(g.volumes) == 0 {
		return nil, errors.New("no mounted volumes found: mount the downloads and library directories as volumes")
	}
	return g, nil
}

// Volumes returns the authorized mount points.
func (g *Guard) Volumes() []string { return append([]string(nil), g.volumes...) }

// Authorized reports whether the resolved absolute path p lies in a volume.
func (g *Guard) Authorized(p string) bool {
	for _, v := range g.volumes {
		if Within(v, p) {
			return true
		}
	}
	return false
}

// VolumeOf returns the authorized mount point holding the resolved path p
// (the deepest one when volumes are nested), or "" outside every volume.
// link(2) never works across mount points, even of the same filesystem, so
// two roots in different volumes cannot be hardlinked.
func (g *Guard) VolumeOf(p string) string {
	best := ""
	for _, v := range g.volumes {
		if Within(v, p) && len(v) > len(best) {
			best = v
		}
	}
	return best
}

// ResolveRoot resolves a configured root directory and checks it is a
// directory inside an authorized volume.
func (g *Guard) ResolveRoot(name, dir string) (string, error) {
	if !filepath.IsAbs(dir) {
		return "", fmt.Errorf("%s must be an absolute path", name)
	}
	r, err := filepath.EvalSymlinks(filepath.Clean(dir))
	if err != nil {
		return "", fmt.Errorf("%s: %w", name, err)
	}
	fi, err := os.Stat(r)
	if err != nil {
		return "", fmt.Errorf("%s: %w", name, err)
	}
	if !fi.IsDir() {
		return "", fmt.Errorf("%s is not a directory", name)
	}
	if !g.Authorized(r) {
		return "", fmt.Errorf("%s (%s) is not inside a mounted volume: %w", name, r, ErrUnauthorizedVolume)
	}
	return r, nil
}

// CleanInput validates a user-supplied path and returns it as a clean
// absolute path lexically inside root. Relative input is joined to root.
// Symlinks are not resolved here.
func CleanInput(root, input string) (string, error) {
	if input == "" || strings.ContainsRune(input, 0) {
		return "", ErrInvalidPath
	}
	for _, part := range strings.Split(filepath.ToSlash(input), "/") {
		if part == ".." {
			return "", ErrInvalidPath
		}
	}
	p := input
	if !filepath.IsAbs(p) {
		p = filepath.Join(root, p)
	}
	p = filepath.Clean(p)
	if !Within(root, p) {
		return "", ErrInvalidPath
	}
	return p, nil
}

// CleanRel validates a user-supplied path relative to some root (an
// os.Root) and returns it clean, with forward slashes; "" is the root itself
// ("."). Absolute paths, ".." components and NUL bytes are rejected, as in
// CleanInput. Symlinks are not resolved here.
func CleanRel(input string) (string, error) {
	if input == "" {
		return ".", nil
	}
	if strings.ContainsRune(input, 0) || strings.HasPrefix(input, "/") || filepath.IsAbs(input) {
		return "", ErrInvalidPath
	}
	for _, part := range strings.Split(input, "/") {
		if part == ".." {
			return "", ErrInvalidPath
		}
	}
	return path.Clean(input), nil
}

// Resolve follows symlinks in p and requires the final target to be inside an
// authorized volume. It does not need to be the same volume as p.
func (g *Guard) Resolve(p string) (string, error) {
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		return "", err
	}
	if !g.Authorized(r) {
		return "", ErrUnauthorizedVolume
	}
	return r, nil
}

// OpenRegular opens an already resolved path without following a final
// symlink and verifies, on the open descriptor, that it is a regular file on
// an authorized device. This closes the window between Resolve and use.
func (g *Guard) OpenRegular(resolved string) (*os.File, os.FileInfo, error) {
	f, err := os.OpenFile(resolved, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		if errors.Is(err, syscall.ELOOP) {
			return nil, nil, ErrUnauthorizedVolume
		}
		return nil, nil, err
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, nil, err
	}
	if !fi.Mode().IsRegular() {
		f.Close()
		return nil, nil, ErrNotRegular
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		f.Close()
		return nil, nil, ErrNotRegular
	}
	if _, ok := g.devs[uint64(st.Dev)]; !ok {
		f.Close()
		return nil, nil, ErrUnauthorizedVolume
	}
	return f, fi, nil
}
