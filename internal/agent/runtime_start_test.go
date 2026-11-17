package agent

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestStopCancelsManagedRuntimePreparation(t *testing.T) {
	for _, all := range []bool{false, true} {
		name := "agent"
		if all {
			name = "all"
		}
		t.Run(name, func(t *testing.T) {
			m, _ := newTestManager(t, 0)
			entered := make(chan struct{})
			m.cfg.Command = func(ctx context.Context) (string, []string, error) {
				close(entered)
				<-ctx.Done()
				return "", nil, ctx.Err()
			}
			prompted := make(chan error, 1)
			go func() { prompted <- m.Prompt(context.Background(), "alice/lead", "blocked startup") }()
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("runtime preparation did not begin")
			}
			// Manager reads stay available even while startup's download waits.
			if _, err := m.Get("alice/lead"); err != nil {
				t.Fatal(err)
			}
			stopped := make(chan struct{})
			go func() {
				if all {
					m.StopAll()
				} else {
					m.Stop("alice/lead")
				}
				close(stopped)
			}()
			select {
			case <-stopped:
			case <-time.After(time.Second):
				t.Fatal("Stop waited for a ten-minute runtime download")
			}
			select {
			case err := <-prompted:
				if !errors.Is(err, errStopped) {
					t.Fatalf("prompt: %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("startup did not release")
			}
			if m.running("alice/lead") != nil {
				t.Fatal("stopped startup launched pi")
			}
		})
	}
}
