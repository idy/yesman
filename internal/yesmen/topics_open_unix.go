//go:build unix

package yesmen

import "syscall"

// topicsOpenFlags opens topics.json without waiting: a FIFO put in its place
// would otherwise block the open, holding the topics lock and the workspace's
// use lease. The opened file is then checked to be a regular file.
const topicsOpenFlags = syscall.O_NONBLOCK
