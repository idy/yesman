package main

import (
	"errors"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows/registry"
)

// refreshInstalledAppName covers automatic archive updates, which do not run
// NSIS. Only an installer registration for this exact directory, still using
// the old standard label, is changed. Portable copies and custom labels stay.
func refreshInstalledAppName() error {
	const path = `Software\Microsoft\Windows\CurrentVersion\Uninstall\dev.idy.yesman`
	key, err := registry.OpenKey(registry.CURRENT_USER, path, registry.QUERY_VALUE|registry.SET_VALUE)
	if errors.Is(err, registry.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer key.Close()
	label, _, err := key.GetStringValue("DisplayName")
	if err != nil || label != "yesman" {
		return nil
	}
	location, _, err := key.GetStringValue("InstallLocation")
	if err != nil || location == "" {
		return nil
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if filepath.Base(exe) != appDisplayName+".exe" {
		return nil
	}
	installed, err := os.Stat(location)
	if err != nil {
		return nil
	}
	current, err := os.Stat(filepath.Dir(exe))
	if err != nil || !os.SameFile(installed, current) {
		return nil
	}
	if err := key.SetStringValue("DisplayIcon", `"`+exe+`"`); err != nil {
		return err
	}
	return key.SetStringValue("DisplayName", appDisplayName)
}
