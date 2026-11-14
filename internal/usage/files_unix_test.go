//go:build !windows

package usage

import (
	"context"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// A named pipe in a log's place is reported, and does not stall the read.
func TestSessionsSkipPipes(t *testing.T) {
	line := resp{id: "a1", provider: "p", model: "m", at: t0, duration: 1000, output: 5}.line()
	ws := workspace(t, map[string]string{"w": line})
	if err := syscall.Mkfifo(filepath.Join(ws, ".yesman", "sessions", "w", "pipe.jsonl"), 0o644); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	var records map[string][]Record
	var st DirStats
	var err error
	go func() {
		records, st, err = (&Sessions{}).Scan(context.Background(), ws)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("reading stalled")
	}
	if err != nil || outputs(records) != 5 || !hasError(st, "w/pipe.jsonl: not a regular file") {
		t.Fatalf("records %d, %+v, %v", outputs(records), st, err)
	}
}
