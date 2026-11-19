package chime

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"
	"testing"
	"time"
)

func TestSynthesis(t *testing.T) {
	seen := map[string]bool{}
	for _, s := range []Sound{Bell, Pluck, Double, Sparkle} {
		t.Run(string(s), func(t *testing.T) {
			data := wav(s)
			u32 := func(i int) int { return int(binary.LittleEndian.Uint32(data[i:])) }
			if string(data[:4]) != "RIFF" || string(data[8:16]) != "WAVEfmt " || string(data[36:40]) != "data" ||
				u32(4) != len(data)-8 || u32(24) != sampleRate || u32(40) != len(data)-44 {
				t.Fatal("bad PCM header")
			}
			n := u32(40) / 2
			if n < sampleRate/3 || n >= sampleRate {
				t.Fatalf("unexpected duration: %d samples", n)
			}
			var energy float64
			peak, last := 0, 0
			for i := range n {
				v := int(int16(binary.LittleEndian.Uint16(data[44+2*i:])))
				peak = max(peak, int(math.Abs(float64(v))))
				energy += float64(v * v)
				if (i < 100 || i >= n-100) && int(math.Abs(float64(v-last))) > 15 {
					t.Fatalf("sharp onset/release at sample %d", i)
				}
				last = v
			}
			rms := math.Sqrt(energy / float64(n))
			if peak < 2400 || peak > 4500 || rms < 900 || rms > 1260 || last != 0 {
				t.Fatalf("peak=%d, RMS=%f, last=%d", peak, rms, last)
			}
			if seen[string(data)] || !bytes.Equal(data, wav(s)) {
				t.Fatal("sounds must be distinct and repeatable")
			}
			seen[string(data)] = true
			t.Logf("%0.3fs, peak %.1f dBFS, RMS %.1f dBFS", float64(n)/sampleRate, 20*math.Log10(float64(peak)/32768), 20*math.Log10(rms/32768))
		})
	}
}

type playback struct {
	audio []byte
	done  chan error
}

func testOutput(t *testing.T) (*Player, <-chan playback) {
	t.Helper()
	calls := make(chan playback, 1)
	return NewPlayer(func(audio []byte) error {
		call := playback{audio, make(chan error, 1)}
		calls <- call
		select {
		case err := <-call.done:
			return err
		case <-time.After(4 * time.Second):
			return fmt.Errorf("fixture never finished playback")
		}
	}), calls
}

func receive(t *testing.T, calls <-chan playback, expected Sound) playback {
	t.Helper()
	select {
	case call := <-calls:
		if !bytes.Equal(call.audio, wav(expected)) {
			call.done <- ErrUnavailable
			t.Fatalf("unexpected audio for %s", expected)
		}
		return call
	case <-time.After(2 * time.Second):
		t.Fatalf("did not play %s", expected)
		return playback{}
	}
}

func awaitIdle(t *testing.T, p *Player) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		p.mu.Lock()
		busy := p.busy
		p.mu.Unlock()
		if !busy {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("playback not idle")
}

func TestPlayerPreviewWhileMutedAndPendingCompletions(t *testing.T) {
	p, calls := testOutput(t)
	defer p.Close()
	p.Configure(false, Bell)
	p.Notify()
	result := make(chan error, 1)
	go func() { result <- p.Preview(Pluck) }()
	start := receive(t, calls, Pluck)
	if err := p.Preview(Bell); err != ErrBusy {
		t.Fatalf("overlapping preview: %v", err)
	}
	p.Configure(true, Pluck)
	p.Notify()
	p.Notify()
	start.done <- nil
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	for range 2 {
		call := receive(t, calls, Pluck)
		call.done <- nil
	}
	awaitIdle(t, p)
}

func TestPlayerMuteSwitchAndOutputFailureClearStaleSounds(t *testing.T) {
	for _, action := range []string{"mute", "switch", "device lost", "close"} {
		t.Run(action, func(t *testing.T) {
			p, calls := testOutput(t)
			defer p.Close()
			p.Configure(true, Bell)
			p.Notify()
			start := receive(t, calls, Bell)
			for range 12 {
				p.Notify()
			}
			p.mu.Lock()
			buffered := len(p.pending)
			p.mu.Unlock()
			if buffered != 3 {
				t.Fatalf("queued %d sounds", buffered)
			}
			var err error
			switch action {
			case "mute":
				p.Configure(false, Bell)
			case "switch":
				p.Configure(true, Sparkle)
			case "device lost":
				err = ErrUnavailable
			case "close":
				p.Close()
			}
			start.done <- err
			awaitIdle(t, p)
			if action == "switch" {
				p.Notify()
				receive(t, calls, Sparkle).done <- nil
				awaitIdle(t, p)
			}
		})
	}
}
