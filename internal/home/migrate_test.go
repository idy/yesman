package home

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func migrationWrite(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func migrationRead(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func migrationGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func migrationHome(t *testing.T) (Home, string) {
	t.Helper()
	tmp, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h := Home{Root: filepath.Join(tmp, "source")}
	if err := h.Ensure(); err != nil {
		t.Fatal(err)
	}
	return h, tmp
}

func TestCopyToPreservesDataAndRepairsWorktrees(t *testing.T) {
	h, tmp := migrationHome(t)
	origin := filepath.Join(tmp, "origin")
	migrationWrite(t, filepath.Join(origin, "README.md"), "original\n")
	migrationWrite(t, filepath.Join(origin, ".gitignore"), "ignored.file\n")
	migrationGit(t, origin, "init", "--quiet", "--initial-branch=main")
	migrationGit(t, origin, "add", ".")
	migrationGit(t, origin, "commit", "--quiet", "-m", "initial")
	repo := filepath.Join(h.Repos(), "local", "demo.git")
	migrationGit(t, tmp, "clone", "--bare", "--quiet", "--no-hardlinks", origin, repo)
	active := filepath.Join(h.Yesmen(), "active", "code")
	archive := filepath.Join(h.Archive(), "previous", "code")
	gone := filepath.Join(h.Yesmen(), "gone", "code")
	for _, wt := range []struct{ path, branch string }{{active, "active"}, {archive, "archived"}, {gone, "gone"}} {
		migrationGit(t, tmp, "--git-dir", repo, "worktree", "add", "--quiet", "--no-track", "-b", wt.branch, wt.path, "main")
	}
	if err := os.RemoveAll(filepath.Dir(gone)); err != nil {
		t.Fatal(err)
	}
	migrationWrite(t, filepath.Join(active, "README.md"), "dirty file mentioning "+h.Root+"\n")
	migrationWrite(t, filepath.Join(active, "untracked.txt"), "keep untracked\n")
	migrationWrite(t, filepath.Join(active, "ignored.file"), "keep ignored\n")
	migrationWrite(t, filepath.Join(archive, "archived.txt"), "keep archived\n")
	migrationWrite(t, h.Settings(), "{\"appearance\":\"dark\"}\n")
	if err := os.Chmod(h.Settings(), 0o640); err != nil {
		t.Fatal(err)
	}
	session := filepath.Join(h.Yesmen(), "active", ".yesman", "sessions", "lead", "recent.jsonl")
	header, err := json.Marshal(map[string]any{"type": "session", "version": 3, "id": "test", "cwd": filepath.Dir(active), "parentSession": session, "custom": true})
	if err != nil {
		t.Fatal(err)
	}
	body := "{\"type\":\"message\",\"message\":{\"text\":\"" + filepath.ToSlash(h.Root) + " must stay in history\"}}\n"
	originalSession := string(header) + "\n" + body
	migrationWrite(t, session, originalSession)
	archivedSession := filepath.Join(h.Archive(), "previous", ".yesman", "sessions", "worker", "old.jsonl")
	archivedHeader, _ := json.Marshal(map[string]any{"type": "session", "cwd": filepath.Dir(archive)})
	migrationWrite(t, archivedSession, string(archivedHeader)+"\n")
	mtime := time.Unix(1_700_000_000, 0)
	if err := os.Chtimes(session, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	// Read-only metadata and directories must still be repairable in staging.
	if err := os.Chmod(session, 0o444); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Dir(session), 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Chmod(filepath.Dir(session), 0o755)
		_ = os.Chmod(session, 0o644)
	})
	beforeStatus := migrationGit(t, active, "status", "--porcelain")
	oldGitPointer := migrationRead(t, filepath.Join(active, ".git"))
	destination := filepath.Join(tmp, "new", "home")
	if err := os.MkdirAll(destination, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := h.CopyTo(context.Background(), destination); err != nil {
		t.Fatal(err)
	}
	newHome := Home{Root: destination}
	newActive := filepath.Join(newHome.Yesmen(), "active", "code")
	newArchive := filepath.Join(newHome.Archive(), "previous", "code")
	if got := migrationGit(t, newActive, "status", "--porcelain"); got != beforeStatus {
		t.Fatalf("copied working tree status = %q, want %q", got, beforeStatus)
	}
	if got := migrationGit(t, active, "status", "--porcelain"); got != beforeStatus {
		t.Fatalf("original working tree status changed: %q", got)
	}
	if got := migrationRead(t, filepath.Join(active, ".git")); got != oldGitPointer {
		t.Fatal("original worktree pointer changed")
	}
	for _, root := range []string{h.Root, newHome.Root} {
		r := filepath.Join(root, "repos", "local", "demo.git")
		list := migrationGit(t, tmp, "--git-dir", r, "worktree", "list", "--porcelain")
		for _, suffix := range []string{filepath.Join("yesmen", "active", "code"), filepath.Join("archive", "previous", "code"), filepath.Join("yesmen", "gone", "code")} {
			if !strings.Contains(list, filepath.ToSlash(filepath.Join(root, suffix))) && !strings.Contains(list, filepath.Join(root, suffix)) {
				t.Errorf("pool worktree list does not contain %s:\n%s", suffix, list)
			}
		}
		other := h.Root
		if root == h.Root {
			other = newHome.Root
		}
		if strings.Contains(list, other) {
			t.Errorf("pool at %s still points into %s:\n%s", root, other, list)
		}
	}
	if got := migrationGit(t, newArchive, "branch", "--show-current"); got != "archived" {
		t.Fatalf("archived worktree does not work: %q", got)
	}
	if got := migrationRead(t, filepath.Join(newActive, "ignored.file")); got != "keep ignored\n" {
		t.Errorf("ignored file = %q", got)
	}
	if got := migrationRead(t, filepath.Join(newActive, "README.md")); got != "dirty file mentioning "+h.Root+"\n" {
		t.Error("user file contents were rewritten")
	}
	if got := migrationRead(t, newHome.Settings()); got != migrationRead(t, h.Settings()) {
		t.Error("settings changed during copying")
	}
	newSession := filepath.Join(newHome.Yesmen(), "active", ".yesman", "sessions", "lead", "recent.jsonl")
	t.Cleanup(func() {
		_ = os.Chmod(filepath.Dir(newSession), 0o755)
		_ = os.Chmod(newSession, 0o644)
	})
	line, history, _ := strings.Cut(migrationRead(t, newSession), "\n")
	var gotHeader map[string]any
	if err := json.Unmarshal([]byte(line), &gotHeader); err != nil {
		t.Fatal(err)
	}
	if gotHeader["cwd"] != filepath.Dir(newActive) || gotHeader["parentSession"] != newSession || gotHeader["custom"] != true {
		t.Errorf("copied session header = %#v", gotHeader)
	}
	if history != body || migrationRead(t, session) != originalSession {
		t.Error("history or original session was changed")
	}
	st, err := os.Stat(newSession)
	if err != nil || !st.ModTime().Equal(mtime) {
		t.Errorf("session mtime changed: %v, %v", st, err)
	}
	if runtime.GOOS != "windows" {
		settingsStat, err := os.Stat(newHome.Settings())
		if err != nil || settingsStat.Mode().Perm() != 0o640 || st.Mode().Perm() != 0o444 {
			t.Errorf("copied permissions changed: settings=%v, session=%v, err=%v", settingsStat, st, err)
		}
	}
	newArchivedSession := filepath.Join(newHome.Archive(), "previous", ".yesman", "sessions", "worker", "old.jsonl")
	if err := json.Unmarshal([]byte(migrationRead(t, newArchivedSession)), &gotHeader); err != nil {
		t.Fatal(err)
	}
	if gotHeader["cwd"] != filepath.Dir(newArchive) {
		t.Error("archived session cwd was not relocated")
	}
	migrationWrite(t, filepath.Join(newActive, "untracked.txt"), "new copy only\n")
	if got := migrationRead(t, filepath.Join(active, "untracked.txt")); got != "keep untracked\n" {
		t.Error("copy shares writable files with the backup")
	}
}

func TestCheckDestination(t *testing.T) {
	h, tmp := migrationHome(t)
	occupied := filepath.Join(tmp, "occupied")
	migrationWrite(t, filepath.Join(occupied, "keep"), "do not overwrite")
	file := filepath.Join(tmp, "file")
	migrationWrite(t, file, "not a directory")
	for name, destination := range map[string]string{
		"empty": "", "relative": "another", "same": h.Root,
		"descendant": filepath.Join(h.Root, "new", "home"), "ancestor": tmp,
		"occupied": occupied, "file": file,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := h.CheckDestination(destination); err == nil {
				t.Errorf("accepted invalid destination %q", destination)
			}
		})
	}
	destination := filepath.Join(tmp, "not-yet", "new-home")
	if got, err := h.CheckDestination(destination); err != nil || got != destination {
		t.Fatalf("new destination = %q, %v", got, err)
	}
	if _, err := os.Stat(filepath.Dir(destination)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("preflight created a directory: %v", err)
	}
	if _, err := (Home{Root: filepath.Join(tmp, "missing")}).CheckDestination(destination); err == nil {
		t.Error("accepted a missing source")
	}
	caseAlias := filepath.Join(tmp, strings.ToUpper(filepath.Base(h.Root)))
	if a, err := os.Stat(caseAlias); err == nil {
		b, _ := os.Stat(h.Root)
		if os.SameFile(a, b) {
			if _, err := h.CheckDestination(filepath.Join(caseAlias, "nested")); err == nil {
				t.Error("accepted source descendant through case alias")
			}
		}
	}
	if migrationRead(t, filepath.Join(occupied, "keep")) != "do not overwrite" {
		t.Error("preflight changed occupied destination")
	}
}

