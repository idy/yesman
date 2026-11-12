// Package home locates yesman's data directory.
//
// Everything yesman owns lives under one root, ~/yesman by default:
//
//	repos/     the repo pool: bare repositories, written only by yesman
//	yesmen/    one workspace directory per hired yesman
//	archive/   workspaces of dismissed yesmen
//	.runtime/  files yesman writes on each start, such as its pi extension
package home

import (
	"fmt"
	"os"
	"path/filepath"
)

// Home is the root of yesman's data.
type Home struct {
	Root string
}

// Default returns $YESMAN_HOME, or ~/yesman.
func Default() (Home, error) {
	if dir := os.Getenv("YESMAN_HOME"); dir != "" {
		abs, err := filepath.Abs(dir)
		if err != nil {
			return Home{}, err
		}
		return Home{Root: abs}, nil
	}
	dir, err := os.UserHomeDir()
	if err != nil {
		return Home{}, fmt.Errorf("find the home directory: %w", err)
	}
	return Home{Root: filepath.Join(dir, "yesman")}, nil
}

// Repos is the repo pool.
func (h Home) Repos() string { return filepath.Join(h.Root, "repos") }

// Yesmen holds the workspaces of hired yesmen.
func (h Home) Yesmen() string { return filepath.Join(h.Root, "yesmen") }

// Archive holds the workspaces of dismissed yesmen.
func (h Home) Archive() string { return filepath.Join(h.Root, "archive") }

// Runtime holds files rewritten on every start.
func (h Home) Runtime() string { return filepath.Join(h.Root, ".runtime") }

// Ensure creates the directories.
func (h Home) Ensure() error {
	for _, dir := range []string{h.Repos(), h.Yesmen(), h.Archive(), h.Runtime()} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return nil
}
