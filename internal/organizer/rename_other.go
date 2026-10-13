//go:build !linux

package organizer

import "golang.org/x/sys/unix"

// renameat2 is Linux only. Elsewhere (a developer's macOS build) overwrite
// and upgrades fail with errNoAtomicReplace instead of racing.
var renameat2 = func(int, string, int, string, uint) error { return unix.ENOSYS }

const (
	renameExchange  = 1 << 1
	renameNoReplace = 1 << 0
)
