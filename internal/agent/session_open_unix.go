//go:build !windows

package agent

import (
	"os"
	"syscall"
)

// sessionOpenFlags open a session file for reading without waiting, should
// a named pipe have taken its place.
const sessionOpenFlags = os.O_RDONLY | syscall.O_NONBLOCK
