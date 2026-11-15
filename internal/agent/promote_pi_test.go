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
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/idy/yesman/internal/yesmen"
)

type promotionFixture struct {
	m         *Manager
	cfg       Config
	info      Info
	gate      string
	store     *yesmen.Store
	failWrite atomic.Bool
	mu        sync.Mutex
	taken     []string
}

func newPromotionFixture(t *testing.T) *promotionFixture {
	return newPromotionFixtureWithEvents(t, nil)
}
func newPromotionFixtureWithEvents(t *testing.T, onEvent func(Event)) *promotionFixture {
	return newPromotionFixtureWithContext(t, onEvent, nil)
}
func newPromotionFixtureWithContext(t *testing.T, onEvent func(Event), beforeContext func()) *promotionFixture {
	t.Helper()
	runtime := os.Getenv("YESMAN_TEST_RUNTIME")
	if runtime == "" {
		t.Skip("set YESMAN_TEST_RUNTIME to managed v2")
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
			if beforeContext != nil {
				beforeContext()
			}
			io.WriteString(w, "Synthetic promotion fixture")
		} else {
			io.WriteString(w, `{"env":{},"hide":[]}`)
		}
	}))
	t.Cleanup(api.Close)
	f := &promotionFixture{gate: filepath.Join(temp, "gate"), store: yesmen.New(filepath.Join(temp, "workspaces"), filepath.Join(temp, "archive"))}
	if _, err = f.store.Hire("alice"); err != nil {
		t.Fatal(err)
	}
	f.cfg = Config{Pi: node, Command: func(context.Context) (string, []string, error) {
		return node, []string{filepath.Join(runtime, "node_modules", "@earendil-works", "pi-coding-agent", "dist", "cli.js")}, nil
	}, Extension: filepath.Join(root, "pi", "yesman.ts"), Yesmen: f.store,
		Env: append(isolatedNativeEnv(), "HOME="+temp, "USERPROFILE="+temp, "APPDATA="+temp, "LOCALAPPDATA="+temp, "PI_CODING_AGENT_DIR="+config, "YESMAN_API="+api.URL, "DELIVERY_GATE="+f.gate, "DELIVERY_AUDIT="+filepath.Join(temp, "audit"))}
	f.cfg.deliveryWrite = func(path string, data []byte) error {
		if f.failWrite.Load() {
			return errors.New("fixture persistence failure")
		}
		return writeDeliveryFile(path, data)
	}
	f.cfg.OnEvent = func(e Event) {
		if onEvent != nil {
			onEvent(e)
		}
		var wire struct {
			Type    string       `json:"type"`
			Pending pendingQueue `json:"pending"`
		}
		if json.Unmarshal(e.Data, &wire) == nil && wire.Type == "queue_update" {
			f.mu.Lock()
			defer f.mu.Unlock()
			for _, c := range wire.Pending.Changes {
				if c.State == "taken" {
					f.taken = append(f.taken, c.SubmissionID)
				}
			}
		}
	}
	f.m = New(f.cfg)
	t.Cleanup(f.m.StopAll)
	f.info, err = f.m.Get("alice/lead")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err = f.m.Submit(ctx, f.info.ID, deliveryTestID(60000), "hold", nil, f.info.Since); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(temp, "audit")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("provider did not enter hold gate")
		}
		time.Sleep(2 * time.Millisecond)
	}
	f.m.mu.Lock()
	capability := f.m.agents[f.info.ID].nativeQueue.Capability
	f.m.mu.Unlock()
	if capability != 4 {
		t.Fatal("promotion requires the real managed v4 runtime")
	}
	return f
}
func (f *promotionFixture) delivery(t *testing.T, id string, want string) Delivery {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		f.m.mu.Lock()
		a := f.m.agents[f.info.ID]
		d := a.deliveryView(a.deliveries[id])
		f.m.mu.Unlock()
		if want == "" || d.State == want {
			return d
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: want %s, got %+v", id, want, d)
		}
		time.Sleep(2 * time.Millisecond)
	}
}
func TestPromoteWithManagedPi(t *testing.T) {
	f := newPromotionFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	text := "  same original 中文 \n"
	image := Image{Type: "image", MimeType: "image/png", Data: "iVBORw0KGgoAAAANSUhEUgAAABAAAAAQCAYAAAAf8/9hAAAAGUlEQVR4nGMQ7Aj9TwlmGDVg1IBRA4aLAQBbKO0Q8TW4KAAAAABJRU5ErkJggg=="}
	images := []Image{image, image}
	ids := []string{deliveryTestID(60001), deliveryTestID(60002), deliveryTestID(60003), deliveryTestID(60004), deliveryTestID(60005)}
	for n, id := range ids {
		kind := "followUp"
		if n == 0 || n == 4 {
			kind = "steer"
		}
		if _, err := f.m.submit(ctx, f.info.ID, id, text, images, kind, f.info.Since); err != nil {
			t.Fatal(err)
		}
	}
	before := f.delivery(t, ids[2], "queued")
	if !before.CanPromote || !before.CanCancel {
		t.Fatalf("not actionable: %+v", before)
	}
	f.m.mu.Lock()
	handle := f.m.agents[f.info.ID].deliveries[ids[2]].queueID
	f.m.forgetRecallLocked(f.m.agents[f.info.ID], ids[2])
	f.m.mu.Unlock()
	if d := f.delivery(t, ids[2], ""); !d.CanPromote || d.CanCancel {
		t.Fatal("promotion incorrectly depends on retained original payload")
	}
	// Native mutation can succeed despite a failed durable receipt write. Retry
	// reconciles that same occurrence; it never sends the original prompt again.
	f.failWrite.Store(true)
	if _, err := f.m.PromoteSubmission(ctx, f.info.ID, ids[2], f.info.Since); err == nil {
		t.Fatal("persistence failure reported durable success")
	}
	f.failWrite.Store(false)
	p, err := f.m.PromoteSubmission(ctx, f.info.ID, ids[2], f.info.Since)
	if err != nil || p.Outcome != "already_steering" || p.Delivery.State != "steering" || p.Delivery.Order != before.Order || p.Delivery.CanPromote {
		t.Fatalf("retry %+v %v", p, err)
	}
	f.m.mu.Lock()
	now := f.m.agents[f.info.ID].deliveries[ids[2]]
	f.m.mu.Unlock()
	if now.queueID != handle {
		t.Fatal("promotion created a new occurrence")
	}
	if _, err = f.m.Submit(ctx, f.info.ID, ids[2], text, images, f.info.Since); err != nil {
		t.Fatal(err)
	}
	// The caller loses its response after send; ordered queue events still update
	// the same receipt. A later cancel restores original whitespace and image bytes.
	expired, stop := context.WithCancel(context.Background())
	stop()
	if _, err = f.m.PromoteSubmission(expired, f.info.ID, ids[3], f.info.Since); err == nil {
		t.Fatal("expired caller unexpectedly got an ACK")
	}
	f.delivery(t, ids[3], "steering")
	restored, err := f.m.CancelSubmission(ctx, f.info.ID, ids[3], f.info.Since)
	if err != nil || restored.Payload.Text != text || !reflect.DeepEqual(restored.Payload.Images, images) {
		t.Fatalf("promoted payload changed: %v", err)
	}
	if _, err = f.m.PromoteSubmission(ctx, f.info.ID, ids[3], f.info.Since); err == nil {
		t.Fatal("cancelled occurrence promoted")
	}
	if _, err = f.m.PromoteSubmission(ctx, f.info.ID, ids[1], f.info.Since.Add(time.Second)); err == nil {
		t.Fatal("wrong generation promoted")
	}
	if err = os.WriteFile(f.gate, []byte("release"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{ids[0], ids[4], ids[2], ids[1]} {
		f.delivery(t, id, "dispatched")
	}
	deadline := time.Now().Add(3 * time.Second)
	var taken []string
	for {
		f.mu.Lock()
		taken = append([]string(nil), f.taken...)
		f.mu.Unlock()
		if len(taken) == 4 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("native taken events missing: %v", taken)
		}
		time.Sleep(2 * time.Millisecond)
	}
	if !reflect.DeepEqual(taken, []string{ids[0], ids[4], ids[2], ids[1]}) {
		t.Fatalf("duplicate/reordered native delivery: %v", taken)
	}
	if _, err = f.m.PromoteSubmission(ctx, f.info.ID, ids[2], f.info.Since); err == nil {
		t.Fatal("taken occurrence promoted")
	}
	f.m.StopAll()
	restarted := New(f.cfg)
	defer restarted.StopAll()
	for _, id := range ids {
		d, err := restarted.Submit(ctx, f.info.ID, id, text, images, f.info.Since)
		if err != nil || d.CanPromote || d.NativePending {
			t.Fatalf("restart replay %+v %v", d, err)
		}
	}
	if restarted.running(f.info.ID) != nil {
		t.Fatal("old ID replay started pi")
	}
}
func TestPromoteStopAndRestartDoNotResume(t *testing.T) {
	f := newPromotionFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	id := deliveryTestID(61001)
	if _, err := f.m.submit(ctx, f.info.ID, id, "pending", nil, "followUp", f.info.Since); err != nil {
		t.Fatal(err)
	}
	if _, err := f.m.PromoteSubmission(ctx, f.info.ID, id, f.info.Since); err != nil {
		t.Fatal(err)
	}
	f.m.StopAll()
	m := New(f.cfg)
	defer m.StopAll()
	if _, err := m.PromoteSubmission(ctx, f.info.ID, id, f.info.Since); err == nil {
		t.Fatal("restart reused native authority")
	}
	d, err := m.Submit(ctx, f.info.ID, id, "pending", nil, f.info.Since)
	if err != nil || d.State != "interrupted" || m.running(f.info.ID) != nil {
		t.Fatalf("restart replay %+v %v", d, err)
	}
}
