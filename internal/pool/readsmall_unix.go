//go:build !windows

package pool

import "syscall"

// noFollowNonblock opens without following a final link or waiting on a FIFO.
const noFollowNonblock = syscall.O_NOFOLLOW | syscall.O_NONBLOCK
