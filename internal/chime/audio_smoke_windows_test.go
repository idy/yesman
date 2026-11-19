//go:build windows

package chime

import (
	"crypto/sha256"
	"errors"
	"os"
	"runtime"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestWindowsWinMMSmoke(t *testing.T) {
	if os.Getenv("YESMAN_WINMM_SMOKE") != "1" {
		t.Skip("requires an isolated Windows host with an audio endpoint")
	}
	winmm := windows.NewLazySystemDLL("winmm.dll")
	deviceCountAPI := winmm.NewProc("waveOutGetNumDevs")
	mapperAPI := winmm.NewProc("waveOutOpen")
	for _, api := range []*windows.LazyProc{deviceCountAPI, mapperAPI} {
		if err := api.Find(); err != nil {
			t.Fatal(err)
		}
	}
	deviceCount, _, _ := deviceCountAPI.Call()
	// WAVEFORMATEX for mono, signed 16-bit PCM at the product sample rate.
	format := struct {
		Tag, Channels                        uint16
		SamplesPerSec, AverageBytesPerSec    uint32
		BlockAlign, BitsPerSample, ExtraSize uint16
	}{1, 1, sampleRate, 2 * sampleRate, 2, 16, 0}
	const waveMapper = 0xffffffff
	const waveFormatQuery = 1
	// WAVE_FORMAT_QUERY only asks the default mapper whether it accepts the format.
	mapperResult, _, _ := mapperAPI.Call(0, waveMapper, uintptr(unsafe.Pointer(&format)), 0, 0, waveFormatQuery)
	runtime.KeepAlive(&format)
	t.Logf("os=%s arch=%s endpoint_count=%d mapper_pcm_query=%d", runtime.GOOS, runtime.GOARCH, deviceCount, mapperResult)
	if deviceCount == 0 || mapperResult != 0 {
		t.Skip("host capability gap: WinMM default PCM endpoint unavailable")
	}

	valid := wav(Double)
	clear(valid[44:])
	t.Logf("case=silent-pcm backend=PlaySoundW zero_pcm=true wav_sha256=%x pcm_sha256=%x bytes=%d",
		sha256.Sum256(valid), sha256.Sum256(valid[44:]), len(valid)-44)
	if err := play(valid); err != nil {
		t.Fatalf("PlaySoundW rejected valid silent PCM: %v", err)
	}
	t.Log("case=silent-pcm native_acceptance=true")

	// Change the RIFF form while preserving the lengths and the silent PCM.
	invalid := append([]byte(nil), valid...)
	copy(invalid[8:12], "WAVX")
	if err := play(invalid); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("PlaySoundW should reject the non-WAVE RIFF form without system fallback: %v", err)
	}
	t.Logf("case=invalid-riff-form native_acceptance=false wav_sha256=%x pcm_sha256=%x",
		sha256.Sum256(invalid), sha256.Sum256(invalid[44:]))
}
