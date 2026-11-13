package main

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"

	"github.com/egoist/mygo"
)

// updateEvent tells the page where updating yesman stands.
var updateEvent = mygo.NewEvent[UpdateView]("update")

// The states of an update, as UpdateView.State.
const (
	updateIdle        = "idle"        // up to date, or not checked yet
	updateChecking    = "checking"    // asking for a newer version
	updateAvailable   = "available"   // a newer version can be installed
	updateDownloading = "downloading" // installing it
	updateReady       = "ready"       // installed: it runs after a restart
	updateFailed      = "failed"      // the install failed, and can be tried again
)

// UpdateView is where updating yesman stands, for the titlebar and About.
type UpdateView struct {
	// Enabled is false for builds that cannot update themselves:
	// development builds, and installs yesman cannot write to.
	Enabled bool   `json:"enabled"`
	State   string `json:"state"`
	// Version and Notes (Markdown) describe the newer version.
	Version string `json:"version,omitempty"`
	Notes   string `json:"notes,omitempty"`
	// Progress runs from 0 to 1 while the update downloads.
	Progress float64 `json:"progress,omitempty"`
	// Error says why the last check or install failed.
	Error string `json:"error,omitempty"`
	// CheckedAt is when a check last got an answer.
	CheckedAt time.Time `json:"checkedAt,omitzero"`
}

// found is a newer version that a check found.
type found struct {
	version, notes string
	install        func(ctx context.Context, progress func(downloaded, total int64)) error
}

// updates checks for new versions of yesman and installs them, through
// mygo.Updater, which takes signed releases of idy/yesman on GitHub.
type updates struct {
	// Hooks for tests; mygo.Updater's by default.
	enabled func() bool
	find    func(ctx context.Context) (*found, error)
	changed func(UpdateView)

	mu      sync.Mutex
	view    UpdateView
	pending *found
}

// updateInterval is how often a running app looks for a new version.
const updateInterval = 6 * time.Hour

func newUpdates() *updates {
	return &updates{
		enabled: mygo.Updater.Enabled,
		find: func(ctx context.Context) (*found, error) {
			up, err := mygo.Updater.Check(ctx)
			if err != nil || up == nil {
				return nil, err
			}
			return &found{version: up.Version, notes: up.Notes, install: up.Install}, nil
		},
		changed: func(v UpdateView) { _ = updateEvent.Broadcast(v) },
		view:    UpdateView{State: updateIdle},
	}
}

// start checks shortly after launch and then every few hours. Builds that
// cannot update themselves never check.
func (u *updates) start() {
	enabled := u.enabled()
	u.mu.Lock()
	u.view.Enabled = enabled
	u.mu.Unlock()
	if !enabled {
		return
	}
	log.Printf("updates: checking for versions newer than %s every %s", mygo.App.Version(), updateInterval)
	go func() {
		time.Sleep(10 * time.Second) // after the window has opened
		for {
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			if _, err := u.check(ctx); err != nil {
				log.Print("check for updates: ", err)
			}
			cancel()
			time.Sleep(updateInterval)
		}
	}()
}

func (u *updates) snapshot() UpdateView {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.view
}

func (u *updates) set(fn func(v *UpdateView)) UpdateView {
	u.mu.Lock()
	fn(&u.view)
	v := u.view
	u.mu.Unlock()
	u.changed(v)
	return v
}

// check asks for a newer version. A check that fails, as offline, keeps
// what an earlier one found. While an update installs, or waits for a
// restart, it does nothing.
func (u *updates) check(ctx context.Context) (UpdateView, error) {
	u.mu.Lock()
	v := u.view
	busy := v.State == updateChecking || v.State == updateDownloading || v.State == updateReady
	if !v.Enabled || busy {
		u.mu.Unlock()
		if !v.Enabled {
			return v, mygo.ErrUpdatesDisabled
		}
		return v, nil
	}
	u.view.State, u.view.Error = updateChecking, ""
	u.mu.Unlock()
	u.changed(u.snapshot())

	f, err := u.find(ctx)
	u.mu.Lock()
	switch {
	case err != nil:
		u.view.State, u.view.Error = v.State, err.Error()
	case f == nil:
		u.pending = nil
		u.view.State, u.view.Version, u.view.Notes = updateIdle, "", ""
		u.view.CheckedAt = time.Now()
	default:
		u.pending = f
		u.view.State, u.view.Version, u.view.Notes = updateAvailable, f.version, f.notes
		u.view.CheckedAt = time.Now()
	}
	v = u.view
	u.mu.Unlock()
	u.changed(v)
	return v, err
}

// install downloads and installs the version a check found, in the
// background. The running app is not touched: the new version runs after
// a restart.
func (u *updates) install() error {
	u.mu.Lock()
	f := u.pending
	if f == nil || u.view.State != updateAvailable && u.view.State != updateFailed {
		u.mu.Unlock()
		return errors.New("there is no update to install")
	}
	u.view.State, u.view.Progress, u.view.Error = updateDownloading, 0, ""
	v := u.view
	u.mu.Unlock()
	u.changed(v)

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		var last time.Time
		err := f.install(ctx, func(downloaded, total int64) {
			if total <= 0 || time.Since(last) < 250*time.Millisecond {
				return
			}
			last = time.Now()
			u.set(func(v *UpdateView) { v.Progress = float64(downloaded) / float64(total) })
		})
		u.set(func(v *UpdateView) {
			if err != nil {
				v.State, v.Error = updateFailed, err.Error()
				return
			}
			v.State, v.Progress = updateReady, 1
		})
	}()
	return nil
}

// Update reports where updating yesman stands.
func (s *service) Update() UpdateView { return s.app.updates.snapshot() }

// CheckForUpdates looks for a newer version now.
func (s *service) CheckForUpdates(ctx context.Context) (UpdateView, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	return s.app.updates.check(ctx)
}

// InstallUpdate downloads and installs the newer version. It runs after
// RestartToUpdate, or the next time yesman starts.
func (s *service) InstallUpdate() error { return s.app.updates.install() }

// RestartToUpdate quits yesman and starts the installed version. Its
// agents stop, and resume their sessions when you next talk to them.
func (s *service) RestartToUpdate() error {
	if s.app.updates.snapshot().State != updateReady {
		return errors.New("no update is ready")
	}
	mygo.App.Relaunch()
	return nil
}
