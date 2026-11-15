package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/idy/yesman/internal/piruntime"
)

func TestAboutManagedRuntimeDoesNotPrepareOrProbe(t *testing.T) {
	r, err := piruntime.New(t.TempDir())
	if err != nil {
		t.Skip(err)
	}
	defer r.Close()
	called := false
	s := &service{app: &app{managedPi: true, piRuntime: r, runPi: func(context.Context, ...string) (string, error) {
		called = true
		return "", errors.New("must not probe")
	}}}
	v := s.About(context.Background())
	if called || v.Pi != "" || v.PiState != "unprepared" || !v.PiManaged {
		t.Fatalf("unprepared runtime reported installed or probed: %+v", v)
	}
	if _, err = os.Stat(r.Directory()); !os.IsNotExist(err) {
		t.Fatal("About created runtime storage")
	}
}

func TestSettingsManagedRuntimeVersionAndSource(t *testing.T) {
	t.Setenv("PI_CODING_AGENT_DIR", t.TempDir())
	root := os.Getenv("YESMAN_TEST_RUNTIME")
	if root == "" {
		t.Skip("prepare managed runtime first")
	}
	r, err := piruntime.New(filepath.Dir(filepath.Dir(root)))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if _, _, err = r.Command(context.Background()); err != nil {
		t.Fatal(err)
	}
	var methods []string
	run := func(_ context.Context, args ...string) (string, error) {
		methods = append(methods, args[0])
		return "", nil
	}
	s := &service{app: &app{managedPi: true, piRuntime: r, pi: r.Description(), runPi: run, piJobs: newPiJobs(run, nil)}}
	about := s.About(context.Background())
	view := s.PiPackages(context.Background())
	if about.Pi != piruntime.Version || about.PiState != "ready" || !about.PiManaged {
		t.Fatalf("About: %+v", about)
	}
	if view.Version != piruntime.Version || view.RuntimeState != "ready" || !view.Managed || !view.PackagesKnown {
		t.Fatalf("Pi settings: %+v", view)
	}
	if len(methods) != 1 || methods[0] != "list" {
		t.Fatalf("extra version/install/global queries: %v", methods)
	}
	if r.Description() != "Managed pi "+piruntime.Version {
		t.Fatal("runtime identity exposes cache digest in product UI")
	}
}
