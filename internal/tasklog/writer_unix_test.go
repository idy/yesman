//go:build !windows

package tasklog

import "os"

func openLateWriter(name string) (*os.File, error) {
	return os.OpenFile(name, os.O_APPEND|os.O_WRONLY, 0)
}
