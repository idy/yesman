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

type CompletionSoundState struct {
	CompletionSoundPreference
	Revision uint64 `json:"revision"`
}

// CompletionSoundPatch changes one preference. A false switch is an edit,
// even when a newer request has saved a different sound in the meantime.
type CompletionSoundPatch struct {
	Enabled *bool        `json:"enabled,omitempty"`
	Sound   *chime.Sound `json:"sound,omitempty"`
}

func (a *app) completionSoundLocked() CompletionSoundState {
	return CompletionSoundState{a.settings.CompletionSound.effective(), a.soundRevision}
}

func (s *service) SetCompletionSound(patch CompletionSoundPatch) (CompletionSoundState, error) {
	if (patch.Enabled == nil) == (patch.Sound == nil) {
		return CompletionSoundState{}, errors.New("change either the completion alert or its sound")
	}
	if patch.Sound != nil && !patch.Sound.Valid() {
		return CompletionSoundState{}, errors.New("unknown completion sound")
	}
	a := s.app
	a.setmu.Lock()
	defer a.setmu.Unlock()
	next := a.settings
	if patch.Enabled != nil {
		next.CompletionSound.Enabled = *patch.Enabled
	} else {
		next.CompletionSound.Sound = *patch.Sound
	}
	if err := a.putSettingsLocked(next); err != nil {
		return CompletionSoundState{}, err
	}
	return a.completionSoundLocked(), nil
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
