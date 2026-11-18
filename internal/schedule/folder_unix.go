//go:build !windows

package schedule

import "golang.org/x/sys/unix"

// openNoWait opens a FIFO without waiting for a writer; a regular file reads
// as it does without it.
const openNoWait = unix.O_NONBLOCK
