package safepath

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
)

const mountinfo = `22 1 0:21 / / rw,relatime - overlay overlay rw,lowerdir=/x
23 22 0:22 / /proc rw,nosuid - proc proc rw
24 22 0:23 / /dev rw,nosuid - tmpfs tmpfs rw
25 24 0:24 / /dev/pts rw - devpts devpts rw
26 22 0:25 / /sys ro - sysfs sysfs ro
27 26 0:26 / /sys/fs/cgroup ro - cgroup2 cgroup rw
28 22 8:1 /var/lib/docker/volumes/data/_data /data rw,relatime - ext4 /dev/sda1 rw
29 22 8:1 /srv/media /media\040library rw,relatime - ext4 /dev/sda1 rw
30 22 8:1 /var/lib/docker/containers/x/hosts /etc/hosts rw - ext4 /dev/sda1 rw
31 22 8:1 /var/lib/docker/containers/x/hostname /etc/hostname rw - ext4 /dev/sda1 rw
32 22 8:1 /var/lib/docker/containers/x/resolv.conf /etc/resolv.conf rw - ext4 /dev/sda1 rw
33 24 0:27 / /dev/shm rw - tmpfs shm rw
34 22 0:28 / /downloads rw shared:1 - nfs4 nas:/downloads rw
`

func TestParseMountInfo(t *testing.T) {
	got, err := ParseMountInfo(strings.NewReader(mountinfo))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/data", "/media library", "/downloads"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestCleanInput(t *testing.T) {
	root := "/data/downloads"
	ok := map[string]string{
		"Movie.2010":              "/data/downloads/Movie.2010",
		"/data/downloads/a/b.mkv": "/data/downloads/a/b.mkv",
		"./a//b.mkv":              "/data/downloads/a/b.mkv",
		"/data/downloads":         "/data/downloads",
	}
	for in, want := range ok {
		got, err := CleanInput(root, in)
		if err != nil || got != want {
			t.Errorf("CleanInput(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	bad := []string{"", "../etc/passwd", "a/../../etc", "/etc/passwd", "/data/downloads-evil/x", "a\x00b", "/data/downloads/../media"}
	for _, in := range bad {
		if _, err := CleanInput(root, in); !errors.Is(err, ErrInvalidPath) {
			t.Errorf("CleanInput(%q) err = %v, want ErrInvalidPath", in, err)
		}
	}
}

func TestResolveAndOpen(t *testing.T) {
	vol := t.TempDir()
	vol2 := t.TempDir()
	outside := t.TempDir()
	g, err := NewGuard([]string{vol, vol2})
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(vol, "a.mkv")
	os.WriteFile(file, []byte("x"), 0o644)
	other := filepath.Join(vol2, "b.mkv")
	os.WriteFile(other, []byte("x"), 0o644)
	secret := filepath.Join(outside, "secret")
	os.WriteFile(secret, []byte("x"), 0o644)

	os.Symlink(other, filepath.Join(vol, "to-other-volume.mkv"))
	os.Symlink(secret, filepath.Join(vol, "to-outside.mkv"))
	os.Symlink("/etc/passwd", filepath.Join(vol, "passwd.mkv"))

	if r, err := g.Resolve(filepath.Join(vol, "to-other-volume.mkv")); err != nil || r != other {
		t.Errorf("link to another mounted volume: %q, %v", r, err)
	}
	for _, name := range []string{"to-outside.mkv", "passwd.mkv"} {
		if _, err := g.Resolve(filepath.Join(vol, name)); !errors.Is(err, ErrUnauthorizedVolume) {
			t.Errorf("%s: err = %v, want ErrUnauthorizedVolume", name, err)
		}
	}

	f, _, err := g.OpenRegular(file)
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	if _, _, err := g.OpenRegular(vol); !errors.Is(err, ErrNotRegular) {
		t.Errorf("directory: err = %v", err)
	}
	fifo := filepath.Join(vol, "fifo")
	if err := syscall.Mkfifo(fifo, 0o644); err == nil {
		if _, _, err := g.OpenRegular(fifo); !errors.Is(err, ErrNotRegular) {
			t.Errorf("fifo: err = %v", err)
		}
	}
	if _, _, err := g.OpenRegular(filepath.Join(vol, "to-outside.mkv")); err == nil {
		t.Error("OpenRegular followed a symlink")
	}
}

func TestResolveRoot(t *testing.T) {
	vol := t.TempDir()
	outside := t.TempDir()
	g, _ := NewGuard([]string{vol})
	if _, err := g.ResolveRoot("X", vol); err != nil {
		t.Error(err)
	}
	if _, err := g.ResolveRoot("X", outside); !errors.Is(err, ErrUnauthorizedVolume) {
		t.Errorf("err = %v", err)
	}
	link := filepath.Join(vol, "escape")
	os.Symlink(outside, link)
	if _, err := g.ResolveRoot("X", link); !errors.Is(err, ErrUnauthorizedVolume) {
		t.Errorf("symlinked root: err = %v", err)
	}
}

func TestVolumeOf(t *testing.T) {
	data, media := t.TempDir(), t.TempDir()
	nested := filepath.Join(data, "library")
	os.MkdirAll(nested, 0o755)
	g, err := NewGuard([]string{data, media, nested})
	if err != nil {
		t.Fatal(err)
	}
	tests := map[string]string{
		filepath.Join(data, "downloads"):       data,
		filepath.Join(nested, "movies"):        nested, // deepest mount wins
		filepath.Join(media, "tv"):             media,
		filepath.Join(filepath.Dir(data), "x"): "",
	}
	for p, want := range tests {
		if got := g.VolumeOf(p); got != want {
			t.Errorf("VolumeOf(%s) = %q, want %q", p, got, want)
		}
	}
}
