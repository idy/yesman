// Package chime synthesizes and plays the app's short completion sounds.
package chime

import (
	"encoding/binary"
	"math"
)

type Sound string

const (
	Bell    Sound = "bell"
	Pluck   Sound = "pluck"
	Double  Sound = "double"
	Sparkle Sound = "sparkle"

	sampleRate = 44100
)

func (s Sound) Valid() bool {
	switch s {
	case Bell, Pluck, Double, Sparkle:
		return true
	}
	return false
}

type note struct {
	start, length, hz, decay, gain float64
	partials                       []partial
}

type partial struct{ ratio, gain float64 }

// wav builds mono 16-bit PCM from four original, short phrases. The envelope
// eases both ends to zero. Each mix is peak-limited to -17 dBFS or below.
func wav(sound Sound) []byte {
	var notes []note
	switch sound {
	case Bell:
		notes = []note{
			{0, .59, 659.25, 4.5, 1, []partial{{1, 1}, {2.01, .23}, {3.02, .09}}},
			{.11, .52, 987.77, 4.5, .47, []partial{{1, 1}, {2.01, .15}}},
		}
	case Pluck:
		notes = []note{
			{0, .32, 392, 6.2, .85, []partial{{1, 1}, {2, .40}, {3, .16}, {4, .07}}},
			{.09, .36, 523.25, 6.2, 1, []partial{{1, 1}, {2, .30}, {3, .14}}},
			{.19, .43, 659.25, 5.5, .75, []partial{{1, 1}, {2, .25}, {3, .10}}},
		}
	case Double:
		notes = []note{
			{0, .19, 587.33, 1.5, .8, []partial{{1, 1}}},
			{.23, .28, 783.99, 2.2, 1, []partial{{1, 1}}},
		}
	case Sparkle:
		notes = []note{
			{0, .17, 783.99, 5, .85, []partial{{1, 1}, {2, .31}}},
			{.075, .17, 1046.50, 5, .74, []partial{{1, 1}, {2, .26}}},
			{.15, .22, 1318.51, 5, .67, []partial{{1, 1}, {2, .23}}},
			{.23, .35, 1567.98, 5, .63, []partial{{1, 1}, {2, .18}}},
		}
	default:
		return nil
	}
	end := notes[len(notes)-1].start + notes[len(notes)-1].length
	mix := make([]float64, int(math.Ceil(end*sampleRate))+1)
	for _, n := range notes {
		start := int(math.Round(n.start * sampleRate))
		for i := start; i < len(mix); i++ {
			t := float64(i-start) / sampleRate
			if t >= n.length {
				break
			}
			envelope := math.Exp(-n.decay*t) * ease(t/.022) * ease((n.length-t)/.085)
			var wave float64
			for _, p := range n.partials {
				wave += p.gain * math.Sin(2*math.Pi*n.hz*p.ratio*t)
			}
			mix[i] += n.gain * envelope * wave
		}
	}
	var peak, energy float64
	for _, v := range mix {
		peak = max(peak, math.Abs(v))
		energy += v * v
	}
	// The sustained double tone is quieter at the peak so that its
	// average level is in the same range as the more percussive clips.
	scale := min(4500/peak, 1250/math.Sqrt(energy/float64(len(mix))))
	audio := make([]byte, 44+2*len(mix))
	copy(audio, "RIFF\x00\x00\x00\x00WAVEfmt \x10\x00\x00\x00\x01\x00\x01\x00")
	binary.LittleEndian.PutUint32(audio[4:], uint32(len(audio)-8))
	binary.LittleEndian.PutUint32(audio[24:], sampleRate)
	binary.LittleEndian.PutUint32(audio[28:], sampleRate*2)
	copy(audio[32:], "\x02\x00\x10\x00data")
	binary.LittleEndian.PutUint32(audio[40:], uint32(len(mix)*2))
	for i, v := range mix {
		binary.LittleEndian.PutUint16(audio[44+2*i:], uint16(int16(math.Round(v*scale))))
	}
	return audio
}

func ease(x float64) float64 {
	x = min(max(0, x), 1)
	return x * x * (3 - 2*x)
}
