package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/idy/yesman/internal/yesmen"
)

func testImage() Image {
	return Image{Type: "image", MimeType: "image/png", Data: "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+jRZkAAAAASUVORK5CYII="}
}

func TestValidateImages(t *testing.T) {
	valid := testImage()
	large := valid
	large.Data = base64.StdEncoding.EncodeToString(append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, maxImageBytes-8)...))
	badType, badMIME, badData, disguised := valid, valid, valid, valid
	badType.Type = "text"
	badMIME.MimeType = "image/svg+xml"
	badData.Data = "not base64!"
	disguised.Data = base64.StdEncoding.EncodeToString([]byte("<html>not an image</html>"))
	for _, tt := range []struct {
		name   string
		images []Image
		want   string
	}{
		{"none", nil, ""},
		{"png", []Image{valid}, ""},
		{"count boundary", []Image{valid, valid, valid, valid}, ""},
		{"count", []Image{valid, valid, valid, valid, valid}, "at most"},
		{"type", []Image{badType}, "not an image"},
		{"mime", []Image{badMIME}, "use PNG"},
		{"base64", []Image{badData}, "invalid base64"},
		{"empty", []Image{{Type: "image", MimeType: "image/png"}}, "invalid base64"},
		{"disguised", []Image{disguised}, "declared format"},
		{"size boundary", []Image{large, large}, ""},
		{"total", []Image{large, large, valid}, "10 MB"},
		{"oversize", []Image{{Type: "image", MimeType: "image/png", Data: large.Data + "AAAA"}}, "5 MB"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := validateImages(tt.images)
			if tt.want == "" && err != nil || tt.want != "" && (err == nil || !strings.Contains(err.Error(), tt.want)) {
				t.Fatalf("validateImages = %v, want %q", err, tt.want)
			}
		})
	}
}

// Capture what crosses the child-process boundary, rather than testing only
// serialization in isolation. Both image-only and text+image prompts use steer.
func TestPromptImagesRPC(t *testing.T) {
	m, _ := newTestManager(t, 0)
	type received struct {
		Type     string  `json:"type"`
		Message  string  `json:"message"`
		Images   []Image `json:"images"`
		Behavior string  `json:"streamingBehavior"`
	}
	events := make(chan received, 10)
	m.cfg.OnEvent = func(e Event) {
		var r received
		if json.Unmarshal(e.Data, &r) == nil && r.Type == "test_prompt_received" {
			events <- r
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	id := ID("alice", yesmen.LeadName)
	images := []Image{testImage()}
	for _, text := range []string{"Describe this", ""} {
		if err := m.PromptImages(ctx, id, text, images); err != nil {
			t.Fatal(err)
		}
		select {
		case got := <-events:
			if got.Message != text || got.Behavior != "steer" || !reflect.DeepEqual(got.Images, images) {
				t.Fatalf("RPC prompt lost content: %+v", got)
			}
		case <-ctx.Done():
			t.Fatal("no prompt received")
		}
	}
	if err := m.Prompt(ctx, id, "plain text"); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-events:
		if got.Message != "plain text" || len(got.Images) != 0 {
			t.Fatalf("plain prompt = %+v", got)
		}
	case <-ctx.Done():
		t.Fatal("no text prompt received")
	}
}

func TestInvalidImagesDoNotStartPi(t *testing.T) {
	m, _ := newTestManager(t, 0)
	id := ID("alice", yesmen.LeadName)
	if err := m.PromptImages(context.Background(), id, "hi", []Image{{Type: "image", MimeType: "image/png", Data: "bad"}}); err == nil {
		t.Fatal("invalid attachment accepted")
	}
	if m.running(id) != nil {
		t.Fatal("invalid attachment started pi")
	}
	if err := m.PromptImages(context.Background(), id, " ", nil); err == nil {
		t.Fatal("empty prompt accepted")
	}
}
