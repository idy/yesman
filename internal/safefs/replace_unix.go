//go:build !windows

package safefs

import "os"

// ReplaceSynced publishes a closed, synced sibling file and waits for the
// directory entry to reach storage before the caller acts on the new record.
func ReplaceSynced(root *os.Root, from, to string) error {
	if err := checkName(from); err != nil {
		return err
	}
	if err := checkName(to); err != nil {
		return err
	}
	if err := root.Rename(from, to); err != nil {
		return err
	}
	dir, err := root.Open(".")
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
