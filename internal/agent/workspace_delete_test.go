package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/idy/yesman/internal/yesmen"
)

func TestWorkspaceDeletionDrainsWriterAndPropagatesFailure(t *testing.T) {
	m, store := newTestManager(t, 0, "FAKE_PI_DELIVERIES=1", "FAKE_PI_DELIVERY_MODE=steer")
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	y, err := store.Get("alice")
	if err != nil {
		t.Fatal(err)
	}
	var failing atomic.Bool
	var once sync.Once
	entered, release := make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	defer unblock()
	m.cfg.deliveryWrite = func(path string, data []byte) error {
		if failing.Load() && strings.HasPrefix(path, y.Path+string(filepath.Separator)) {
			once.Do(func() { close(entered) })
			<-release
			return errors.New("fixture could not persist final receipt")
		}
		return writeDeliveryFile(path, data)
	}
	if _, err := m.Submit(ctx, "alice/lead", deliveryTestID(901), "pending", nil); err != nil {
		t.Fatal(err)
	}
	flushDeliveries(t, m, "alice/lead")
	target, err := store.PrepareDeletion(y.Name, y.HiredAt)
	if err != nil {
		t.Fatal(err)
	}
	remove := func() error {
		_, err := store.Delete(ctx, target, func(context.Context, string) error { return nil }, func(y yesmen.Yesman) error { return m.StopYesmanContext(ctx, y.Name) })
		return err
	}
	failing.Store(true)
	result := make(chan error, 1)
	go func() { result <- remove() }()
	select {
	case <-entered:
	case err := <-result:
		t.Fatal("deletion finished before draining", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	select {
	case <-result:
		t.Fatal("deletion passed an active writer")
	default:
	}
	if _, err := store.Hire("bob"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Messages(ctx, "bob/lead"); err != nil {
		t.Fatal(err)
	}
	unblock()
	if err := <-result; err == nil || !strings.Contains(err.Error(), "persist final receipt") {
		t.Fatal(err)
	}
	if got, err := store.Get(y.Name); err != nil || got != y {
		t.Fatal("writer failure lost workspace", got, err)
	}
	failing.Store(false)
	if err := remove(); err != nil {
		t.Fatal("retry:", err)
	}
	if _, err := os.Stat(y.Path); !os.IsNotExist(err) {
		t.Fatal(err)
	}
}