func TestCopyToSymlinks(t *testing.T) {
	h, tmp := migrationHome(t)
	outside := filepath.Join(tmp, "outside")
	migrationWrite(t, filepath.Join(outside, "keep"), "external data")
	migrationWrite(t, filepath.Join(h.Root, "data"), "internal data")
	links := map[string]string{
		"relative": "data", "absolute": filepath.Join(h.Root, "data"),
		"outside": outside, "broken": filepath.Join(tmp, "missing"),
		"cycle": filepath.Join(h.Root, "cycle"),
	}
	for name, target := range links {
		if err := os.Symlink(target, filepath.Join(h.Root, name)); err != nil {
			if runtime.GOOS == "windows" {
				t.Skipf("symbolic links unavailable: %v", err)
			}
			t.Fatal(err)
		}
	}
	alias := filepath.Join(tmp, "source-alias")
	if err := os.Symlink(h.Root, alias); err != nil {
		t.Fatal(err)
	}
	for _, dest := range []string{alias, filepath.Join(alias, "child", "home")} {
		if _, err := h.CheckDestination(dest); err == nil {
			t.Fatalf("accepted overlapping symlink destination %q", dest)
		}
	}
	destination := filepath.Join(tmp, "destination")
	if err := h.CopyTo(context.Background(), destination); err != nil {
		t.Fatal(err)
	}
	for name, oldTarget := range links {
		target, err := os.Readlink(filepath.Join(destination, name))
		if err != nil {
			t.Fatal(err)
		}
		want := oldTarget
		if name == "absolute" || name == "cycle" {
			want = strings.Replace(oldTarget, h.Root, destination, 1)
		}
		if target != want {
			t.Errorf("copied %s link = %q, want %q", name, target, want)
		}
		if original, err := os.Readlink(filepath.Join(h.Root, name)); err != nil || original != oldTarget {
			t.Errorf("source link %s changed: %q, %v", name, original, err)
		}
	}
	if got := migrationRead(t, filepath.Join(destination, "absolute")); got != "internal data" {
		t.Errorf("relocated absolute link = %q", got)
	}
	if got := migrationRead(t, filepath.Join(outside, "keep")); got != "external data" {
		t.Error("external link target changed")
	}
	// A source-root alias in a persisted path must also map to the new home.
	if got, moved, err := relocatePath(filepath.Join(alias, "data"), h.Root, destination); err != nil || !moved || got != filepath.Join(destination, "data") {
		t.Errorf("aliased path = %q, %v, %v", got, moved, err)
	}
}

