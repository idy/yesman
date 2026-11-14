package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/idy/yesman/internal/yesmen"
)

// Uses a genuinely prepared source-patched runtime. Opt in, with no credentials
// or real model: YESMAN_TEST_RUNTIME=/path/to/runtime go test -race ...
func TestRecallWithManagedPi(t *testing.T) {
	runtime := os.Getenv("YESMAN_TEST_RUNTIME")
	if runtime == "" {
		t.Skip("set YESMAN_TEST_RUNTIME to the prepared pinned runtime")
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal(err)
	}
	temp := t.TempDir()
	config := filepath.Join(temp, "config")
	if err = os.MkdirAll(config, 0700); err != nil {
		t.Fatal(err)
	}
	settings := map[string]any{"packages": []string{}, "extensions": []string{filepath.Join(root, "scripts", "fixtures", "delivery-provider.ts")}, "skills": []string{}, "promptTemplates": []string{}, "themes": []string{}, "defaultProjectTrust": "no", "defaultProvider": "delivery-test", "defaultModel": "m", "compaction": map[string]bool{"enabled": false}, "retry": map[string]bool{"enabled": false}}
	data, _ := json.Marshal(settings)
	if err = os.WriteFile(filepath.Join(config, "settings.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/context" {
			io.WriteString(w, "Isolated managed pi fixture")
		} else {
			io.WriteString(w, `{"env":{},"hide":[]}`)
		}
	}))
	defer api.Close()
	store := yesmen.New(filepath.Join(temp, "workspaces"), filepath.Join(temp, "archive"))
	if _, err = store.Hire("alice"); err != nil {
		t.Fatal(err)
	}
	gate := filepath.Join(temp, "gate")
	cfg := Config{Pi: node, Command: func(context.Context) (string, []string, error) {
		return node, []string{filepath.Join(runtime, "node_modules", "@earendil-works", "pi-coding-agent", "dist", "cli.js")}, nil
	}, Extension: filepath.Join(root, "pi", "yesman.ts"), Yesmen: store,
		Env: append(isolatedNativeEnv(), []string{"HOME=" + temp, "USERPROFILE=" + temp, "APPDATA=" + temp, "LOCALAPPDATA=" + temp, "PI_CODING_AGENT_DIR=" + config, "YESMAN_API=" + api.URL, "DELIVERY_GATE=" + gate, "DELIVERY_AUDIT=" + filepath.Join(temp, "audit")}...)}
	var failWrite atomic.Bool
	cfg.deliveryWrite = func(path string, data []byte) error {
		if failWrite.Load() {
			return errors.New("fixture disk failure")
		}
		return writeDeliveryFile(path, data)
	}
	m := New(cfg)
	defer m.StopAll()
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	info, err := m.Get("alice/lead")
	if err != nil {
		t.Fatal(err)
	}
	initial, err := m.Submit(ctx, info.ID, deliveryTestID(10000), "hold", nil, info.Since)
	if err != nil || initial.State != "accepted" {
		// Preserve complete diagnostics only from this synthetic configuration,
		// not from any user's pi, for platform startup failures.
		probe := exec.CommandContext(ctx, node, filepath.Join(runtime, "node_modules", "@earendil-works", "pi-coding-agent", "dist", "cli.js"), "--mode", "rpc", "--no-session", "-e", cfg.Extension)
		probe.Dir = temp
		probe.Env = append(os.Environ(), cfg.Env...)
		probe.Env = append(probe.Env, "YESMAN_ROLE=lead", "YESMAN_AGENT=alice/lead", "YESMAN_TOKEN=fixture")
		probe.Stdin = strings.NewReader("{\"type\":\"get_state\",\"id\":\"diagnostic\"}\n")
		output, probeErr := probe.CombinedOutput()
		t.Fatalf("initial prompt state=%s err=%v detail=%s; isolated probe %v: %s", initial.State, err, initial.Error, probeErr, output)
	}
	image := Image{Type: "image", MimeType: "image/png", Data: "iVBORw0KGgoAAAANSUhEUgAAABAAAAAQCAYAAAAf8/9hAAAAGUlEQVR4nGMQ7Aj9TwlmGDVg1IBRA4aLAQBbKO0Q8TW4KAAAAABJRU5ErkJggg=="}
	text := "original " + strings.Repeat("complete text 中文\n", 100)
	ids := []string{deliveryTestID(10001), deliveryTestID(10002), deliveryTestID(10003), deliveryTestID(10004), deliveryTestID(10005), deliveryTestID(10006)}
	for n, id := range ids {
		behavior := "steer"
		if n >= 3 {
			behavior = "followUp"
		}
		d, err := m.submit(ctx, info.ID, id, text, []Image{image, image}, behavior, info.Since)
		if err != nil {
			t.Fatal(err)
		}
		if d.State != "steering" && d.State != "queued" {
			t.Fatalf("not queued: %+v", d)
		}
	}
	snapshot, err := m.Messages(ctx, info.ID)
	if err != nil {
		t.Fatal(err)
	}
	cancelable := 0
	for _, d := range snapshot.Deliveries {
		if d.CanCancel {
			cancelable++
		}
	}
	if cancelable != 6 {
		t.Fatalf("cancelable=%d: %+v", cancelable, snapshot.Deliveries)
	}
	failWrite.Store(true)
	if _, err = m.CancelSubmission(ctx, info.ID, ids[1], info.Since); err == nil {
		t.Fatal("failed cancellation persistence reported restore success")
	}
	failWrite.Store(false)
	first, err := m.CancelSubmission(ctx, info.ID, ids[1], info.Since)
	if err != nil {
		t.Fatal(err)
	}
	if first.Payload.Text != text || len(first.Payload.Images) != 2 || first.Payload.Images[1] != image {
		t.Fatalf("original payload not restored")
	}
	if first.Delivery.State != "cancelled" || first.Delivery.CanCancel {
		t.Fatalf("cancelled state: %+v", first.Delivery)
	}
	duplicate, err := m.CancelSubmission(ctx, info.ID, ids[1], info.Since)
	if err != nil || duplicate.Payload.Text != text {
		t.Fatalf("cancel retry: %+v %v", duplicate, err)
	}
	replay, err := m.Submit(ctx, info.ID, ids[1], text, []Image{image, image}, info.Since)
	if err != nil || replay.State != "cancelled" {
		t.Fatalf("old intent replay: %+v %v", replay, err)
	}
	if _, err = m.CancelSubmission(ctx, info.ID, ids[4], info.Since); err != nil {
		t.Fatal(err)
	}
	// Editing is a fresh intent and sends only once even after an unchanged retry.
	editedID := deliveryTestID(10007)
	if _, err = m.Submit(ctx, info.ID, editedID, "edited", []Image{image}, info.Since); err != nil {
		t.Fatal(err)
	}
	if _, err = m.Submit(ctx, info.ID, editedID, "edited", []Image{image}, info.Since); err != nil {
		t.Fatal(err)
	}
	if err = m.AckRecall(info.ID, ids[1], info.Since); err != nil {
		t.Fatal(err)
	}
	if _, err = m.CancelSubmission(ctx, info.ID, ids[1], info.Since); err == nil {
		t.Fatal("restored payload still retained after acknowledgement")
	}
	if _, err = m.CancelSubmission(ctx, info.ID, ids[0], info.Since.Add(time.Second)); err == nil {
		t.Fatal("wrong generation accepted")
	}
	if err = os.WriteFile(gate, []byte("release"), 0600); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		i, _ := m.Get(info.ID)
		if i.Status == Idle {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("pi did not settle")
		}
		time.Sleep(5 * time.Millisecond)
	}
	snapshot, err = m.Messages(ctx, info.ID)
	if err != nil {
		t.Fatal(err)
	}
	var messages []struct {
		Role    string `json:"role"`
		Content json.RawMessage
	}
	if err = json.Unmarshal(snapshot.Messages, &messages); err != nil {
		t.Fatal(err)
	}
	var texts []string
	for _, msg := range messages {
		if msg.Role == "user" {
			var content []struct{ Type, Text string }
			if err := json.Unmarshal(msg.Content, &content); err != nil {
				t.Fatal(err)
			}
			for _, b := range content {
				if b.Type == "text" {
					texts = append(texts, b.Text)
				}
			}
		}
	}
	if len(texts) != 6 || strings.Join(texts, "|") != "hold|"+text+"|"+text+"|edited|"+text+"|"+text {
		t.Fatalf("unexpected survivor order/count: %d", len(texts))
	}
	if _, err = m.CancelSubmission(ctx, info.ID, ids[0], info.Since); err == nil {
		t.Fatal("already taken input recalled")
	}
	m.StopAll()
	// Whole Manager restart retains the cancellation tombstone, never full data.
	restored := New(cfg)
	defer restored.StopAll()
	replay, err = restored.Submit(ctx, info.ID, ids[1], text, []Image{image, image}, info.Since)
	if err != nil || replay.State != "cancelled" || replay.CanCancel || replay.CanRestore || restored.running(info.ID) != nil {
		t.Fatalf("restart replay: %+v %v", replay, err)
	}
	disk, err := os.ReadFile(filepath.Join(storePath(t, store, "alice"), ".yesman", "sessions", "lead", "delivery.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(disk), image.Data) || strings.Contains(string(disk), text) {
		t.Fatal("full payload persisted")
	}
	// Archive seals the old writer before a same-name generation is hired.
	old, err := store.Get("alice")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Archive("alice", func(yesmen.Yesman) error { return restored.StopYesman("alice") }); err != nil {
		t.Fatal(err)
	}
	restored.ForgetYesman("alice", old.HiredAt)
	if _, err = store.Hire("alice"); err != nil {
		t.Fatal(err)
	}
	if _, err = restored.CancelSubmission(ctx, info.ID, ids[1], info.Since); err == nil {
		t.Fatal("old generation recalled into new workspace")
	}
	if restored.running(info.ID) != nil {
		t.Fatal("stale cancellation started replacement pi")
	}
}

func isolatedNativeEnv() []string {
	var clear []string
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		switch strings.ToUpper(key) {
		case "PATH", "PATHEXT", "SYSTEMROOT", "SYSTEMDRIVE", "WINDIR", "COMSPEC", "TMPDIR", "TEMP", "TMP":
		default:
			clear = append(clear, key+"=")
		}
	}
	return clear
}

func storePath(t *testing.T, store *yesmen.Store, name string) string {
	t.Helper()
	y, err := store.Get(name)
	if err != nil {
		t.Fatal(err)
	}
	return y.Path
}
