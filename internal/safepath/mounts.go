package safepath

import (
	"bufio"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"
)

// pseudoFS are filesystem types that never hold user media.
var pseudoFS = map[string]bool{
	"proc": true, "sysfs": true, "devpts": true, "devtmpfs": true, "mqueue": true,
	"cgroup": true, "cgroup2": true, "securityfs": true, "debugfs": true,
	"tracefs": true, "pstore": true, "bpf": true, "hugetlbfs": true,
	"fusectl": true, "configfs": true, "binfmt_misc": true, "autofs": true,
	"nsfs": true, "efivarfs": true, "selinuxfs": true,
}

// injectedFiles are bind mounts the container runtime adds on its own.
var injectedFiles = map[string]bool{
	"/etc/hosts": true, "/etc/hostname": true, "/etc/resolv.conf": true,
}

var systemPrefixes = []string{"/proc", "/sys", "/dev"}

// ParseMountInfo reads /proc/self/mountinfo content and returns the mount
// points that are volumes mounted into the container: the root filesystem,
// pseudo filesystems and runtime-injected files are excluded.
func ParseMountInfo(r io.Reader) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		sep := -1
		for i, f := range fields {
			if f == "-" {
				sep = i
				break
			}
		}
		if len(fields) < 5 || sep < 0 || sep+1 >= len(fields) {
			continue
		}
		mountPoint := filepath.Clean(unescapeMount(fields[4]))
		fsType := fields[sep+1]
		if mountPoint == "/" || pseudoFS[fsType] || injectedFiles[mountPoint] || isSystemPath(mountPoint) {
			continue
		}
		if !seen[mountPoint] {
			seen[mountPoint] = true
			out = append(out, mountPoint)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read mountinfo: %w", err)
	}
	return out, nil
}

func isSystemPath(p string) bool {
	for _, pre := range systemPrefixes {
		if Within(pre, p) {
			return true
		}
	}
	return false
}

// unescapeMount decodes the octal escapes (\040 etc.) used by mountinfo.
func unescapeMount(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+4 <= len(s) {
			if v, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(v))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}
