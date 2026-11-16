//go:build windows

package agent

// syncDir is a no-op: Windows makes a new name durable with the file.
func syncDir(string) {}
