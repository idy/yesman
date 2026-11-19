package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestHumanDraftStageRecovery(t *testing.T) {
	f := newPromotionFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	y, err := f.store.Get("alice")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(y.SessionDir("lead"), "delivery.json")
	id := deliveryTestID(97201)
	text := "synthetic pending original"
	if d, err := f.m.Submit(ctx, f.info.ID, id, text, nil, f.info.Since); err != nil || !d.CanCancel {
		t.Fatal(d, err)
	}
	f.m.StopAll()
	stage := filepath.Join(filepath.Dir(path), humanDraftStage)
	nearby := filepath.Join(filepath.Dir(path), humanDraftStage+".notes")
	for file, contents := range map[string]string{stage: `{"incomplete":`, nearby: "unrelated file"} {
		if err := os.WriteFile(file, []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
	}
	restarted := New(f.cfg)
	defer restarted.StopAll()
	d, err := restarted.Submit(ctx, f.info.ID, id, text, nil, f.info.Since)
	if err != nil || d.State != "interrupted" || !d.CanRecover || restarted.running(f.info.ID) != nil {
		t.Fatal(d, err)
	}
	payload, err := restarted.SubmissionPayload(f.info.ID, id, f.info.Since)
	if err != nil || payload.Text != text {
		t.Fatal(payload, err)
	}
	if err := restarted.AckRecall(f.info.ID, id, f.info.Since); err != nil {
		t.Fatal(err)
	}
	restarted.StopAll()
	if _, err := os.Stat(stage); !os.IsNotExist(err) {
		t.Fatalf("stage survived recovery: %v", err)
	}
	if data, err := os.ReadFile(nearby); err != nil || string(data) != "unrelated file" {
		t.Fatal(string(data), err)
	}
	if data, err := readHumanDrafts(path); err != nil || len(data) != 0 {
		t.Fatal(data, err)
	}
}

func TestHumanDraftCorruptionPreservesSnapshot(t *testing.T) {
	f := newPromotionFixture(t)
	f.m.StopAll()
	y, err := f.store.Get("alice")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(y.SessionDir("lead"), humanDraftFile)
	broken := `{"interrupted":`
	if err := os.WriteFile(path, []byte(broken), 0600); err != nil {
		t.Fatal(err)
	}
	restarted := New(f.cfg)
	defer restarted.StopAll()
	for i := 0; i < 2; i++ {
		if _, err := restarted.Submit(context.Background(), f.info.ID, deliveryTestID(97301+i), "synthetic attempt", nil, f.info.Since); err == nil || restarted.running(f.info.ID) != nil {
			t.Fatalf("corrupt snapshot admitted a prompt: %v", err)
		}
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != broken {
		t.Fatal(string(data), err)
	}
}

func TestHumanDraftAcknowledgementSurvivesWriteFailure(t *testing.T) {
	f := newPromotionFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	skill, refused := deliveryTestID(97401), deliveryTestID(97402)
	if d, err := f.m.Submit(ctx, f.info.ID, skill, "/skill:fixture-steer example", nil, f.info.Since); err != nil || !d.CanCancel {
		t.Fatal(d, err)
	}
	original := testPNG(2, 3, 4)
	text := "synthetic unchanged retry"
	if d, err := f.m.Submit(ctx, f.info.ID, refused, text, []Image{original}, f.info.Since); err != nil || d.State != "failed" || !d.CanRecover {
		t.Fatal(d, err)
	}
	if _, err := f.m.CancelSubmission(ctx, f.info.ID, skill, f.info.Since); err != nil {
		t.Fatal(err)
	}
	if err := f.m.AckRecall(f.info.ID, skill, f.info.Since); err != nil {
		t.Fatal(err)
	}
	retry := deliveryTestID(97403)
	if d, err := f.m.Submit(ctx, f.info.ID, retry, text, []Image{original}, f.info.Since); err != nil || !d.CanCancel {
		t.Fatal(d, err)
	}
	f.failWrite.Store(true)
	if err := f.m.AckRecall(f.info.ID, refused, f.info.Since); err == nil {
		t.Fatal("reported durable release on a failed write")
	}
	if p, err := f.m.SubmissionPayload(f.info.ID, refused, f.info.Since); err != nil || p.Text != text || len(p.Images) != 1 || p.Images[0] != original {
		t.Fatal(p, err)
	}
	f.failWrite.Store(false)
	if err := f.m.AckRecall(f.info.ID, refused, f.info.Since); err != nil {
		t.Fatal(err)
	}
	y, err := f.store.Get("alice")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(y.SessionDir("lead"), "delivery.json")
	if drafts, err := readHumanDrafts(path); err != nil || drafts[refused].Text != "" || drafts[retry].Text != text {
		t.Fatal(drafts, err)
	}
	if _, err := f.m.SubmissionPayload(f.info.ID, refused, f.info.Since); err == nil {
		t.Fatal("acknowledged original is still served")
	}
	f.release(t)
	calls, _ := f.awaitProvider(t, text)
	if got := introduced(calls, text); len(got) != 1 {
		t.Fatalf("retry delivered %d times", len(got))
	}
}
