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
	Input         ID = "input"
	Automation    ID = "automation"
)

var ids = [...]ID{Bluetooth, Notifications, FullDisk, Microphone, Camera, Accessibility, Screen, Input, Automation}

type Item struct {
	ID       ID     `json:"id"`
	State    string `json:"state"`
	Detail   string `json:"detail,omitempty"`
	Error    string `json:"error,omitempty"`
	Request  bool   `json:"request"`
	Settings bool   `json:"settings"`
	Busy     bool   `json:"busy"`
}
type View struct {
	Platform string `json:"platform"`
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
}

// Manager serializes user actions across pages/windows. Native adapters also
// retain their pending request guard after a caller stops waiting: OS prompts
// cannot in general be canceled by closing a window.
type Manager struct {
	platform string
	native   adapter
	mu       sync.Mutex
	acting   bool
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
	return item
}

func (m *Manager) List(ctx context.Context) View {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	view := View{Platform: m.platform, Items: make([]Item, len(ids))}
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