func TestCopyToFailureLeavesSourceAndDestination(t *testing.T) {
	h, tmp := migrationHome(t)
	destination := filepath.Join(tmp, "destination")
	if err := os.Mkdir(destination, 0o755); err != nil {
		t.Fatal(err)
	}
	migrationWrite(t, h.Settings(), "original settings")
	// An externally registered checkout cannot be migrated with this home.
	// Fail after the entire copy, while keeping both originals untouched.
	record := filepath.Join(h.Repos(), "local", "demo.git", "worktrees", "outside", "gitdir")
	outsideGit := filepath.Join(tmp, "external", ".git")
	migrationWrite(t, filepath.Join(filepath.Dir(filepath.Dir(filepath.Dir(record))), "HEAD"), "ref: refs/heads/main\n")
	migrationWrite(t, record, outsideGit+"\n")
	migrationWrite(t, outsideGit, "external checkout remains unchanged\n")
	if err := h.CopyTo(context.Background(), destination); err == nil {
		t.Fatal("expected external worktree migration to fail")
	}
	entries, err := os.ReadDir(destination)
	if err != nil || len(entries) != 0 {
		t.Errorf("empty destination was changed: %v, %v", entries, err)
	}
	if migrationRead(t, record) != outsideGit+"\n" || migrationRead(t, outsideGit) != "external checkout remains unchanged\n" || migrationRead(t, h.Settings()) != "original settings" {
		t.Error("failure changed original files")
	}
	staging, err := filepath.Glob(filepath.Join(tmp, ".yesman-move-*"))
	if err != nil || len(staging) != 0 {
		t.Errorf("staging directories left behind: %v, %v", staging, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := h.CopyTo(ctx, filepath.Join(tmp, "canceled")); !errors.Is(err, context.Canceled) {
		t.Errorf("canceled copy = %v", err)
	}
	if _, err := os.Stat(filepath.Join(tmp, "canceled")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("canceled copy created destination: %v", err)
	}
}

func TestCopyToDoesNotFollowSymlinkedWorktreeMetadata(t *testing.T) {
	h, tmp := migrationHome(t)
	repo := filepath.Join(h.Repos(), "local", "demo.git")
	migrationWrite(t, filepath.Join(repo, "HEAD"), "ref: refs/heads/main\n")
	externalRecord := filepath.Join(tmp, "external-records", "record", "gitdir")
	gitFile := filepath.Join(h.Yesmen(), "active", "code", ".git")
	migrationWrite(t, externalRecord, gitFile+"\n")
	migrationWrite(t, gitFile, "gitdir: "+filepath.Join(repo, "worktrees", "record")+"\n")
	if err := os.Symlink(filepath.Dir(filepath.Dir(externalRecord)), filepath.Join(repo, "worktrees")); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("symbolic links unavailable: %v", err)
		}
		t.Fatal(err)
	}
	destination := filepath.Join(tmp, "destination")
	if err := h.CopyTo(context.Background(), destination); err == nil {
		t.Fatal("accepted symbolic links in writable Git metadata")
	}
	if migrationRead(t, externalRecord) != gitFile+"\n" {
		t.Fatal("external metadata was changed")
	}
	if _, err := os.Stat(destination); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("failed copy published the destination: %v", err)
	}
}

