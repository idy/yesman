package main

import (
	"context"
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
		return "", errors.New(tr("enter the full path of the yesman folder"))
	}
	destination = filepath.Clean(destination)
	if info, err := os.Stat(destination); err == nil {
		if !info.IsDir() {
			return "", errors.New(tr("choose a folder, not a file"))
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
		return errors.New(tr("YESMAN_HOME sets the folder for this launch; unset it and restart to change the folder in Settings"))
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
		Parent: win, Title: tr("Choose a yesman folder"), ButtonLabel: tr("Choose folder"),
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

// StorageUsage is the disk the yesman folder takes, by location, and the
// space of the disk it is on. Its parts come in a fixed order, as Settings ›
// Storage lists the locations: repos, yesmen, archive, runtime, then other,
// which holds the secrets, the variables, the settings and anything else.
type StorageUsage struct {
	WorkspaceUsage
	// Disk is the disk the yesman folder is on, when the system says.
	Disk *DiskSpace `json:"disk,omitempty"`
}

// DiskSpace is the size of a disk and how much of it is free to use.
type DiskSpace struct {
	Total int64 `json:"total"`
	Free  int64 `json:"free"`
}

// StorageUsage measures the yesman folder, reusing a measure of the last
// minute unless refresh asks again.
func (s *service) StorageUsage(ctx context.Context, refresh bool) (StorageUsage, error) {
	h := s.app.home
	u, err := s.app.infoCache().measured(ctx, h.Root, refresh, func(ctx context.Context) (WorkspaceUsage, error) {
		return measureStorage(ctx, h)
	})
	if err != nil {
		return StorageUsage{}, err
	}
	v := StorageUsage{WorkspaceUsage: u}
	if d, err := diskSpace(h.Root); err == nil {
		v.Disk = &d
	}
	return v, nil
}

// measureStorage adds up the sizes of the files in the yesman folder by
// location. Runtime and other files show only when there are some.
func measureStorage(ctx context.Context, h home.Home) (WorkspaceUsage, error) {
	root := h.Root
	if real, err := filepath.EvalSymlinks(root); err == nil {
		root = real // the walk does not follow a link, even the root
	}
	var parts []UsagePart
	for _, p := range []struct{ kind, dir string }{
		{"repos", h.Repos()}, {"yesmen", h.Yesmen()}, {"archive", h.Archive()}, {"runtime", h.Runtime()},
	} {
		rel, err := filepath.Rel(h.Root, p.dir)
		if err != nil {
			return WorkspaceUsage{}, err
		}
		parts = append(parts, UsagePart{Kind: p.kind, Path: filepath.ToSlash(rel)})
	}
	u, err := measureTree(ctx, root, parts)
	if err != nil {
		return u, err
	}
	all := u.Parts
	u.Parts = []UsagePart{}
	for _, pt := range all {
		if pt.Files > 0 || (pt.Kind != "runtime" && pt.Kind != "other") {
			u.Parts = append(u.Parts, pt)
		}
	}
	return u, nil
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
