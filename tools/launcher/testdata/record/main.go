package main

import (
	"encoding/json"
	"os"
	"path/filepath"
)

func main() {
	exe, _ := os.Executable()
	exe, _ = filepath.EvalSymlinks(exe)
	cwd, _ := os.Getwd()
	b, _ := json.Marshal(struct {
		Exe, Cwd, Sentinel string
		Args               []string
		PID                int
	}{exe, cwd, os.Getenv("YESMAN_LAUNCH_SENTINEL"), os.Args[1:], os.Getpid()})
	path := os.Getenv("YESMAN_LAUNCH_REPORT")
	if path == "" && len(os.Args) > 1 {
		path = os.Args[1] // Launch Services fixture: environment is launchd's.
	}
	if err := os.WriteFile(path+".tmp", b, 0o600); err != nil {
		panic(err)
	}
	if err := os.Rename(path+".tmp", path); err != nil {
		panic(err)
	}
}