func TestCopyToWithSubmodule(t *testing.T) {
	h, tmp := migrationHome(t)
	child := filepath.Join(tmp, "child-origin")
	origin := filepath.Join(tmp, "origin")
	for _, dir := range []string{child, origin} {
		migrationWrite(t, filepath.Join(dir, "README.md"), "original\n")
		migrationGit(t, dir, "init", "--quiet", "--initial-branch=main")
		migrationGit(t, dir, "add", ".")
		migrationGit(t, dir, "commit", "--quiet", "-m", "initial")
	}
	migrationGit(t, origin, "-c", "protocol.file.allow=always", "submodule", "add", "--quiet", child, "deps/child")
	migrationGit(t, origin, "commit", "--quiet", "-am", "submodule")
	repo := filepath.Join(h.Repos(), "local", "demo.git")
	migrationGit(t, tmp, "clone", "--bare", "--quiet", "--no-hardlinks", origin, repo)
	worktree := filepath.Join(h.Yesmen(), "active", "code")
	migrationGit(t, tmp, "--git-dir", repo, "worktree", "add", "--quiet", "--no-track", "-b", "active", worktree, "main")
	migrationGit(t, worktree, "-c", "protocol.file.allow=always", "submodule", "update", "--init", "--quiet")
	childTree := filepath.Join(worktree, "deps", "child")
	// Exercise the absolute form even if this Git version writes relative links.
	childGitDir := migrationGit(t, childTree, "rev-parse", "--absolute-git-dir")
	migrationWrite(t, filepath.Join(childTree, ".git"), "gitdir: "+childGitDir+"\n")
	migrationWrite(t, filepath.Join(childTree, "README.md"), "dirty submodule\n")
	destination := filepath.Join(tmp, "destination")
	if err := h.CopyTo(context.Background(), destination); err != nil {
		t.Fatal(err)
	}
	newChild := filepath.Join(destination, "yesmen", "active", "code", "deps", "child")
	if got := migrationGit(t, newChild, "status", "--porcelain"); got != "M README.md" {
		t.Errorf("copied submodule status = %q", got)
	}
	if got := migrationGit(t, newChild, "rev-parse", "--absolute-git-dir"); !strings.Contains(filepath.ToSlash(got), filepath.ToSlash(destination)) {
		t.Errorf("copied submodule still depends on the source: %s", got)
	}
	if got := migrationGit(t, childTree, "rev-parse", "--absolute-git-dir"); filepath.ToSlash(got) != filepath.ToSlash(childGitDir) {
		t.Errorf("original submodule gitdir changed: %q", got)
	}
}

func TestCopyToPreservesUnknownOrMalformedSessionHistory(t *testing.T) {
	h, tmp := migrationHome(t)
	logs := map[string]string{
		"empty": "", "malformed": "{unfinished\nbody\n", "unknown": "{\"type\":\"message\",\"cwd\":\"untouched\"}\n",
		"large": strings.Repeat("x", (1<<20)+17),
	}
	for name, content := range logs {
		migrationWrite(t, filepath.Join(h.Yesmen(), "active", ".yesman", "sessions", "lead", name+".jsonl"), content)
	}
	destination := filepath.Join(tmp, "destination")
	if err := h.CopyTo(context.Background(), destination); err != nil {
		t.Fatal(err)
	}
	for name, content := range logs {
		data, err := os.ReadFile(filepath.Join(destination, "yesmen", "active", ".yesman", "sessions", "lead", name+".jsonl"))
		if err != nil || !bytes.Equal(data, []byte(content)) {
			t.Errorf("%s session bytes were changed: %v", name, err)
		}
	}
}
