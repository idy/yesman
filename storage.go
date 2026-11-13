package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/egoist/mygo"
	"github.com/idy/yesman/internal/home"
)

// The location must live outside the data it locates. YESMAN_HOME remains an
// explicit override; normal launches remember the user's choice here.
type storageConfig struct {
	Home     string `json:"home"`
	Pending  string `json:"pending,omitempty"`
	Previous string `json:"previous,omitempty"`
	Error    string `json:"error,omitempty"`
}

type storageLocation struct {
	mu       sync.Mutex
	path     string
	override bool
	config   storageConfig
}

func (c storageConfig) save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".storage-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func openStorage(ctx context.Context) (home.Home, *storageLocation, error) {
	h, err := home.Default()
	if err != nil {
		return home.Home{}, nil, err
	}
	if os.Getenv("YESMAN_HOME") != "" {
		return h, &storageLocation{override: true, config: storageConfig{Home: h.Root}}, nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return home.Home{}, nil, fmt.Errorf("find storage configuration: %w", err)
	}
	return loadStorage(ctx, filepath.Join(dir, "yesman", "storage.json"), h.Root)
}

// A pending relocation runs before any agents, API handlers or file watchers
// start. CopyTo leaves the old directory intact until the new one is complete.
func loadStorage(ctx context.Context, path, fallback string) (home.Home, *storageLocation, error) {
	loc := &storageLocation{path: path, config: storageConfig{Home: fallback}}
	data, err := os.ReadFile(path)
	if err == nil {
		if err := json.Unmarshal(data, &loc.config); err != nil {
			return home.Home{}, nil, fmt.Errorf("read storage configuration: %w", err)
		}
		if !filepath.IsAbs(loc.config.Home) {
			return home.Home{}, nil, errors.New("the configured yesman folder must be an absolute path")
		}
		// An unplugged drive must not silently become a new empty home.
		if info, err := os.Stat(loc.config.Home); err != nil || !info.IsDir() {
			return home.Home{}, nil, fmt.Errorf("the configured yesman folder is unavailable: %s", loc.config.Home)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return home.Home{}, nil, fmt.Errorf("read storage configuration: %w", err)
	}
	h := home.Home{Root: loc.config.Home}
	if loc.config.Pending == "" {
		return h, loc, nil
	}
	next := loc.config
	destination := next.Pending
	next.Pending = ""
	if err := h.CopyTo(ctx, destination); err != nil {
		next.Error = fmt.Sprintf("Could not move to %s: %v. Your data is still in %s. Choose another empty folder and try again.", destination, err, h.Root)
	} else {
		next.Home, next.Previous, next.Error = destination, h.Root, ""
	}
	if err := next.save(path); err != nil {
		// The old copy is still usable even if saving the new location fails.
		loc.config.Pending = ""
		loc.config.Error = fmt.Sprintf("Could not save the new location: %v. Still using %s.", err, h.Root)
		return h, loc, nil
	}
	loc.config = next
	return home.Home{Root: next.Home}, loc, nil
}

func (l *storageLocation) schedule(h home.Home, destination string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.override {
		return errors.New("YESMAN_HOME sets the folder for this launch; unset it and restart to change the folder in Settings")
	}
	if l.config.Pending != "" {
		return errors.New("a folder move is already scheduled; restart yesman to finish it")
	}
	destination, err := h.CheckDestination(destination)
	if err != nil {
		return err
	}
	// Saving a selector inside the source or destination would make its
	// availability depend on the move it controls.
	for _, root := range []string{h.Root, destination} {
		if rel, err := filepath.Rel(root, l.path); err == nil && filepath.IsLocal(rel) {
			return errors.New("choose a folder outside yesman's application configuration directory")
		}
	}
	next := l.config
	next.Home, next.Pending, next.Error = h.Root, destination, ""
	if err := next.save(l.path); err != nil {
		return fmt.Errorf("save the new location: %w", err)
	}
	l.config = next
	return nil
}

// ChooseStorageFolder uses the platform's folder picker; cancellation keeps
// the input unchanged. The user can also enter a not-yet-created full path.
func (s *service) ChooseStorageFolder() (string, error) {
	a := s.app
	a.wmu.Lock()
	win := a.win
	a.wmu.Unlock()
	paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
		Parent: win, Title: "Choose a new yesman folder", ButtonLabel: "Choose folder",
		DefaultPath: filepath.Dir(a.home.Root), Directory: true, CreateDirectories: true,
	})
	if err != nil || len(paths) == 0 {
		return "", err
	}
	return paths[0], nil
}

// CheckStorageFolder validates an entered path before asking to restart.
func (s *service) CheckStorageFolder(destination string) (string, error) {
	return s.app.home.CheckDestination(destination)
}

// MoveStorage saves the request and relaunches. Graceful quit stops all agents;
// the next process copies their settled files before opening the data again.
func (s *service) MoveStorage(destination string) error {
	if err := s.app.storage.schedule(s.app.home, destination); err != nil {
		return err
	}
	mygo.App.Relaunch()
	return nil
}
