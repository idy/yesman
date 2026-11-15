// Package piruntime builds the project's pinned pi dependency in its own cache.
// It never installs globally or rewrites the user's pi/configuration. Prepared
// versions are immutable; a different lock/patch creates a different directory.
package piruntime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

//go:embed assets/*
var assets embed.FS

const Version = "1.5.2-yesman.5"
const cli = "node_modules/@earendil-works/pi-coding-agent/dist/cli.js"

type preparation struct {
	done chan struct{}
	err  error // assigned before done closes; each waiter keeps its own attempt
}

type Runtime struct {
	root      string
	node      string
	npm       string
	mu        sync.Mutex
	preparing *preparation
	ready     bool
	lastErr   error
	life      context.Context
	closeLife context.CancelFunc
	// prepareBuild lets tests gate preparation without a registry/network wait.
	// Production always uses prepare, including its integrity and process checks.
	prepareBuild func(context.Context) error
}

// New discovers Node/npm only. Downloading happens on first Command, outside
// the UI thread and Manager locks. An installed cache can run without npm.
func New(parent string) (*Runtime, error) {
	node, err := exec.LookPath("node")
	if err != nil {
		return nil, errors.New("Node.js 22.19 or later is required for yesman's managed pi runtime")
	}
	names, _ := assets.ReadDir("assets")
	h := sha256.New()
	for _, name := range names {
		b, _ := assets.ReadFile("assets/" + name.Name())
		h.Write([]byte(name.Name()))
		h.Write(b)
	}
	root := filepath.Join(parent, "pi-managed", Version+"-"+hex.EncodeToString(h.Sum(nil))[:16])
	npm := findNpm(node)
	life, closeLife := context.WithCancel(context.Background())
	return &Runtime{root: root, node: node, npm: npm, life: life, closeLife: closeLife}, nil
}
func findNpm(node string) string {
	// npm's entry script is portable; execute with Node, never through a shell or
	// a Windows .cmd quoting layer. Resolve the conventional npm shim first.
	if p, err := exec.LookPath("npm"); err == nil {
		if real, err := filepath.EvalSymlinks(p); err == nil && strings.HasSuffix(real, "npm-cli.js") {
			return real
		}
		for _, candidate := range []string{filepath.Join(filepath.Dir(p), "node_modules", "npm", "bin", "npm-cli.js"), filepath.Join(filepath.Dir(p), "..", "lib", "node_modules", "npm", "bin", "npm-cli.js")} {
			if st, err := os.Stat(candidate); err == nil && st.Mode().IsRegular() {
				return candidate
			}
		}
	}
	if real, err := filepath.EvalSymlinks(node); err == nil {
		for _, p := range []string{filepath.Join(filepath.Dir(real), "node_modules", "npm", "bin", "npm-cli.js"), filepath.Join(filepath.Dir(real), "..", "lib", "node_modules", "npm", "bin", "npm-cli.js")} {
			if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() {
				return p
			}
		}
	}
	return ""
}
func (r *Runtime) Directory() string   { return r.root }
func (r *Runtime) Description() string { return "Managed pi " + Version }

// State is observational: it does not install, run pi, or read user settings.
func (r *Runtime) State() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	switch {
	case r.life.Err() != nil:
		return "closed"
	case r.preparing != nil:
		return "preparing"
	case r.ready:
		return "ready"
	case r.lastErr != nil:
		return "failed"
	default:
		return "unprepared"
	}
}

func (r *Runtime) PreparationError() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.preparing == nil && !r.ready && r.lastErr != nil {
		return r.lastErr.Error()
	}
	return ""
}

func (r *Runtime) Close() {
	r.closeLife()
	r.mu.Lock()
	attempt := r.preparing
	r.mu.Unlock()
	if attempt != nil {
		<-attempt.done
	} // includes process-tree join and staging cleanup
}

