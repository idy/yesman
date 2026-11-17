package piruntime

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestShortCallerDoesNotCancelSharedPreparation(t *testing.T) {
	r, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	entered, release := make(chan struct{}, 2), make(chan struct{})
	var runs atomic.Int32
	r.prepareBuild = func(ctx context.Context) error {
		runs.Add(1)
		entered <- struct{}{}
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	short, cancel := context.WithCancel(context.Background())
	defer cancel()
	shortDone := make(chan error, 1)
	go func() { _, _, err := r.Command(short); shortDone <- err }()
	<-entered
	cancel() // same boundary as the Settings 5-second deadline, without sleeping
	select {
	case err = <-shortDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("short waiter: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("caller could not cancel its wait")
	}
	longDone := make(chan error, 1)
	go func() { _, _, err := r.Command(context.Background()); longDone <- err }()
	close(release)
	select {
	case err = <-longDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("shared preparation did not finish")
	}
	if runs.Load() != 1 {
		t.Fatalf("short caller killed shared preparation: %d installations", runs.Load())
	}
}

func TestSharedPreparationFailureCanRetry(t *testing.T) {
	r, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	var runs atomic.Int32
	r.prepareBuild = func(context.Context) error {
		if runs.Add(1) == 1 {
			return errors.New("offline fixture")
		}
		return nil
	}
	if _, _, err = r.Command(context.Background()); err == nil {
		t.Fatal("failed preparation looked ready")
	}
	if r.State() != "failed" || r.PreparationError() != "offline fixture" {
		t.Fatal("failure status was hidden")
	}
	if _, _, err = r.Command(context.Background()); err != nil {
		t.Fatal(err)
	}
	if runs.Load() != 2 {
		t.Fatalf("retry runs=%d", runs.Load())
	}
	if r.State() != "ready" || r.PreparationError() != "" {
		t.Fatal("successful retry retained failed state")
	}
}
