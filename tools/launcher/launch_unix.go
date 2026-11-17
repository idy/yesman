//go:build !windows

package main

import (
	"os"
	"syscall"
)

func launch(target string, args []string) error {
	return syscall.Exec(target, append([]string{target}, args...), os.Environ())
}
