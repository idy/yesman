package agent

import (
	"context"
	"github.com/idy/yesman/internal/yesmen"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReviewR5StopBeforeHireOwnershipIO(t *testing.T) {
	m, s := newTestManager(t, 0)
	m.StopByUser("alice/lead")
	if _, err := s.Archive("alice", func(y yesmen.Yesman) error { return m.StopYesman(y.Name) }); err != nil {
		t.Fatal(err)
	}
	// A new hire can exist before service's delayed ForgetYesman callback.
	// As with opening its conversation, ensure starts it without sending input.
	if _, err := s.Hire("alice"); err != nil {
		t.Fatal(err)
	}
	_, p, err := m.ensure("alice/lead")
	if err != nil {
		t.Fatal(err)
	}
	entered, release, archiveDone := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		_, err := s.Archive("alice", func(y yesmen.Yesman) error { close(entered); <-release; return m.StopYesman(y.Name) })
		archiveDone <- err
	}()
	<-entered
	stopped := make(chan struct{})
	go func() { m.StopByUser("alice/lead"); close(stopped) }()
	select {
	case <-p.done:
	case <-time.After(250 * time.Millisecond):
		t.Error("current lead Stop waited for ownership Store.Use before cancelling its process")
	}
	close(release)
	select {
	case <-archiveDone:
	case <-time.After(3 * time.Second):
		t.Fatal("archive did not join")
	}
	select {
	case <-stopped:
	case <-time.After(3 * time.Second):
		t.Fatal("Stop did not join")
	}
}

func TestReviewR5RegistrationRefusalKeepsPause(t *testing.T) {
	m, s := newTestManager(t, 0)
	if err := s.AddWorker("alice", yesmen.Worker{Name: "w1", StartedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.ensure("alice/w1"); err != nil {
		t.Fatal(err)
	}
	m.StopByUser("alice/w1")
	first := reviewR4WorkerRecord(t, s, "w1").PausedAt
	if err := m.Prompt(context.Background(), "alice/w1", ""); err == nil {
		t.Fatal("empty prompt accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := m.Prompt(ctx, "alice/w1", "not registered"); err == nil {
		t.Fatal("cancelled prompt accepted")
	}
	if got := reviewR4WorkerRecord(t, s, "w1").PausedAt; !got.Equal(first) {
		t.Error("registration refusal cleared durable pause")
	}
	m.continueTasks("alice")
	reviewR5Drain(t, m)
	if got := reviewR4WorkerRecord(t, s, "w1").PausedAt; !got.Equal(first) {
		t.Error("retry lifted pause for unregistered input")
	}
}

func TestReviewR5LateReopenAndResumeReplacement(t *testing.T) {
	m, s := newTestManager(t, 0)
	prompts := promptsTo(m)
	if err := s.AddWorker("alice", yesmen.Worker{Name: "w1", StartedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.ensure("alice/w1"); err != nil {
		t.Fatal(err)
	}
	m.StopByUser("alice/w1")
	entered, release := make(chan struct{}), make(chan struct{})
	m.reviewBeforeReopen = func() { close(entered); <-release }
	done := make(chan error, 1)
	go func() { done <- m.Prompt(context.Background(), "alice/w1", "registered before later Stop") }()
	<-entered
	m.StopByUser("alice/w1")
	later := reviewR4WorkerRecord(t, s, "w1").PausedAt
	close(release)
	<-done
	if got := reviewR4WorkerRecord(t, s, "w1").PausedAt; got.IsZero() || !got.Equal(later) {
		t.Errorf("late reopen erased later Stop: %v vs %v", got, later)
	}
	m.continueTasks("alice")
	reviewR5Drain(t, m)
	if got := reviewR4WorkerRecord(t, s, "w1").PausedAt; !got.Equal(later) {
		t.Error("retry cleared later Stop")
	}
	if len(prompts("alice/w1")) != 0 {
		t.Errorf("stopped registered message was sent: %q", prompts("alice/w1"))
	}
}

func TestReviewR5ResumeAfterWorkerRemoveAndRehire(t *testing.T) {
	m, s := newTestManager(t, 0)
	prompts := promptsTo(m)
	y, _ := s.Get("alice")
	if err := s.AddWorker("alice", yesmen.Worker{Name: "w1", StartedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.ensure("alice/w1"); err != nil {
		t.Fatal(err)
	}
	m.StopByUser("alice/w1")
	fail := filepath.Join(y.MetaDir(), "workers.json.tmp")
	if err := os.Mkdir(fail, 0700); err != nil {
		t.Fatal(err)
	}
	if err := m.Prompt(context.Background(), "alice/w1", "explicit new work"); err != nil {
		t.Fatal(err)
	}
	waitCount(t, prompts, "alice/lead", 1)
	reviewR5Drain(t, m)
	if err := os.Remove(fail); err != nil {
		t.Fatal(err)
	}
	if err := m.RemoveWorker("alice", "w1"); err != nil {
		t.Fatal(err)
	}
	newSince := time.Now().UTC()
	if err := s.AddWorker("alice", yesmen.Worker{Name: "w1", StartedAt: newSince}); err != nil {
		t.Fatal(err)
	}
	m.StopByUser("alice/w1")
	newPause := reviewR4WorkerRecord(t, s, "w1").PausedAt
	m.continueTasks("alice")
	reviewR5Drain(t, m)
	w := reviewR4WorkerRecord(t, s, "w1")
	if !w.StartedAt.Equal(newSince) || !w.PausedAt.Equal(newPause) || newPause.IsZero() {
		t.Fatal("old retry crossed actual worker removal/rehire")
	}
	i, err := m.Get("alice/w1")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(i.Detail, "Given work, but its Stop could not be cleared") {
		t.Fatal("new worker displays old resume failure")
	}
	if got := prompts("alice/w1"); len(got) != 1 {
		t.Errorf("retry replayed worker input: %q", got)
	}
}
