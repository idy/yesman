package agent

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// Workspace temporary storage. Every pi process yesman starts gets, inside
// its workspace, a scratch directory and an evidence directory of its own,
// and the workspace's shared build caches, set in its environment before it
// starts:
//
//	.yesman/tmp/_agent/<name>/<incarnation>/       TMPDIR, TMP, TEMP, GOTMPDIR, YESMAN_TMP
//	.yesman/evidence/_agent/<name>/<incarnation>/  YESMAN_EVIDENCE
//	.yesman/tmp/_cache/go-build/                    GOCACHE
//	.yesman/tmp/_cache/go-mod/                      GOMODCACHE
//	.yesman/tmp/_cache/npm/                         npm_config_cache
//
// The caches are shared by the workspace's agents and kept across runs: the
// Go build and module caches and npm's cache are made for concurrent use by
// several processes. Nothing here deletes them, or any directory, except a
// reservation whose process never started.
//
// These are default locations, not a sandbox: a command can still name
// another path or set the variables again. HOME, USERPROFILE, XDG config
// and every credential and configuration location are left alone.
//
// The directories are made here, without the Manager's lock, checking each
// component: a link, or anything but a directory, refuses the start rather
// than letting programs fall back to the system's temporary directory. An
// incarnation directory is created exclusively, never reused, and its
// identity is kept with the process: the app's managed write entry writes
// to it only while it is still that directory.

const (
	tempRoot     = ".yesman/tmp"
	evidenceRoot = ".yesman/evidence"
	cacheRoot    = ".yesman/tmp/_cache"
	agentDirs    = "_agent"
)

// tempEnvKeys are the variables the scratch directory sets. Go reads TMPDIR
// on Unix and TMP, then TEMP, on Windows; Node TMPDIR on Unix and TEMP, then
// TMP, on Windows; Python TMPDIR, TEMP, TMP; go build uses GOTMPDIR.
var tempEnvKeys = []string{"TMPDIR", "TMP", "TEMP", "GOTMPDIR"}

// ManagedEnvKeys are all the variables an agent's start sets here. The
// extension's shell applies the same names after the user's variables
// (pi/yesman.ts shellEnvironment); keep the two lists in step.
var ManagedEnvKeys = append(append([]string{}, tempEnvKeys...),
	"GOCACHE", "GOMODCACHE", "npm_config_cache",
	"YESMAN_TMP", "YESMAN_EVIDENCE", "YESMAN_TMP_ROOT", "YESMAN_EVIDENCE_ROOT", "YESMAN_CACHE")

// TempDir is a directory reserved for one process: its path below the
// workspace, slash-separated, and its identity when it was created.
type TempDir struct {
	Rel  string
	Info fs.FileInfo
}

// agentTemp is what one start of an agent reserved.
type agentTemp struct {
	workspace         string
	scratch, evidence TempDir
}

// tempOwner is written into each reserved directory for people and later
// tools to see whose it is. It is a record only, never an authorization.
type tempOwner struct {
	Agent       string    `json:"agent"`
	Name        string    `json:"name"`
	Role        string    `json:"role"`
	Incarnation string    `json:"incarnation"`
	Created     time.Time `json:"created"`
}

// reserveAgentTemp makes the directories of one start of an agent.
func reserveAgentTemp(workspace, agentID, name, role string) (*agentTemp, error) {
	if !plainName(name) {
		return nil, fmt.Errorf("agent name %q cannot name a directory", name)
	}
	root, err := os.OpenRoot(workspace)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	if err := realDir(root, ".yesman"); err != nil {
		return nil, err
	}
	for _, rel := range []string{
		tempRoot, path.Join(tempRoot, agentDirs), path.Join(tempRoot, agentDirs, name),
		evidenceRoot, path.Join(evidenceRoot, agentDirs), path.Join(evidenceRoot, agentDirs, name),
		cacheRoot, path.Join(cacheRoot, "go-build"), path.Join(cacheRoot, "go-mod"), path.Join(cacheRoot, "npm"),
	} {
		if err := ensureDir(root, rel); err != nil {
			return nil, err
		}
	}
	var random [4]byte
	if _, err := rand.Read(random[:]); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	incarnation := now.Format("20060102T150405Z") + "-" + hex.EncodeToString(random[:])
	data, _ := json.MarshalIndent(tempOwner{Agent: agentID, Name: name, Role: role, Incarnation: incarnation, Created: now}, "", "  ")
	t := &agentTemp{workspace: workspace}
	for _, d := range []*TempDir{&t.scratch, &t.evidence} {
		base := tempRoot
		if d == &t.evidence {
			base = evidenceRoot
		}
		rel := path.Join(base, agentDirs, name, incarnation)
		// Mkdir, not MkdirAll: an existing directory is not ours.
		if err := root.Mkdir(rel, 0o700); err != nil {
			t.release()
			return nil, fmt.Errorf("reserve %s: %w", rel, err)
		}
		info, err := root.Lstat(rel)
		if err != nil || !info.IsDir() || info.Mode()&fs.ModeSymlink != 0 {
			t.release()
			return nil, fmt.Errorf("reserve %s: not a directory", rel)
		}
		*d = TempDir{Rel: rel, Info: info}
		if err := writeNew(root, path.Join(rel, "owner.json"), append(data, '\n')); err != nil {
			t.release()
			return nil, fmt.Errorf("reserve %s: %w", rel, err)
		}
	}
	return t, nil
}

