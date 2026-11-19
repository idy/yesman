package chime

import (
	"errors"
	"sync"
	"time"
)

// Four queued plus one playing covers a live burst of five. Keep older
// admitted clips in order and discard newer arrivals once the queue is full.
const (
	maxPending  = 4
	maxAlertAge = 4 * time.Second
)

var (
	ErrUnavailable = errors.New("audio output is unavailable")
	ErrBusy        = errors.New("another sound is playing")
)

// Player owns one output stream at a time. A short queue keeps nearby live
// completions from overlapping. Nothing is queued while disabled, and old
// sounds are never held for the output device (or window) to become ready.
// The zero value uses native audio and starts with notifications off.
type Player struct {
	mu      sync.Mutex
	enabled bool
	sound   Sound
	busy    bool
	closed  bool
	pending []time.Time
	output  func([]byte) error
}

// NewPlayer supplies an isolated sink for fixtures. The sink must return
// after the clip finishes or fails, and may not be called concurrently.
func NewPlayer(output func([]byte) error) *Player { return &Player{output: output} }

func (p *Player) Configure(enabled bool, sound Sound) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if enabled != p.enabled || sound != p.sound {
		p.pending = nil
	}
	p.enabled, p.sound = enabled, sound
}

// Notify is safe to call while committing a Board change. It never waits
// for synthesis or playback. Excess simultaneous sounds expire silently.
func (p *Player) Notify() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || !p.enabled || !p.sound.Valid() {
		return
	}
	if p.busy {
		now := time.Now()
		p.expire(now)
		if len(p.pending) < maxPending {
			p.pending = append(p.pending, now.Add(maxAlertAge))
		}
		return
	}
	p.busy = true
	go p.run(p.sound, nil)
}

// Preview explicitly plays one choice, even if automatic sounds are off.
func (p *Player) Preview(sound Sound) error {
	if !sound.Valid() {
		return ErrUnavailable
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return ErrUnavailable
	}
	if p.busy {
		p.mu.Unlock()
		return ErrBusy
	}
	p.busy = true
	p.mu.Unlock()
	result := make(chan error, 1)
	go p.run(sound, result)
	select {
	case err := <-result:
		return err
	case <-time.After(3 * time.Second):
		return ErrUnavailable
	}
}

func (p *Player) Close() {
	p.mu.Lock()
	p.closed = true
	p.pending = nil
	p.mu.Unlock()
}

func (p *Player) run(sound Sound, preview chan error) {
	output := p.output
	if output == nil {
		output = play
	}
	for {
		err := output(wav(sound))
		p.mu.Lock()
		if preview != nil {
			preview <- err
			preview = nil
		}
		if err != nil {
			p.pending = nil // do not retry on an unavailable output
		}
		p.expire(time.Now())
		if len(p.pending) == 0 {
			p.busy = false
			p.pending = nil
			p.mu.Unlock()
			return
		}
		p.pending = p.pending[1:]
		sound = p.sound
		p.mu.Unlock()
	}
}

// expire runs under mu both before admitting a new alert and before
// taking the next one to the output. Nothing waits for a device recovery.
func (p *Player) expire(now time.Time) {
	for len(p.pending) > 0 && !now.Before(p.pending[0]) {
		p.pending = p.pending[1:]
	}
}
