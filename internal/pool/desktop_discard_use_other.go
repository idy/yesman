//go:build !darwin && !linux && !windows

package pool

import (
	"context"
	"os"
)

func visitDesktopOpenFiles(ctx context.Context, _ func(string) bool) error { return ctx.Err() }
func checkDiscardSharing(*os.File, string) error                           { return nil }