func (t *agentTemp) abs(rel string) string {
	return filepath.Join(t.workspace, filepath.FromSlash(rel))
}

// env is the agent's environment for the reservation.
func (t *agentTemp) env() []string {
	scratch := t.abs(t.scratch.Rel)
	var env []string
	for _, key := range tempEnvKeys {
		env = append(env, key+"="+scratch)
	}
	return append(env,
		"GOCACHE="+t.abs(path.Join(cacheRoot, "go-build")),
		"GOMODCACHE="+t.abs(path.Join(cacheRoot, "go-mod")),
		"npm_config_cache="+t.abs(path.Join(cacheRoot, "npm")),
		"YESMAN_TMP="+scratch,
		"YESMAN_EVIDENCE="+t.abs(t.evidence.Rel),
		"YESMAN_TMP_ROOT="+t.abs(tempRoot),
		"YESMAN_EVIDENCE_ROOT="+t.abs(evidenceRoot),
		"YESMAN_CACHE="+t.abs(cacheRoot),
	)
}

// release removes the reservation of a process that never ran, each
// directory only if it is still the one created.
func (t *agentTemp) release() {
	root, err := os.OpenRoot(t.workspace)
	if err != nil {
		return
	}
	defer root.Close()
	for _, d := range []TempDir{t.scratch, t.evidence} {
		if d.Info == nil {
			continue
		}
		if now, err := root.Lstat(d.Rel); err == nil && os.SameFile(now, d.Info) {
			_ = root.RemoveAll(d.Rel)
		}
	}
}

// ProcessTemp returns the directories reserved for the process token was
// given to, while that credential is current.
func (m *Manager) ProcessTemp(token string) (scratch, evidence TempDir, ok bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id, found := m.tokens[token]
	a := m.agents[id]
	if !found || token == "" || a == nil || a.token != token || a.tokenProc == nil || a.tokenProc.temp == nil {
		return TempDir{}, TempDir{}, false
	}
	return a.tokenProc.temp.scratch, a.tokenProc.temp.evidence, true
}

// withEnv replaces every variable of set in env, whatever its case (npm
// reads npm_config_* in either case; Windows does not tell cases apart and
// Go keeps the last of a key, Node the first), then adds set's values.
func withEnv(env, set []string) []string {
	keys := map[string]bool{}
	for _, kv := range set {
		k, _, _ := strings.Cut(kv, "=")
		keys[strings.ToUpper(k)] = true
	}
	out := make([]string, 0, len(env)+len(set))
	for _, kv := range env {
		if k, _, ok := strings.Cut(kv, "="); ok && keys[strings.ToUpper(k)] {
			continue
		}
		out = append(out, kv)
	}
	return append(out, set...)
}

func plainName(name string) bool {
	return name != "" && name != "." && name != ".." && !strings.ContainsAny(name, `/\:`) && filepath.Base(name) == name
}

// realDir requires rel to be a directory and not a link.
func realDir(root *os.Root, rel string) error {
	info, err := root.Lstat(rel)
	if err != nil {
		return err
	}
	if info.Mode()&fs.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("%s is not a plain directory", rel)
	}
	return nil
}

// ensureDir makes rel when missing; its parent was checked before it.
func ensureDir(root *os.Root, rel string) error {
	if err := root.Mkdir(rel, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return err
	}
	return realDir(root, rel)
}

func writeNew(root *os.Root, name string, data []byte) error {
	f, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	return errors.Join(err, f.Close())
}
