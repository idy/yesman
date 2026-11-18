//go:build windows

package schedule

// openNoWait is nothing on Windows: a folder holds no FIFO there to wait on,
// named pipes live in a namespace of their own.
const openNoWait = 0
