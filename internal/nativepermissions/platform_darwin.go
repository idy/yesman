//go:build darwin && cgo

package nativepermissions

/*
#cgo CFLAGS: -mmacosx-version-min=13.0
#cgo LDFLAGS: -mmacosx-version-min=13.0 -framework Foundation -framework AppKit -framework AVFoundation -framework CoreBluetooth -framework UserNotifications -framework ApplicationServices -framework Security
#include <stdint.h>
#include <stdlib.h>
void ym_permission_start(uint64_t token, int permission, int action, const char *url);
int ym_permission_signing(void);
char *ym_permission_bundle(void);
*/
import "C"

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"unsafe"
)

type macAdapter struct{}

func newAdapter() adapter { return macAdapter{} }

type nativeReply struct {
	item Item
	err  error
}
type nativeWaiter struct {
	ctx  context.Context
	done chan nativeReply
}

var callbacks sync.Map
var callbackID atomic.Uint64

// C blocks capture only a numeric token. Removing it on timeout/close makes a
// late callback harmless, without freeing memory still owned by the OS.
//
//export goPermissionActive
func goPermissionActive(token C.uint64_t) C.int {
	if waiting, ok := callbacks.Load(uint64(token)); ok && waiting.(nativeWaiter).ctx.Err() == nil {
		return 1
	}
	return 0
}

//export goPermissionDone
func goPermissionDone(token C.uint64_t, state C.int, busy C.int, request C.int, detail *C.char, failure *C.char) {
	ch, ok := callbacks.LoadAndDelete(uint64(token))
	if !ok || ch.(nativeWaiter).ctx.Err() != nil {
		return
	}
	states := [...]string{"unknown", "not-determined", "restricted", "denied", "granted", "not-granted", "limited", "manual", "unavailable", "not-applicable"}
	name := "unknown"
	if state >= 0 && int(state) < len(states) {
		name = states[int(state)]
	}
	reply := nativeReply{item: Item{State: name, Detail: C.GoString(detail), Busy: busy != 0, Request: request != 0, Settings: true}}
	if text := C.GoString(failure); text != "" {
		reply.err = errors.New(text)
	}
	ch.(nativeWaiter).done <- reply
}
func macCall(ctx context.Context, id ID, action int) (Item, error) {
	if err := ctx.Err(); err != nil {
		return Item{}, err
	}
	index := -1
	for i, p := range ids {
		if id == p {
			index = i
			break
		}
	}
	if index < 0 {
		return Item{}, errors.New("Unknown permission")
	}
	token := callbackID.Add(1)
	done := make(chan nativeReply, 1)
	callbacks.Store(token, nativeWaiter{ctx: ctx, done: done})
	defer callbacks.Delete(token)
	url := C.CString(settingsURL("darwin", id))
	defer C.free(unsafe.Pointer(url))
	C.ym_permission_start(C.uint64_t(token), C.int(index), C.int(action), url)
	select {
	case reply := <-done:
		return reply.item, reply.err
	case <-ctx.Done():
		return Item{}, ctx.Err()
	}
}
func (macAdapter) query(ctx context.Context, id ID) (Item, error) { return macCall(ctx, id, 0) }
func (macAdapter) request(ctx context.Context, id ID) error {
	_, err := macCall(ctx, id, 1)
	return err
}
func (macAdapter) open(ctx context.Context, id ID) error { _, err := macCall(ctx, id, 2); return err }
func selfSigning() string {
	return [...]string{"", "unsigned", "adhoc", "signed"}[min(max(int(C.ym_permission_signing()), 0), 3)]
}
func (macAdapter) identity() *App {
	path := C.ym_permission_bundle()
	if path == nil {
		return nil
	}
	defer C.free(unsafe.Pointer(path))
	return &App{Path: C.GoString(path), Signing: selfSigning()}
}
