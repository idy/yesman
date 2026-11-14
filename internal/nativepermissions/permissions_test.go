package nativepermissions

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
)

type fakeAdapter struct {
	mu              sync.Mutex
	item            Item
	err             error
	queryErr        error
	after           *Item
	started         chan struct{}
	release         chan struct{}
	requests, opens int
}

func (f *fakeAdapter) query(ctx context.Context, id ID) (Item, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.item, f.queryErr
}
func (f *fakeAdapter) request(ctx context.Context, id ID) error {
	f.mu.Lock()
	f.requests++
	f.mu.Unlock()
	if f.started != nil {
		close(f.started)
		select {
		case <-f.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.after != nil {
		f.item = *f.after
	}
	return f.err
}
func (f *fakeAdapter) open(context.Context, ID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.opens++
	return f.err
}
func manager(f *fakeAdapter) *Manager { return &Manager{platform: "fixture", native: f} }
func TestReadNeverRequestsAndFailsClosed(t *testing.T) {
	f := &fakeAdapter{item: Item{State: "granted", Request: true, Settings: true}, queryErr: errors.New("query failed")}
	v := manager(f).List(context.Background())
	if len(v.Items) != len(ids) {
		t.Fatal(v)
	}
	for _, item := range v.Items {
		if item.State != "unknown" || item.Request || item.Error == "" {
			t.Fatal(item)
		}
	}
	if f.requests != 0 || f.opens != 0 {
		t.Fatal("read caused a side effect")
	}
}
func TestPermissionStatesAndActions(t *testing.T) {
	for _, state := range []string{"not-determined", "not-granted", "granted", "denied", "restricted", "manual", "system", "not-applicable", "unavailable", "limited", "new-os-value"} {
		t.Run(state, func(t *testing.T) {
			f := &fakeAdapter{item: Item{State: state, Request: true, Settings: true}}
			m := manager(f)
			item := m.read(context.Background(), Camera)
			if item.Request != (state == "not-determined" || state == "not-granted") {
				t.Fatal(item)
			}
			_, err := m.Act(context.Background(), Camera, "request")
			if (err == nil) != item.Request {
				t.Fatalf("%+v %v", item, err)
			}
			if state == "new-os-value" && item.State != "unknown" {
				t.Fatal(item)
			}
		})
	}
}
func TestSettingsIsNotAuthorization(t *testing.T) {
	f := &fakeAdapter{item: Item{State: "manual", Settings: true}}
	result, err := manager(f).Act(context.Background(), FullDisk, "settings")
	if err != nil || result.Outcome != "settings-opened" || result.Item.State != "manual" || f.opens != 1 || f.requests != 0 {
		t.Fatalf("%+v %v %+v", result, err, f)
	}
}
func TestActionWhitelistAndErrors(t *testing.T) {
	f := &fakeAdapter{item: Item{State: "not-determined", Request: true, Settings: true}}
	m := manager(f)
	for _, args := range [][2]string{{"bad", "request"}, {"camera", "https://example.org"}, {"camera;touch /tmp/invalid", "settings"}, {"", ""}} {
		if _, err := m.Act(context.Background(), ID(args[0]), args[1]); err == nil {
			t.Fatal(args)
		}
	}
	if f.opens != 0 || f.requests != 0 {
		t.Fatal("invalid action reached native adapter")
	}
	f.queryErr = errors.New("cannot query")
	if _, err := m.Act(context.Background(), Camera, "request"); err == nil || f.requests != 0 {
		t.Fatal(err)
	}
	f.queryErr = nil
	f.err = errors.New("native failure")
	if _, err := m.Act(context.Background(), Camera, "request"); err == nil {
		t.Fatal("failure lost")
	}
	if _, err := m.Act(context.Background(), Camera, "settings"); err == nil {
		t.Fatal("launch failure lost")
	}
	f.item.Busy = true
	f.err = nil
	if _, err := m.Act(context.Background(), Camera, "request"); err == nil {
		t.Fatal("pending request duplicated")
	}
}
func TestRequestReentryAndCancellation(t *testing.T) {
	f := &fakeAdapter{item: Item{State: "not-determined", Request: true}, started: make(chan struct{}), release: make(chan struct{})}
	m := manager(f)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := m.Act(ctx, Camera, "request"); done <- err }()
	<-f.started
	if _, err := m.Act(context.Background(), Microphone, "request"); err == nil {
		t.Fatal("concurrent prompt allowed")
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	f.mu.Lock()
	requests := f.requests
	f.mu.Unlock()
	if requests != 1 {
		t.Fatal(requests)
	}
	if _, err := m.Act(ctx, Camera, "request"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
func TestRequestOutcomes(t *testing.T) {
	for _, tc := range []struct{ state, outcome string }{{"granted", "granted"}, {"denied", "denied"}, {"restricted", "restricted"}, {"not-determined", "unchanged"}, {"not-granted", "unchanged"}, {"limited", "unchanged"}} {
		f := &fakeAdapter{item: Item{State: "not-determined", Request: true}, after: &Item{State: tc.state}}
		got, err := manager(f).Act(context.Background(), Camera, "request")
		if err != nil || got.Outcome != tc.outcome || got.Item.State != tc.state {
			t.Fatalf("%+v: %+v %v", tc, got, err)
		}
	}
}
func TestSystemPlatformLimitations(t *testing.T) {
	for _, platform := range []string{"windows", "linux", "darwin"} {
		for _, id := range ids {
			item := systemItem(platform, id)
			if item.Request || item.State == "granted" {
				t.Fatal(platform, id, item)
			}
			if platform == "linux" && item.Settings {
				t.Fatal("portable Linux settings entry fabricated")
			}
			if platform == "windows" && id == FullDisk && (item.State != "not-applicable" || item.Settings) {
				t.Fatal(item)
			}
			if platform == "darwin" && item.State != "unavailable" {
				t.Fatal("non-cgo native permission fabricated")
			}
		}
	}
}
func TestSettingsDestinations(t *testing.T) {
	for _, platform := range []string{"darwin", "windows", "linux"} {
		for _, id := range ids {
			url := settingsURL(platform, id)
			if platform == "darwin" && !strings.HasPrefix(url, "x-apple.systempreferences:") {
				t.Fatal(platform, id, url)
			}
			if platform == "windows" && url != "" && !strings.HasPrefix(url, "ms-settings:") {
				t.Fatal(url)
			}
			if platform == "linux" && url != "" {
				t.Fatal(url)
			}
		}
		for _, id := range []ID{"camera?x=1", "https://example.org", "full-disk;echo nope", ""} {
			if settingsURL(platform, id) != "" {
				t.Fatal(platform, id)
			}
		}
	}
	if !reflect.DeepEqual(ids, [...]ID{Bluetooth, Notifications, FullDisk, Microphone, Camera, Accessibility, Screen, Input, Automation}) {
		t.Fatal("native ABI order changed")
	}
}
