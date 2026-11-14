// Command launcher is the legacy yesman entry point. It contains no app,
// workspace, settings, or updater logic; the real application is Yes Man.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

func main() {
	exe, err := os.Executable()
	if err == nil {
		if real, e := filepath.EvalSymlinks(exe); e == nil {
			exe = real
		}
		target := filepath.Join(filepath.Dir(exe), "Yes Man")
		if runtime.GOOS == "windows" {
			target += ".exe"
		}
		err = launch(target, os.Args[1:])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "yesman:", err)
		os.Exit(1)
	}
}
