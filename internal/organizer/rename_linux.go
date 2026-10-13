package organizer

import "golang.org/x/sys/unix"

// renameat2 swaps or moves entries atomically (see swapIn and removeChecked).
var renameat2 = unix.Renameat2

const (
	renameExchange  = unix.RENAME_EXCHANGE
	renameNoReplace = unix.RENAME_NOREPLACE
)
