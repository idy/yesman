package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/egoist/mygo"
	"github.com/idy/yesman/internal/home"
)

// The location lives outside the data it locates. YESMAN_HOME remains an
// explicit override; normal launches remember the user's choice here.
type storageConfig struct {
	Home string `json:"home"`
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

func openStorage() (home.Home, *storageLocation, error) {
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
	return loadStorage(filepath.Join(dir, "yesman", "storage.json"), h.Root)
}

func loadStorage(path, fallback string) (home.Home, *storageLocation, error) {
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
	return home.Home{Root: loc.config.Home}, loc, nil
}

func checkStorageFolder(destination string) (string, error) {
	destination = strings.TrimSpace(destination)
	if !filepath.IsAbs(destination) {
		return "", errors.New("enter the full path of the yesman folder")
	}
	destination = filepath.Clean(destination)
	if info, err := os.Stat(destination); err == nil {
		if !info.IsDir() {
			return "", errors.New("choose a folder, not a file")
		}
		return filepath.EvalSymlinks(destination)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	return destination, nil
}

func (l *storageLocation) setHome(destination string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.override {
		return errors.New("YESMAN_HOME sets the folder for this launch; unset it and restart to change the folder in Settings")
	}
	destination, err := checkStorageFolder(destination)
	if err != nil {
		return err
	}
	// Existing data is used as-is; only missing standard directories are made.
	h := home.Home{Root: destination}
	if err := h.Ensure(); err != nil {
		return fmt.Errorf("prepare the yesman folder: %w", err)
	}
	destination, err = filepath.EvalSymlinks(destination)
	if err != nil {
		return err
	}
	for _, dir := range []string{h.Root, h.Repos(), h.Yesmen(), h.Archive(), h.Runtime()} {
		probe, err := os.CreateTemp(dir, ".write-check-*")
		if err != nil {
			return fmt.Errorf("the yesman folder is not writable at %s: %w", dir, err)
		}
		if err := errors.Join(probe.Close(), os.Remove(probe.Name())); err != nil {
			return fmt.Errorf("check write access to %s: %w", dir, err)
		}
	}
	next := storageConfig{Home: destination}
	if err := next.save(l.path); err != nil {
		return fmt.Errorf("save the location: %w", err)
	}
	l.config = next
	return nil
}

// ChooseStorageFolder uses the platform's folder picker; cancellation leaves
// the input unchanged. Existing folders may already contain the user's data.
func (s *service) ChooseStorageFolder() (string, error) {
	a := s.app
	a.wmu.Lock()
	win := a.win
	a.wmu.Unlock()
	paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
		Parent: win, Title: "Choose a yesman folder", ButtonLabel: "Choose folder",
		DefaultPath: filepath.Dir(a.home.Root), Directory: true, CreateDirectories: true,
	})
	if err != nil || len(paths) == 0 {
		return "", err
	}
	return paths[0], nil
}

func (s *service) CheckStorageFolder(destination string) (string, error) {
	return checkStorageFolder(destination)
}

// SetStorageFolder remembers the selected folder and restarts to open it.
// The app does not copy, move or delete existing data in either folder.
func (s *service) SetStorageFolder(destination string) error {
	if err := s.app.storage.setHome(destination); err != nil {
		return err
	}
	mygo.App.Relaunch()
	return nil
}
