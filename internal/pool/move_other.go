//go:build !darwin && !linux && !windows

package pool

import "errors"

func moveNoReplace(from, to string) error {
	return errors.New("this platform does not support exclusive worktree moves")
}
