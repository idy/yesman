//go:build darwin && cgo

// Read-only native linkage probe. It never calls GrantAction or request/open.
// Run from a temporary bundle to inspect that copy's status, or unbundled to
// verify the bundle guard. It does not initialize yesman data or agent services.
package main

/*
#include <dispatch/dispatch.h>
*/
import "C"

import (
	"context"
	"encoding/json"
	"github.com/idy/yesman/internal/nativepermissions"
	"os"
	"runtime"
)

func init() { runtime.LockOSThread() }
func main() {
	go func() {
		view := nativepermissions.New().List(context.Background())
		if err := json.NewEncoder(os.Stdout).Encode(view); err != nil {
			os.Exit(1)
		}
		for _, item := range view.Items {
			if item.Error != "" {
				os.Exit(1)
			}
		}
		os.Exit(0)
	}()
	C.dispatch_main()
}
