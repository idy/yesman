package chime

import (
	"runtime"
	"syscall"
	"time"

	"github.com/ebitengine/purego"
)

// Use the desktop's default ALSA PCM. Its configuration can route to
// PipeWire/PulseAudio. Resolve the system library on demand so a headless
// machine or installation without ALSA can still start Yes Man normally.
func play(wav []byte) error {
	lib, err := purego.Dlopen("libasound.so.2", purego.RTLD_NOW|purego.RTLD_LOCAL)
	if err != nil {
		return ErrUnavailable
	}
	defer purego.Dlclose(lib)

	var (
		open      func(*uintptr, string, int32, int32) int32
		closePCM  func(uintptr) int32
		setParams func(uintptr, int32, int32, uint32, uint32, int32, uint32) int32
		write     func(uintptr, *byte, uintptr) int
		wait      func(uintptr, int32) int32
		drain     func(uintptr) int32
		state     func(uintptr) int32
	)
	for _, entry := range []struct {
		name string
		dest any
	}{
		{"snd_pcm_open", &open}, {"snd_pcm_close", &closePCM}, {"snd_pcm_set_params", &setParams},
		{"snd_pcm_writei", &write}, {"snd_pcm_wait", &wait}, {"snd_pcm_drain", &drain}, {"snd_pcm_state", &state},
	} {
		symbol, err := purego.Dlsym(lib, entry.name)
		if err != nil {
			return ErrUnavailable
		}
		purego.RegisterFunc(entry.dest, symbol)
	}

	const (
		playback   = 0
		nonblock   = 1
		signed16LE = 2
		interleave = 3
		setup      = 1
	)
	var pcm uintptr
	if open(&pcm, "default", playback, nonblock) < 0 {
		return ErrUnavailable
	}
	defer closePCM(pcm)
	if setParams(pcm, signed16LE, interleave, 1, sampleRate, 1, 80000) < 0 {
		return ErrUnavailable
	}
	const again = -int(syscall.EAGAIN)
	deadline := time.Now().Add(2 * time.Second)
	for samples := wav[44:]; len(samples) > 0; {
		if time.Now().After(deadline) || wait(pcm, 50) < 0 {
			return ErrUnavailable
		}
		written := write(pcm, &samples[0], uintptr(len(samples)/2))
		runtime.KeepAlive(samples)
		if written == again || written == 0 {
			continue
		}
		if written < 0 || written > len(samples)/2 {
			return ErrUnavailable
		}
		samples = samples[written*2:]
	}
	result := drain(pcm)
	if int(result) == again {
		// Nonblocking hw drains finish in SETUP; plugins that drain in
		// the call itself return 0. Don't keep playing after a lost device.
		for time.Now().Before(deadline) {
			if state(pcm) == setup {
				return nil
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	if result != 0 {
		return ErrUnavailable
	}
	return nil
}
