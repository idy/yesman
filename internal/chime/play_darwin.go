//go:build darwin && cgo

package chime

/*
#cgo CFLAGS: -mmacosx-version-min=13.0
#cgo LDFLAGS: -mmacosx-version-min=13.0 -framework Foundation -framework AVFoundation
#include <stddef.h>
int yesman_play_chime(const void *wav, size_t length);
*/
import "C"

import "unsafe"

func play(wav []byte) error {
	if C.yesman_play_chime(unsafe.Pointer(&wav[0]), C.size_t(len(wav))) == 0 {
		return ErrUnavailable
	}
	return nil
}