// Start prepares this digest at most once concurrently. Only the runtime/app
// owns the bounded build lifetime; Settings/agent deadlines only end their wait.
func (r *Runtime) Start() error { _, err := r.start(); return err }
func (r *Runtime) start() (*preparation, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.life.Err(); err != nil {
		return nil, err
	}
	if r.ready {
		return nil, nil
	}
	if r.preparing == nil {
		attempt := &preparation{done: make(chan struct{})}
		r.preparing = attempt
		ctx, cancel := context.WithTimeout(r.life, 10*time.Minute)
		go func() {
			defer cancel()
			prepare := r.prepare
			if r.prepareBuild != nil {
				prepare = r.prepareBuild
			}
			err := prepare(ctx)
			if err == nil {
				err = ctx.Err()
			}
			r.mu.Lock()
			attempt.err, r.lastErr = err, err
			r.ready = err == nil
			r.preparing = nil
			close(attempt.done)
			r.mu.Unlock()
		}()
	}
	return r.preparing, nil
}
func (r *Runtime) Command(ctx context.Context) (string, []string, error) {
	if err := ctx.Err(); err != nil {
		return "", nil, err
	}
	attempt, err := r.start()
	if err != nil {
		return "", nil, err
	}
	if attempt != nil {
		select {
		case <-ctx.Done():
			return "", nil, ctx.Err()
		case <-r.life.Done():
			return "", nil, r.life.Err()
		case <-attempt.done:
		}
		if attempt.err != nil {
			return "", nil, attempt.err
		}
	}
	if err := ctx.Err(); err != nil {
		return "", nil, err
	}
	if err := r.life.Err(); err != nil {
		return "", nil, err
	}
	return r.node, []string{filepath.Join(r.root, filepath.FromSlash(cli))}, nil
}
func digest(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }
func verify(root string) error {
	var patch struct {
		Patches []struct{ Path, After string }
	}
	source, _ := assets.ReadFile("assets/patch.json")
	if err := json.Unmarshal(source, &patch); err != nil {
		return err
	}
	entries, _ := assets.ReadDir("assets")
	for _, entry := range entries {
		want, _ := assets.ReadFile("assets/" + entry.Name())
		got, err := os.ReadFile(filepath.Join(root, entry.Name()))
		if err != nil {
			return err
		}
		if !bytes.Equal(got, want) {
			return fmt.Errorf("managed pi asset mismatch: %s", entry.Name())
		}
	}
	for _, p := range patch.Patches {
		got, err := os.ReadFile(filepath.Join(root, "node_modules", filepath.FromSlash(p.Path)))
		if err != nil {
			return err
		}
		if digest(got) != p.After {
			return fmt.Errorf("managed pi patch integrity mismatch: %s", p.Path)
		}
	}
	want, _ := assets.ReadFile("assets/pending-queue.js")
	got, err := os.ReadFile(filepath.Join(root, "node_modules", "@earendil-works", "pi-agent-core", "dist", "pending-queue.js"))
	if err != nil {
		return err
	}
	if !bytes.Equal(want, got) {
		return errors.New("managed pi queue integrity mismatch")
	}
	var ready struct{ Version, Lock, Patch, Queue string }
	b, err := os.ReadFile(filepath.Join(root, "ready.json"))
	if err != nil {
		return err
	}
	if err = json.Unmarshal(b, &ready); err != nil {
		return err
	}
	lock, _ := assets.ReadFile("assets/package-lock.json")
	if ready.Version != Version || ready.Lock != digest(lock) || ready.Patch != digest(source) || ready.Queue != digest(want) {
		return errors.New("managed pi build manifest mismatch")
	}
	return nil
}
func (r *Runtime) prepare(ctx context.Context) error {
	if _, err := os.Stat(r.root); err == nil {
		// Preserve unexpected/tampered caches for diagnosis instead of overwriting.
		return verify(r.root)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if r.npm == "" {
		return errors.New("npm is required to prepare yesman's managed pi runtime; install Node.js with npm")
	}
	parent := filepath.Dir(r.root)
	if err := os.MkdirAll(parent, 0700); err != nil {
		return err
	}
	staging, err := os.MkdirTemp(parent, ".prepare-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)
	entries, _ := assets.ReadDir("assets")
	for _, entry := range entries {
		b, _ := assets.ReadFile("assets/" + entry.Name())
		if err = os.WriteFile(filepath.Join(staging, entry.Name()), b, 0600); err != nil {
			return err
		}
	}
	installCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(installCtx, r.node, filepath.Join(staging, "install.mjs"))
	isolateInstaller(cmd)
	cmd.Cancel = func() error { killInstaller(cmd); return nil }
	cmd.WaitDelay = 2 * time.Second
	// No user model credentials are needed for dependency installation. npm's
	// normal proxy/certificate configuration still works through the environment.
	cmd.Dir = staging
	cmd.Env = append(os.Environ(), "YESMAN_RUNTIME_NPM="+r.npm, "npm_config_cache="+filepath.Join(parent, "npm-cache"))
	var output bytes.Buffer
	cmd.Stderr = &output
	if err = cmd.Run(); err != nil {
		text := output.String()
		if len(text) > 2000 {
			text = text[len(text)-2000:]
		}
		return fmt.Errorf("prepare managed pi: %w: %s", err, strings.TrimSpace(text))
	}
	if err = verify(staging); err != nil {
		return err
	}
	if err = os.Rename(staging, r.root); err != nil {
		// Another application instance may have completed the same immutable build.
		if verified := verify(r.root); verified != nil {
			return err
		}
	}
	return nil
}

// OfficialNPM reports a recognizable official pi entry. Arbitrary PATH wrappers
// and explicit YESMAN_PI values must keep their original execution semantics.
func OfficialNPM(path string) bool {
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return false
	}
	for _, suffix := range []string{filepath.FromSlash("dist/bundle/cli.js"), filepath.FromSlash("dist/cli.js")} {
		if !strings.HasSuffix(real, string(filepath.Separator)+suffix) {
			continue
		}
		root := strings.TrimSuffix(real, string(filepath.Separator)+suffix)
		b, err := os.ReadFile(filepath.Join(root, "package.json"))
		if err != nil {
			return false
		}
		var p struct{ Name string }
		if json.Unmarshal(b, &p) != nil {
			return false
		}
		return p.Name == "@earendil-works/pi-coding-agent"
	}
	// Recognize npm's exact generated Windows shim, not arbitrary command text.
	// Any extra wrapper command or environment assignment keeps external mode.
	if strings.EqualFold(filepath.Base(path), "pi.cmd") {
		body, err := os.ReadFile(path)
		if err != nil {
			return false
		}
		for _, tail := range []string{"dist/bundle/cli.js", "dist/cli.js"} {
			target := "node_modules/@earendil-works/pi-coding-agent/" + tail
			if strings.ReplaceAll(string(body), "\r\n", "\n") != npmWindowsShim(strings.ReplaceAll(target, "/", "\\")) {
				continue
			}
			root := filepath.Join(filepath.Dir(path), "node_modules", "@earendil-works", "pi-coding-agent")
			data, err := os.ReadFile(filepath.Join(root, "package.json"))
			var pkg struct{ Name string }
			return err == nil && json.Unmarshal(data, &pkg) == nil && pkg.Name == "@earendil-works/pi-coding-agent"
		}
	}
	return false
}

func npmWindowsShim(target string) string {
	return "@ECHO off\nGOTO start\n:find_dp0\nSET dp0=%~dp0\nEXIT /b\n:start\nSETLOCAL\nCALL :find_dp0\n\n" +
		"IF EXIST \"%dp0%\\node.exe\" (\n  SET \"_prog=%dp0%\\node.exe\"\n) ELSE (\n  SET \"_prog=node\"\n  SET PATHEXT=%PATHEXT:;.JS;=;%\n)\n\n" +
		"endLocal & goto #_undefined_# 2>NUL || title %COMSPEC% & \"%_prog%\"  \"%dp0%\\" + target + "\" %*\n"
}
