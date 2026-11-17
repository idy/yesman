//go:build unix

package yesmen

import (
	"strings"
	"syscall"
	"testing"
	"time"
)

// A FIFO in place of topics.json is refused at once, not waited on with the
// topics lock and the workspace's lease held.
func TestTopicsRefuseAFifoWithoutWaiting(t *testing.T) {
	s, y := topicStore(t)
	if err := syscall.Mkfifo(y.TopicsFile(), 0o644); err != nil {
		t.Skip("mkfifo:", err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := s.Topics("alice", y.HiredAt)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "not a regular file") {
			t.Fatalf("a FIFO was taken for topics.json: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("reading topics waited on a FIFO")
	}
	// The workspace is not held: other topic calls and counts answer too.
	if c := s.TopicCounts("alice"); c.Error == "" {
		t.Fatal("counts must report the FIFO")
	}
	if _, err := s.CreateTopic("alice", y.HiredAt, TopicByUser, TopicInput{Title: "x"}); err == nil {
		t.Fatal("wrote over a FIFO")
	}
}
