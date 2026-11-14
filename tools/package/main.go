// Command package builds the distribution with the pinned MyGo naming adapter.
// It never changes the module cache. See docs/application-name.md.
package main

import (
	"crypto/sha256"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

//go:embed updater_contract.go.txt
var updaterContract []byte

//go:embed upgrade_assets.go.txt
var upgradeAssets []byte

type replacement struct{ Old, New string }
type patch struct {
	File         string
	SHA256       string
	Replacements []replacement
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "package:", err)
		os.Exit(1)
	}
}

func run() error {
	// The adapter applies only to this app and the build command. Do not
	// accidentally adapt another project, mygo dev, or a newer dependency.
	config, err := os.ReadFile("mygo.json")
	if err != nil {
		return err
	}
	var c struct{ Name, Slug, Identifier string }
	if err := json.Unmarshal(config, &c); err != nil {
		return err
	}
	if c.Name != "Yes Man" || c.Slug != "yesman" || c.Identifier != "dev.idy.yesman" {
		return fmt.Errorf("unexpected application identity")
	}
	cmd := exec.Command("go", "list", "-m", "-json", "github.com/egoist/mygo")
	data, err := cmd.Output()
	if err != nil {
		return err
	}
	var mod struct {
		Dir, Version string
		Replace      any
	}
	if err := json.Unmarshal(data, &mod); err != nil {
		return err
	}
	if mod.Version != "v0.9.0" || mod.Replace != nil {
		return fmt.Errorf("adapter requires unmodified MyGo v0.9.0")
	}
	if mod.Dir == "" {
		// A clean hosted runner may know the module version before its
		// source has been downloaded. Download only this pinned module.
		data, err = exec.Command("go", "mod", "download", "-json", "github.com/egoist/mygo").Output()
		if err != nil {
			return err
		}
		if err := json.Unmarshal(data, &mod); err != nil {
			return err
		}
		if mod.Dir == "" || mod.Version != "v0.9.0" || mod.Replace != nil {
			return fmt.Errorf("could not resolve pinned MyGo source")
		}
	}
	work, err := os.MkdirTemp("", "yesman-package-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)
	// Go refuses overlays beneath GOMODCACHE. Stage the pinned dependency
	// in a private temporary directory instead; neither go.mod in the repo
	// nor the shared module cache is edited.
	staged := filepath.Join(work, "mygo")
	if err := os.CopyFS(staged, os.DirFS(mod.Dir)); err != nil {
		return err
	}
	for _, p := range patches {
		original := filepath.Join(mod.Dir, filepath.FromSlash(p.File))
		b, err := os.ReadFile(original)
		if err != nil {
			return err
		}
		adapted, err := apply(p, b)
		if err != nil {
			return err
		}
		dst := filepath.Join(staged, filepath.FromSlash(p.File))
		if err := os.WriteFile(dst, adapted, 0o644); err != nil {
			return err
		}
	}
	modfile := filepath.Join(work, "go.mod")
	for _, name := range []string{"go.mod", "go.sum"} {
		b, err := os.ReadFile(name)
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(work, name), b, 0o600); err != nil {
			return err
		}
	}
	cmd = exec.Command("go", "mod", "edit", "-modfile="+modfile, "-replace=github.com/egoist/mygo="+staged)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("staging module: %w: %s", err, out)
	}
	flags := os.Getenv("GOFLAGS")
	if strings.Contains(flags, "overlay") || strings.Contains(flags, "modfile") {
		return fmt.Errorf("GOFLAGS must not supply another overlay or modfile")
	}
	cmd = exec.Command("go", append([]string{"run", "github.com/egoist/mygo/cmd/mygo", "build"}, os.Args[1:]...)...)
	if len(os.Args) == 2 && os.Args[1] == "-verify-updater" {
		if err := os.WriteFile(filepath.Join(staged, "yesman_name_test.go"), updaterContract, 0o644); err != nil {
			return err
		}
		cmd = exec.Command("go", "test", "github.com/egoist/mygo", "-run", "^Test(YesmanRename|Updater|SwapFiles|Relaunch)", "-count=1", "-v", "-timeout=5m")
	}
	if len(os.Args) == 3 && os.Args[1] == "-upgrade-assets" {
		dir := filepath.Join(staged, "cmd", "yesman-upgrade-assets")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, "main.go"), upgradeAssets, 0o600); err != nil {
			return err
		}
		cmd = exec.Command("go", "run", "github.com/egoist/mygo/cmd/yesman-upgrade-assets", os.Args[2])
	}
	// GOFLAGS propagates the same staged module to the CLI and app build.
	if strings.ContainsAny(modfile, "\"'\n\r") {
		return fmt.Errorf("unsupported temporary directory: %s", work)
	}
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	cmd.Env = append(os.Environ(), "YESMAN_PACKAGE_SOURCE="+root, "GOFLAGS="+flags+` "-modfile=`+modfile+`"`)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}

func apply(p patch, src []byte) ([]byte, error) {
	if fmt.Sprintf("%x", sha256.Sum256(src)) != p.SHA256 {
		return nil, fmt.Errorf("MyGo %s has changed; review the adapter before building", p.File)
	}
	s := string(src)
	for _, r := range p.Replacements {
		if strings.Count(s, r.Old) != 1 {
			return nil, fmt.Errorf("MyGo %s: replacement must match once", p.File)
		}
		s = strings.Replace(s, r.Old, r.New, 1)
	}
	return []byte(s), nil
}
