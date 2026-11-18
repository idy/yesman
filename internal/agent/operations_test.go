package agent

import (
	"context"
	"testing"
	"time"

	"github.com/idy/yesman/internal/yesmen"
)

func TestExternalOperationRetirementFence(t *testing.T) {
	for _, retirement := range []string{"archive", "remove-worker", "stop", "shutdown"} {
		t.Run(retirement, func(t *testing.T) {
			m, store := newTestManager(t, 0)
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			name := "lead"
			if retirement == "remove-worker" {
				name = "worker"
				if err := store.AddWorker("alice", yesmen.Worker{Name: name, StartedAt: time.Now().UTC()}); err != nil {
					t.Fatal(err)
				}
			}
			id := ID("alice", name)
			if _, err := m.Messages(ctx, id); err != nil {
				t.Fatal(err)
			}
			y, _ := store.Get("alice")
			info, err := m.Get(id)
			if err != nil {
				t.Fatal(err)
			}
			m.mu.Lock()
			token := m.agents[id].token
			m.mu.Unlock()
			opCtx, finish, err := m.BeginOperation(ctx, token, info, y.HiredAt)
			if err != nil {
				t.Fatal(err)
			}
			defer finish()
			retired := make(chan error, 1)
			go func() {
				switch retirement {
				case "archive":
					_, err := archiveManaged(ctx, m, store, "alice")
					retired <- err
				case "remove-worker":
					retired <- m.RemoveWorker("alice", name)
				case "stop":
					m.Stop(id)
					retired <- nil
				case "shutdown":
					m.StopAll()
					retired <- nil
				}
			}()
			select {
			case <-opCtx.Done():
			case <-ctx.Done():
				t.Fatal("retirement did not cancel operation")
			}
			select {
			case <-retired:
				t.Fatal("retirement did not drain operation")
			default:
			}
			// The manager remains readable while retirement waits for external I/O.
			read := make(chan struct{})
			go func() { m.mu.Lock(); m.mu.Unlock(); close(read) }()
			select {
			case <-read:
			case <-ctx.Done():
				t.Fatal("retirement holds global manager lock")
			}
			if retirement == "archive" {
				if _, err := store.Hire("bob"); err != nil {
					t.Fatal(err)
				}
				if _, err := m.Messages(ctx, "bob/lead"); err != nil {
					t.Fatal("other workspace blocked", err)
				}
			}
			finish()
			select {
			case err := <-retired:
				if err != nil {
					t.Fatal(err)
				}
			case <-ctx.Done():
				t.Fatal("retirement did not finish")
			}
			if _, _, err := m.BeginOperation(ctx, token, info, y.HiredAt); err == nil {
				t.Fatal("retired request admitted")
			}
			if retirement == "shutdown" {
				return
			}
			if retirement == "archive" {
				if _, err := store.Hire("alice"); err != nil {
					t.Fatal(err)
				}
			}
			if retirement == "remove-worker" {
				if err := store.AddWorker("alice", yesmen.Worker{Name: name, StartedAt: time.Now().UTC()}); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := m.Messages(ctx, id); err != nil {
				t.Fatal(err)
			}
			current, _ := m.Get(id)
			fresh, _ := store.Get("alice")
			m.mu.Lock()
			newToken := m.agents[id].token
			m.mu.Unlock()
			if _, _, err := m.BeginOperation(ctx, token, info, y.HiredAt); err == nil {
				t.Fatal("old actor borrowed replacement")
			}
			newCtx, newFinish, err := m.BeginOperation(ctx, newToken, current, fresh.HiredAt)
			if err != nil {
				t.Fatal("replacement cannot operate", err)
			}
			defer newFinish()
			finish() // delayed old completion cannot cancel the new actor
			if newCtx.Err() != nil {
				t.Fatal("old completion canceled replacement")
			}
			wrong := current
			wrong.Since = current.Since.Add(-time.Second)
			if _, _, err := m.BeginOperation(ctx, newToken, wrong, fresh.HiredAt); err == nil {
				t.Fatal("wrong actor generation accepted")
			}
		})
	}
}
