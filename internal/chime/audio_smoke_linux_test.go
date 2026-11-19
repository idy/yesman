//go:build linux

package chime

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
)

func TestLinuxALSASmoke(t *testing.T) {
	mode := os.Getenv("YESMAN_ALSA_SMOKE")
	if mode == "" {
		t.Skip("execute the two native probe cases in separate test processes")
	}
	if mode != "capture" && mode != "missing-default" {
		t.Fatalf("unknown ALSA probe case %q", mode)
	}
	dir := t.TempDir()
	pcmPath := filepath.Join(dir, "capture.raw")
	config := "pcm.fixture_unused { type null }\n"
	if mode == "capture" {
		config = "pcm.!default {\n type file\n slave.pcm { type null }\n file " + strconv.Quote(pcmPath) + "\n format raw\n}\n"
	}
	configPath := filepath.Join(dir, "asound.conf")
	if err := os.WriteFile(configPath, []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ALSA_CONFIG_PATH", configPath)

	wavBytes := wav(Double)
	clip := wavBytes[44:]
	err := play(wavBytes)
	t.Logf("case=%s os=%s arch=%s backend=libasound.so.2 config_sha256=%x play_error=%v",
		mode, runtime.GOOS, runtime.GOARCH, sha256.Sum256([]byte(config)), err)
	if mode == "missing-default" {
		if !errors.Is(err, ErrUnavailable) {
			t.Fatalf("missing default PCM: expected ErrUnavailable, got %v", err)
		}
		if _, err := os.Stat(pcmPath); !os.IsNotExist(err) {
			t.Fatalf("unexpected capture from missing default PCM: %v", err)
		}
		return
	}
	if err != nil {
		t.Fatalf("file PCM backed by null: %v", err)
	}
	actual, err := os.ReadFile(pcmPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(clip) == 0 || bytes.Count(clip, []byte{0}) == len(clip) || !bytes.Equal(actual, clip) {
		t.Fatalf("capture mismatch: expected %d bytes %x, received %d bytes %x",
			len(clip), sha256.Sum256(clip), len(actual), sha256.Sum256(actual))
	}
	t.Logf("sink=ALSA-file-to-null format=mono-S16_LE-44100 bytes=%d expected_pcm_sha256=%x captured_pcm_sha256=%x",
		len(clip), sha256.Sum256(clip), sha256.Sum256(actual))
}
