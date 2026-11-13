package main

import (
	"context"
	"errors"
	"testing"
	"time"
)

func testUpdates(find func(context.Context) (*found, error)) (*updates, chan UpdateView) {
	seen := make(chan UpdateView, 64)
	u := &updates{
		enabled: func() bool { return true },
		find:    find,
		changed: func(v UpdateView) { seen <- v },
		view:    UpdateView{State: updateIdle},
	}
	u.start() // enables it; its first check waits 10s, past these tests
	return u, seen
}

func waitState(t *testing.T, seen chan UpdateView, want string) UpdateView {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case v := <-seen:
			if v.State == want {
				return v
			}
		case <-timeout:
			t.Fatalf("the update never reached %s", want)
		}
	}
}

func TestUpdateFoundAndInstalled(t *testing.T) {
	release := make(chan struct{})
	u, seen := testUpdates(func(context.Context) (*found, error) {
		return &found{version: "0.2.0", notes: "Fixes", install: func(_ context.Context, progress func(int64, int64)) error {
			progress(10, 10)
			<-release
			return nil
		}}, nil
	})
	if err := u.install(); err == nil {
		t.Fatal("installed before a check found anything")
	}
	v, err := u.check(context.Background())
	if err != nil || v.State != updateAvailable || v.Version != "0.2.0" || v.Notes != "Fixes" || v.CheckedAt.IsZero() {
		t.Fatalf("check = %+v, %v", v, err)
	}
	if err := u.install(); err != nil {
		t.Fatal(err)
	}
	waitState(t, seen, updateDownloading)
	if err := u.install(); err == nil {
		t.Error("a second install started while the first ran")
	}
	if v, _ := u.check(context.Background()); v.State != updateDownloading {
		t.Errorf("a check during the install changed it to %+v", v)
	}
	close(release)
	if v := waitState(t, seen, updateReady); v.Progress != 1 {
		t.Errorf("ready at progress %v", v.Progress)
	}
}

func TestUpdateFailures(t *testing.T) {
	offline := false
	u, seen := testUpdates(func(context.Context) (*found, error) {
		if offline {
			return nil, errors.New("offline")
		}
		return &found{version: "0.2.0", install: func(context.Context, func(int64, int64)) error { return errors.New("bad signature") }}, nil
	})
	if _, err := u.check(context.Background()); err != nil {
		t.Fatal(err)
	}
	// A failed check keeps the version found before.
	offline = true
	if v, err := u.check(context.Background()); err == nil || v.State != updateAvailable || v.Error != "offline" {
		t.Errorf("offline check = %+v, %v", v, err)
	}
	if err := u.install(); err != nil {
		t.Fatal(err)
	}
	if v := waitState(t, seen, updateFailed); v.Error != "bad signature" {
		t.Errorf("failed install = %+v", v)
	}
	// It can be tried again.
	if err := u.install(); err != nil {
		t.Errorf("retry: %v", err)
	}
	waitState(t, seen, updateFailed)
}

func TestUpdatesDisabled(t *testing.T) {
	u := &updates{enabled: func() bool { return false }, changed: func(UpdateView) {}, view: UpdateView{State: updateIdle}}
	u.find = func(context.Context) (*found, error) { t.Fatal("a disabled build checked"); return nil, nil }
	u.start()
	if v, err := u.check(context.Background()); err == nil || v.Enabled {
		t.Errorf("check = %+v, %v", v, err)
	}
}
