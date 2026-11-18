package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/egoist/mygo"
	"github.com/idy/yesman/internal/crew"
)

// ScorePatchView describes the one profile-level patch, not a snapshot cache.
type ScorePatchView struct {
	Path    string `json:"path"`
	Rows    int    `json:"rows"`
	Enabled bool   `json:"enabled"`
	Error   string `json:"error,omitempty"`
}

func (a *app) scorePatchPath() string { return filepath.Join(a.home.Root, "model-score-patch.csv") }

// initScorePatch runs on launch and lazily in isolated service tests. Invalid
// on-disk data is left alone and reported; no previous installed bundle is read.
func (a *app) initScorePatch() {
	a.scorePatchOnce.Do(func() {
		data, err := readScorePatch(a.scorePatchPath())
		if errors.Is(err, fs.ErrNotExist) {
			return
		}
		if err == nil {
			a.scorePatch, err = crew.ParsePatch(data)
		}
		if err != nil {
			a.scorePatchError = err.Error()
			return
		}
		a.scorePatchData = data
	})
}

func (a *app) effectiveScorePatch() (crew.Patch, ScorePatchView) {
	a.initScorePatch()
	a.setmu.Lock()
	enabled := !a.settings.ScorePatchDisabled
	a.setmu.Unlock()
	a.scorePatchMu.Lock()
	defer a.scorePatchMu.Unlock()
	v := ScorePatchView{Path: a.scorePatchPath(), Rows: len(a.scorePatch), Enabled: enabled, Error: a.scorePatchError}
	if !enabled {
		return nil, v
	}
	return a.scorePatch, v // immutable after validation; updates replace the map
}

// readScorePatch checks the opened descriptor and bounds the read itself.
// External imports are read only, never copied to app storage before validation.
func readScorePatch(path string) ([]byte, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() {
		return nil, fmt.Errorf("choose a regular CSV file, not a link or special file")
	}
	if before.Size() > crew.MaxPatchBytes {
		return nil, fmt.Errorf("CSV exceeds %d bytes", crew.MaxPatchBytes)
	}
	f, err := os.OpenFile(path, os.O_RDONLY|scorePatchNoBlock, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	after, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !after.Mode().IsRegular() || !os.SameFile(before, after) {
		return nil, fmt.Errorf("CSV changed while opening; choose it again")
	}
	data, err := io.ReadAll(io.LimitReader(f, crew.MaxPatchBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > crew.MaxPatchBytes {
		return nil, fmt.Errorf("CSV exceeds %d bytes", crew.MaxPatchBytes)
	}
	return data, nil
}

// writeScorePatch publishes complete validated bytes with the app's existing
// same-directory temporary-file/rename convention. Failure retains last-good.
func writeScorePatch(path string, data []byte) error {
	if fi, err := os.Lstat(path); err == nil && !fi.Mode().IsRegular() {
		return fmt.Errorf("score patch destination must be a regular file")
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".model-score-patch-*.csv")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

func (a *app) importScorePatch(path string) error {
	a.initScorePatch()
	data, err := readScorePatch(path)
	if err != nil {
		return err
	}
	patch, err := crew.ParsePatch(data)
	if err != nil {
		return err
	}
	// Import never rewrites the selected source, even if it is our saved file.
	source, err := os.Stat(path)
	if err != nil {
		return err
	}
	if target, err := os.Stat(a.scorePatchPath()); err == nil && os.SameFile(source, target) {
		return fmt.Errorf("export the current patch and edit that copy before importing")
	}
	a.scorePatchMu.Lock()
	defer a.scorePatchMu.Unlock()
	if err := writeScorePatch(a.scorePatchPath(), data); err != nil {
		return err
	}
	a.scorePatch, a.scorePatchData, a.scorePatchError = patch, data, ""
	return nil
}

func scorePatchFilters() []mygo.FileFilter {
	return []mygo.FileFilter{{Name: "CSV", Extensions: []string{"csv"}}}
}

// ImportScorePatch uses ordinary Settings file authority. Cancel changes nothing.
func (s *service) ImportScorePatch(ctx context.Context) (CrewView, error) {
	dir, _ := os.UserHomeDir()
	paths, err := configOpenDialog(mygo.OpenDialogOptions{Parent: s.app.window(), Title: tr("Import score patch"), ButtonLabel: tr("Choose"), DefaultPath: dir, Filters: scorePatchFilters()})
	if err == nil && len(paths) > 0 {
		err = s.app.importScorePatch(paths[0])
	}
	if err != nil {
		return CrewView{}, err
	}
	return s.app.crewView(ctx, false), nil
}

// ExportScorePatch writes either a blank header-only template or the exact saved
// valid patch bytes. Export does not change the patch or its enabled state.
func (s *service) ExportScorePatch(template bool) (string, error) {
	a := s.app
	a.initScorePatch()
	data := []byte(crew.PatchHeader)
	name := "model-score-template.csv"
	if !template {
		a.scorePatchMu.Lock()
		data = append([]byte(nil), a.scorePatchData...)
		a.scorePatchMu.Unlock()
		if len(data) == 0 {
			return "", fmt.Errorf("no valid score patch to export")
		}
		name = "model-score-patch.csv"
	}
	dir, _ := os.UserHomeDir()
	path, err := configSaveDialog(mygo.SaveDialogOptions{Parent: a.window(), Title: tr("Export score CSV"), ButtonLabel: tr("Export"), DefaultPath: filepath.Join(dir, name), Filters: scorePatchFilters(), CreateDirectories: true})
	if err != nil || path == "" {
		return "", err
	}
	activePath, _ := filepath.Abs(a.scorePatchPath())
	exportPath, _ := filepath.Abs(path)
	if exportPath == activePath {
		return "", fmt.Errorf("export to a separate file; use Import to replace the active patch")
	}
	if target, err := os.Stat(a.scorePatchPath()); err == nil {
		if destination, err := os.Stat(path); err == nil && os.SameFile(target, destination) {
			return "", fmt.Errorf("export to a separate file; use Import to replace the active patch")
		}
	}
	if err := writeScorePatch(path, data); err != nil {
		return "", err
	}
	return path, nil
}

func (s *service) SetScorePatchEnabled(ctx context.Context, enabled bool) (CrewView, error) {
	a := s.app
	a.setmu.Lock()
	next := a.settings
	next.ScorePatchDisabled = !enabled
	err := a.putSettingsLocked(next)
	a.setmu.Unlock()
	if err != nil {
		return CrewView{}, err
	}
	return a.crewView(ctx, false), nil
}
