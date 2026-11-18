//go:build unix

package agent

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/idy/yesman/internal/yesmen"
)

func TestReviewR3WorkerStopMustCancelBeforeSlowMetadata(t *testing.T) {
	m, s := newTestManager(t, 0)
	y, _ := s.Get("alice")
	if err := s.AddWorker("alice", yesmen.Worker{Name: "w1", StartedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	_, p, err := m.ensure("alice/w1")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(y.MetaDir(), "workers.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path, path+".saved"); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
	entered, release, written := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		f, e := os.OpenFile(path, os.O_WRONLY, 0600)
		if e != nil {
			written <- e
			return
		}
		close(entered)
		<-release
		_, e = f.Write(data)
		f.Close()
		written <- e
	}()
	done := make(chan struct{})
	go func() { m.StopByUser("alice/w1"); close(done) }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("real FIFO writer did not rendezvous")
	}
	blocked := false
	select {
	case <-p.done:
	case <-time.After(250 * time.Millisecond):
		blocked = true
	}
	// Existing reader retains its real FIFO fd; all later accesses see normal metadata.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path+".saved", path); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-written; err != nil {
		t.Fatal(err)
	}
	<-done
	if blocked {
		t.Fatal("worker process still uncancelled after 250ms because Stop waits for worker pause metadata I/O")
	}
}
