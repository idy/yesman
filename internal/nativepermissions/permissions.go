// Package nativepermissions manages OS permissions for this application. Reading
// status never requests access or probes protected user data.
package nativepermissions

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"time"
)

type ID string

const (
	Bluetooth     ID = "bluetooth"
	Notifications ID = "notifications"
	FullDisk      ID = "full-disk"
	Microphone    ID = "microphone"
	Camera        ID = "camera"
	Accessibility ID = "accessibility"
	Screen        ID = "screen"
	SystemAudio   ID = "system-audio"
	Input         ID = "input"
	Automation    ID = "automation"
)

// The native adapter indexes this order; change both together.
var ids = [...]ID{Bluetooth, Notifications, FullDisk, Microphone, Camera, Accessibility, Screen, SystemAudio, Input, Automation}

type Item struct {
	ID       ID     `json:"id"`
	State    string `json:"state"`
	Detail   string `json:"detail,omitempty"`
	Error    string `json:"error,omitempty"`
	Request  bool   `json:"request"`
	Settings bool   `json:"settings"`
	Busy     bool   `json:"busy"`
}

// App identifies the running copy macOS evaluates. Signing is "adhoc",
// "signed" or "unsigned"; it comes from this process's own code signature.
type App struct {
	Path    string `json:"path"`
	Signing string `json:"signing,omitempty"`
}
type View struct {
	Platform string `json:"platform"`
	OS       string `json:"os,omitempty"` // macOS product version, for System Settings names
	App      *App   `json:"app,omitempty"`
	Items    []Item `json:"items"`
}
type Result struct {
	Item    Item   `json:"item"`
	Outcome string `json:"outcome"`
}

type adapter interface {
	query(context.Context, ID) (Item, error)
	request(context.Context, ID) error
	open(context.Context, ID) error
	identity() *App // nil unless this is the packaged macOS app
}

// Manager serializes user actions across pages/windows. Native adapters also
// retain their pending request guard after a caller stops waiting: OS prompts
// cannot in general be canceled by closing a window.
type Manager struct {
	platform string
	native   adapter
	mu       sync.Mutex
	acting   bool
	appOnce  sync.Once
	app      *App
}

func New() *Manager { return &Manager{platform: runtime.GOOS, native: newAdapter()} }
func valid(id ID) bool {
	for _, p := range ids {
		if id == p {
			return true
		}
	}
	return false
}

// identity is read once: a running process keeps its code signature.
func (m *Manager) identity() *App {
	m.appOnce.Do(func() { m.app = m.native.identity() })
	return m.app
}

// Ad-hoc builds have code-hash-based designated requirements. Replacing a
// build can invalidate a previous grant while Settings retains its enabled
// entry. A Boolean false cannot distinguish that from an off switch.
func buildBound(id ID) bool { return id == Accessibility || id == Screen || id == Input }

func (m *Manager) read(ctx context.Context, id ID) Item {
	item, err := m.native.query(ctx, id)
	item.ID = id
	if err != nil {
		item.State, item.Error, item.Request = "unknown", err.Error(), false
	}
	// Unknown values never become an authorization or an actionable request.
	switch item.State {
	case "granted", "not-determined", "denied", "restricted", "not-granted", "limited", "manual", "system", "not-applicable", "unavailable", "unknown":
	default:
		item.State, item.Request = "unknown", false
	}
	if item.State != "not-determined" && item.State != "not-granted" {
		item.Request = false
	}
	if item.State == "not-applicable" {
		item.Settings = false
	}
	if app := m.identity(); item.Error == "" && item.State == "not-granted" && buildBound(id) && app != nil && app.Signing == "adhoc" {
		item.Detail = "adhoc-build"
	}
	return item
}

func (m *Manager) List(ctx context.Context) View {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	view := View{Platform: m.platform, OS: osVersion(), App: m.identity(), Items: make([]Item, len(ids))}
	var wg sync.WaitGroup
	for i, id := range ids {
		wg.Add(1)
		go func() { defer wg.Done(); view.Items[i] = m.read(ctx, id) }()
	}
	wg.Wait()
	return view
}

func (m *Manager) Act(ctx context.Context, id ID, action string) (Result, error) {
	if !valid(id) || (action != "request" && action != "settings") {
		return Result{}, errors.New("Unknown permission or action")
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	m.mu.Lock()
	if m.acting {
		m.mu.Unlock()
		return Result{}, errors.New("Another permission action is still in progress")
	}
	m.acting = true
	m.mu.Unlock()
	defer func() { m.mu.Lock(); m.acting = false; m.mu.Unlock() }()
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	before := m.read(ctx, id)
	if action == "settings" {
		if !before.Settings {
			return Result{}, errors.New("No system settings entry is available for this permission")
		}
		if err := m.native.open(ctx, id); err != nil {
			return Result{}, err
		}
		// Launching Settings does not authorize anything. Query on returning.
		return Result{Item: before, Outcome: "settings-opened"}, nil
	}
	if before.Error != "" {
		return Result{}, errors.New(before.Error)
	}
	if before.Busy {
		return Result{}, errors.New("A system permission request is still pending; answer it before trying again")
	}
	if !before.Request {
		return Result{}, errors.New("This permission must be managed in system settings")
	}
	if err := m.native.request(ctx, id); err != nil {
		return Result{}, err
	}
	after := m.read(ctx, id)
	outcome := "unchanged"
	if after.Error != "" {
		outcome = "query-failed"
	} else if after.State == "granted" {
		outcome = "granted"
	} else if after.State == "denied" {
		outcome = "denied"
	} else if after.State == "restricted" {
		outcome = "restricted"
	}
	return Result{Item: after, Outcome: outcome}, nil
}
