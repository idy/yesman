package chime

import (
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

var playSound = windows.NewLazySystemDLL("winmm.dll").NewProc("PlaySoundW")

func play(wav []byte) error {
	if err := playSound.Find(); err != nil {
		return ErrUnavailable
	}
	// SND_MEMORY | SND_NODEFAULT, synchronously. If the output fails, don't
	// substitute a system alert or start a second sound.
	ok, _, _ := playSound.Call(uintptr(unsafe.Pointer(&wav[0])), 0, 0x0004|0x0002)
	runtime.KeepAlive(wav)
	if ok == 0 {
		return ErrUnavailable
	}
	return nil
}
