//go:build unix

package yesmen

import (
	"os"
	"syscall"
)

// In particular, opening a FIFO must not wait for a writer while metaMu is held.
const metadataOpenFlags = os.O_RDONLY | syscall.O_NONBLOCK
