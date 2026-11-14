package main

import (
	"os"
	"path/filepath"
)

// writePrivate replaces path with data readable only by the user. It
// writes a file of its own beside path, which CreateTemp makes 0600, and
// renames it over path, so the data is never in a file others can read,
// whatever the permissions of a file it replaces.
func writePrivate(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // nothing is left after the rename
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
