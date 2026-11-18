//go:build darwin && cgo

package nativepermissions

import (
	"context"
	"runtime"
	"testing"
)

// Exercise the actual callback boundary without invoking OS request APIs.
func TestNativeCallbackStateAndRetirement(t *testing.T) {
	states := []string{"unknown", "not-determined", "restricted", "denied", "granted", "not-granted", "limited", "manual", "unavailable", "not-applicable"}
	for i, want := range states {
		done := make(chan nativeReply, 1)
		callbacks.Store(uint64(7), nativeWaiter{ctx: context.Background(), done: done})
		// C integer types are inferred for untyped constants at this boundary.
		switch i {
		case 0:
			goPermissionDone(7, 0, 0, 0, nil, nil)
		case 1:
			goPermissionDone(7, 1, 0, 1, nil, nil)
		case 2:
			goPermissionDone(7, 2, 0, 0, nil, nil)
		case 3:
			goPermissionDone(7, 3, 0, 0, nil, nil)
		case 4:
			goPermissionDone(7, 4, 0, 0, nil, nil)
		case 5:
			goPermissionDone(7, 5, 0, 0, nil, nil)
		case 6:
			goPermissionDone(7, 6, 0, 0, nil, nil)
		case 7:
			goPermissionDone(7, 7, 0, 0, nil, nil)
		case 8:
			goPermissionDone(7, 8, 0, 0, nil, nil)
		case 9:
			goPermissionDone(7, 9, 0, 0, nil, nil)
		}
		if got := (<-done).item.State; got != want {
			t.Fatalf("%s != %s", got, want)
		}
		goPermissionDone(7, 4, 0, 0, nil, nil) // duplicate callback after completion
		select {
		case <-done:
			t.Fatal("callback delivered twice")
		default:
		}
	}
	done := make(chan nativeReply, 1)
	ctx, cancel := context.WithCancel(context.Background())
	callbacks.Store(uint64(8), nativeWaiter{ctx: ctx, done: done})
	if goPermissionActive(8) == 0 {
		t.Fatal("missing active callback")
	}
	cancel() // reject even before the waiting goroutine gets scheduled
	if goPermissionActive(8) != 0 {
		t.Fatal("canceled queued operation is active")
	}
	callbacks.Delete(uint64(8)) // timeout / window closed
	if goPermissionActive(8) != 0 {
		t.Fatal("retired callback is active")
	}
	goPermissionDone(8, 4, 0, 0, nil, nil)
	select {
	case <-done:
		t.Fatal("late callback wrote to retired page")
	default:
	}
}

// The real public Security call on this test binary, which Go's linker signs
// ad hoc on Apple silicon. An unbundled binary never reports an app identity.
func TestSelfSigningAndBundleGuard(t *testing.T) {
	got := selfSigning()
	if got == "" {
		t.Fatal("own code signature unreadable")
	}
	if runtime.GOARCH == "arm64" && got != "adhoc" {
		t.Fatal(got)
	}
	if app := (macAdapter{}).identity(); app != nil {
		t.Fatal("test binary reported as the packaged app", app)
	}
}
