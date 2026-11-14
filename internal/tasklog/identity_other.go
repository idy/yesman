//go:build !darwin && !linux && !windows

package tasklog

import (
	"errors"
	"os"
)

func fileIdentity(*os.File) (Identity, error) {
	return Identity{}, errors.New("stable Working log source identity is unsupported on this platform")
}
