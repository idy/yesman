package yesmen

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	ResponseText       = "text"
	ResponseChoice     = "choice"
	ResponseAction     = "action"
	MaxResponseText    = 2000 // Unicode code points, shared with Pi text replies
	maxResponseOptions = 8
	maxOptionLength    = 120
)

// HandoffResponse is the lead's explicit description of what counts as a
// reply. It contains data only. Action cards acknowledge work done by the
// user; they never run commands.
type HandoffResponse struct {
	Kind      string   `json:"kind"`
	Options   []string `json:"options,omitempty"`
	AllowNote bool     `json:"allowNote,omitempty"`
}

// HandoffAnswer is the human response, stored with the closed request. A
// choice is the exact option from HandoffResponse, not free-form text.
type HandoffAnswer struct {
	Text   string `json:"text,omitempty"`
	Choice string `json:"choice,omitempty"`
	Note   string `json:"note,omitempty"`
}

func (r HandoffResponse) valid(handoffType string) error {
	if (handoffType == HandoffLogin || handoffType == HandoffInstall || handoffType == HandoffManual) && r.Kind != ResponseAction {
		return errors.New("login, install and manual requests can only ask for action confirmation")
	}
	switch r.Kind {
	case ResponseText, ResponseAction:
		if len(r.Options) != 0 || r.AllowNote {
			return errors.New("options and optional notes are only for choice requests")
		}
	case ResponseChoice:
		if len(r.Options) < 2 || len(r.Options) > maxResponseOptions {
			return fmt.Errorf("give between 2 and %d choices", maxResponseOptions)
		}
		seen := map[string]bool{}
		for _, option := range r.Options {
			key := strings.ToLower(option)
			if option == "" || oneLine(option) != option || !validResponseText(option, maxOptionLength) || seen[key] {
				return fmt.Errorf("each choice must be a distinct single line of up to %d characters", maxOptionLength)
			}
			seen[key] = true
		}
	default:
		return errors.New("response kind must be text, choice or action")
	}
	return nil
}

func validResponseText(s string, maxRunes int) bool {
	return utf8.ValidString(s) && utf8.RuneCountInString(s) <= maxRunes && strings.IndexFunc(s, func(r rune) bool {
		return unicode.IsControl(r) && r != '\t' && r != '\r' && r != '\n'
	}) == -1
}

// ValidResponseText checks the common bound and encoding. A dialogue's own
// semantics decide whether an empty value is acceptable.
func ValidResponseText(s string) bool { return validResponseText(s, MaxResponseText) }

func (h Handoff) checkAnswer(resolution string, answer *HandoffAnswer) error {
	if resolution != HandoffDone {
		if answer != nil {
			return errors.New("closing without an answer cannot include response data")
		}
		return nil
	}
	r := h.Response
	if r == nil {
		// Old cards can still be closed via their original API. The current
		// UI also allows an inline reply to an old input or decision card.
		if answer == nil {
			return nil
		}
		if h.Type != HandoffInput && h.Type != HandoffDecision {
			return errors.New("this request asks for action confirmation")
		}
		r = &HandoffResponse{Kind: ResponseText}
	}
	if answer != nil && (!ValidResponseText(answer.Text) || !ValidResponseText(answer.Note)) {
		return fmt.Errorf("a reply or note must be valid text of at most %d characters", MaxResponseText)
	}
	switch r.Kind {
	case ResponseText:
		if answer == nil || strings.TrimSpace(answer.Text) == "" || answer.Choice != "" || answer.Note != "" {
			return errors.New("this request needs a text reply")
		}
	case ResponseChoice:
		if answer == nil || !slices.Contains(r.Options, answer.Choice) || answer.Text != "" || (!r.AllowNote && answer.Note != "") {
			return errors.New("choose one of the offered options and give a note only if allowed")
		}
	case ResponseAction:
		if answer != nil {
			return errors.New("this request asks for action confirmation only")
		}
	}
	return nil
}
