package agent

import (
	"bufio"
	"context"
	"errors"
	"io"
	"testing"
	"time"
)

func TestRPCWriteAttemptCompletion(t *testing.T) {
	for _, mode := range []string{"refused", "exited", "pipe closed"} {
		t.Run(mode, func(t *testing.T) {
			r, w := io.Pipe()
			r.Close()
			defer w.Close()
			p := &proc{stdin: w, pending: make(map[string]waiter), done: make(chan struct{})}
			var admit func() error
			want := io.ErrClosedPipe
			switch mode {
			case "refused":
				want = context.Canceled
				admit = func() error { return want }
			case "exited":
				p.err, want = errExited, errExited
			}
			attempts := 0
			_, err := p.callTracked(t.Context(), map[string]any{"type": "prompt"}, admit, nil, func() { attempts++ })
			if !errors.Is(err, want) || attempts != 1 || len(p.pending) != 0 {
				t.Fatalf("attempts=%d, pending=%d, error=%v", attempts, len(p.pending), err)
			}
		})
	}

	t.Run("write then asynchronous reply", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		r, w := io.Pipe()
		defer r.Close()
		defer w.Close()
		p := &proc{stdin: w, pending: make(map[string]waiter), done: make(chan struct{})}
		admitted, written, observed := make(chan struct{}), make(chan struct{}), make(chan struct{})
		result := make(chan error, 1)
		go func() {
			_, err := p.callTracked(ctx, map[string]any{"type": "prompt"}, func() error { close(admitted); return nil },
				func(response) { close(observed) }, func() { close(written) })
			result <- err
		}()
		select {
		case <-admitted:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
		select {
		case <-written:
			t.Fatal("write reported complete with the record still in the pipe")
		default:
		}
		if _, err := bufio.NewReader(r).ReadString('\n'); err != nil {
			t.Fatal(err)
		}
		select {
		case <-written:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
		select {
		case <-result:
			t.Fatal("RPC completed before the reply")
		default:
		}
		p.handle([]byte(`{"type":"response","id":"y1","success":true}`), nil)
		if err := <-result; err != nil {
			t.Fatal(err)
		}
		select {
		case <-observed:
		default:
			t.Fatal("reply observer did not run")
		}
	})
}
