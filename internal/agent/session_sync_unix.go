//go:build !windows

package agent

import "os"

// syncDir makes a new name in dir durable.
func syncDir(dir string) {
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		d.Close()
	}
}
