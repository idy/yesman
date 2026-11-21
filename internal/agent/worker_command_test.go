package agent

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/idy/yesman/internal/yesmen"
)

func workerTestLead(t *testing.T, ctx context.Context, m *Manager, y yesmen.Yesman) (Info, string) {
	t.Helper()
	id := ID(y.Name, yesmen.LeadName)
	if _, err := m.Messages(ctx, id); err != nil {
		t.Fatal(err)
	}
	lead, err := m.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return lead, m.agents[id].token
}

// Exercise the same three Manager operations the lead's tools use. The fake
// pi keeps the actual process/credential lifecycle and workspace Store.
func TestLeadWorkerManagement(t *testing.T) {
	m, store := newTestManager(t, 0)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	y, err := store.Get("alice")
	if err != nil {
		t.Fatal(err)
	}
	lead, token := workerTestLead(t, ctx, m, y)
	selected := func(context.Context, []Model) (Choice, error) {
		return Choice{Provider: "alpha", Model: "shared", Effort: "max"}, nil
	}
	w, err := m.StartWorkerBy(ctx, token, lead, y, "builder", "Build", "test task", selected)
	if err != nil || w.Role != RoleWorker || w.Model != "shared" || w.Since.IsZero() {
		t.Fatalf("start: %+v %v", w, err)
	}
	if err := m.SendWorkerBy(ctx, token, lead, y, w, "follow up"); err != nil {
		t.Fatal(err)
	}
	if err := m.RemoveWorkerBy(ctx, token, lead, y, w); err != nil {
		t.Fatal(err)
	}
	if ws, err := store.Workers(y.Name); err != nil || len(ws) != 0 {
		t.Fatal(ws, err)
	}
	missing := func(context.Context, []Model) (Choice, error) { return Choice{Provider: "missing", Model: "none"}, nil }
	if _, err := m.StartWorkerBy(ctx, token, lead, y, "builder", "Build", "test task", missing); err == nil {
		t.Fatal("invalid model accepted")
	}
	if ws, err := store.Workers(y.Name); err != nil || len(ws) != 0 {
		t.Fatal("failed startup retained a worker", ws, err)
	}
}

// The cancellable process lease allows Delete to acquire the workspace while
// runtime preparation is still in progress, cancel the start and join it.
func TestLeadWorkerStartReleasesWorkspaceBeforeRuntime(t *testing.T) {
	m, s := newTestManager(t, 0)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	workerStart := make(chan struct{})
	first := true // only the lead's startup and the following worker use Command
	m.cfg.Command = func(start context.Context) (string, []string, error) {
		if first {
			first = false
			return exe, nil, nil
		}
		close(workerStart)
		<-start.Done()
		return "", nil, start.Err()
	}
	y, err := s.Get("alice")
	if err != nil {
		t.Fatal(err)
	}
	lead, token := workerTestLead(t, ctx, m, y)
	done := make(chan error, 1)
	go func() {
		_, err := m.StartWorkerBy(ctx, token, lead, y, "builder", "Build", "prepare", nil)
		done <- err
	}()
	select {
	case <-workerStart:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	target, err := s.PrepareDeletion(y.Name, y.HiredAt)
	if err != nil {
		t.Fatal(err)
	}
	retired := make(chan error, 1)
	go func() {
		_, err := s.Delete(ctx, target, func(context.Context, string) error { return nil }, func(y yesmen.Yesman) error { return m.StopYesmanContext(ctx, y.Name) })
		retired <- err
	}()
	select {
	case err := <-retired:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("workspace retirement waited for runtime preparation")
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("interrupted startup reported success")
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	fresh, err := s.Hire(y.Name)
	if err != nil {
		t.Fatal(err)
	}
	if ws, err := s.Workers(fresh.Name); err != nil || len(ws) != 0 {
		t.Fatal(ws, err)
	}
}

// Ordinary generation checks, independent of HTTP transport or scheduling.
// Failure cleanup holds the same target as the initial admission.
func TestWorkerCommandIdentities(t *testing.T) {
	m, s := newTestManager(t, 0)
	y, err := s.Get("alice")
	if err != nil {
		t.Fatal(err)
	}
	w := yesmen.Worker{Name: "helper", StartedAt: time.Now().UTC()}
	if err := s.AddWorker(y.Name, w); err != nil {
		t.Fatal(err)
	}
	bind := func(hire yesmen.Yesman, since time.Time) (*workerCommand, error) {
		return m.bindWorker(t.Context(), hire, w.Name, &since, false)
	}
	original, err := bind(y, w.StartedAt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bind(y, w.StartedAt.Add(-time.Second)); !errors.Is(err, ErrRetired) {
		t.Fatal(err)
	}
	other := y
	other.HiredAt = y.HiredAt.Add(-time.Minute)
	if _, err := bind(other, w.StartedAt); !errors.Is(err, yesmen.ErrRehired) {
		t.Fatal(err)
	}
	if err := m.RemoveWorkerOf(y.Name, w.Name, y.HiredAt, w.StartedAt); err != nil {
		t.Fatal(err)
	}
	w.StartedAt = w.StartedAt.Add(time.Minute)
	if err := s.AddWorker(y.Name, w); err != nil {
		t.Fatal(err)
	}
	next, err := bind(y, w.StartedAt)
	if err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	oldCurrent, newCurrent := original.currentLocked(t.Context()), next.currentLocked(t.Context())
	m.mu.Unlock()
	if oldCurrent || !newCurrent {
		t.Fatal("incorrect command generations")
	}
	if err := original.remove(); !errors.Is(err, ErrRetired) {
		t.Fatal("expected expired cleanup", err)
	}
	if err := s.RemoveWorkerOf(y, w.Name, original.since); err == nil {
		t.Fatal("old cleanup modified worker record")
	}
	if got, err := m.Get(next.id); err != nil || !got.Since.Equal(w.StartedAt) {
		t.Fatal(got, err)
	}
}
