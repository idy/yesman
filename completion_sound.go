package main

import (
	"errors"

	"github.com/idy/yesman/internal/chime"
)

type CompletionSoundPreference struct {
	Enabled bool        `json:"enabled"`
	Sound   chime.Sound `json:"sound"`
}

func (p CompletionSoundPreference) effective() CompletionSoundPreference {
	if !p.Sound.Valid() {
		p.Sound = chime.Bell
	}
	return p
}

func (s *service) SetCompletionSound(pref CompletionSoundPreference) error {
	if !pref.Sound.Valid() {
		return errors.New("unknown completion sound")
	}
	a := s.app
	a.setmu.Lock()
	defer a.setmu.Unlock()
	next := a.settings
	next.CompletionSound = pref
	return a.putSettingsLocked(next)
}

// PreviewCompletionSound is a desktop bridge action, not an agent API.
// Return a small result code so the Settings page can localize its feedback.
func (s *service) PreviewCompletionSound(sound chime.Sound) string {
	if err := s.app.chimes.Preview(sound); err != nil {
		if errors.Is(err, chime.ErrBusy) {
			return "busy"
		}
		return "unavailable"
	}
	return ""
}
