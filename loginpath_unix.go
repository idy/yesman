//go:build !windows

package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// importLoginPath adds the login shell's PATH to the app's. An app opened
// from the Finder or a desktop menu inherits a minimal PATH that lacks pi
// and node when a package manager such as Homebrew installed them.
func importLoginPath() {
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, shell, "-l", "-c", `printf '%s' "$PATH"`).Output()
	if err != nil {
		return
	}
	seen := map[string]bool{}
	var dirs []string
	for _, list := range []string{string(out), os.Getenv("PATH")} {
		for _, d := range filepath.SplitList(strings.TrimSpace(list)) {
			if d != "" && !seen[d] {
				seen[d] = true
				dirs = append(dirs, d)
			}
		}
	}
	_ = os.Setenv("PATH", strings.Join(dirs, string(filepath.ListSeparator)))
}
