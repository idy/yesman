//go:build windows

package pool

// noFollowNonblock: Windows has no FIFOs to wait on in a file system, and
// readSmall's callers check the files are regular.
const noFollowNonblock = 0
