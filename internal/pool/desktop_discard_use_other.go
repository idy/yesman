//go:build !darwin && !linux && !windows

package pool

import (
	"context"
	"os"
)

func checkDesktopWorktreeUse(ctx context.Context, _ string) error { return ctx.Err() }
func checkDiscardSharing(*os.File, string) error                  { return nil }
