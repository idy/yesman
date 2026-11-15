package agent

import (
	"encoding/json"
	"testing"
)

func TestNativeProjectionDoesNotGuessFromLegacyTracker(t *testing.T) {
	tracker := originTracker{native: true, started: &Origin{Source: OriginYesman, Kind: "report", Worker: "old"}}
	raw := json.RawMessage(`{"type":"message_start","message":{"role":"user","timestamp":1,"content":"same","yesman":{"source":"yesman","submission":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}}`)
	projected := tracker.observe("message_start", raw)
	var event struct {
		Message struct {
			Origin *Origin `json:"yesman"`
		} `json:"message"`
	}
	if err := json.Unmarshal(projected, &event); err != nil {
		t.Fatal(err)
	}
	if event.Message.Origin != nil {
		t.Fatal("native queue with missing host evidence used old guessed source")
	}
	history := projectNativeMessages(json.RawMessage(`[{"role":"user","content":"same","yesman":{"source":"yesman"}}]`), nil)
	var msgs []struct {
		Origin *Origin `json:"yesman"`
	}
	if err := json.Unmarshal(history, &msgs); err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 || msgs[0].Origin != nil {
		t.Fatal("missing host history evidence retained claimed source")
	}
}
