package yesmen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readDocument(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestHireInitializesSharedDocuments(t *testing.T) {
	root := t.TempDir()
	s := New(filepath.Join(root, "yesmen"), filepath.Join(root, "archive"))
	y, err := s.HireAs("alice", "provider/model:high")
	if err != nil {
		t.Fatal(err)
	}
	if got := y.TaskLogPath(); got != filepath.Join(y.MetaDir(), "task-log.md") {
		t.Fatalf("task log path = %q", got)
	}
	if got := readDocument(t, y.TaskLogPath()); got != taskLogTemplate || !strings.HasPrefix(got, "# Task log\n") {
		t.Fatalf("new task log = %q", got)
	}
	index := readDocument(t, filepath.Join(y.KnowledgeDir(), "INDEX.md"))
	for _, want := range []string{"Durable guidance", "Phosphor", "Finishing a task alone", "task log or board"} {
		if !strings.Contains(index, want) {
			t.Errorf("new index lacks %q", want)
		}
	}
	loaded, err := s.Get("alice")
	if err != nil || loaded.Model != y.Model || !loaded.HiredAt.Equal(y.HiredAt) {
		t.Fatalf("hire metadata changed: %+v, %v", loaded, err)
	}
	if err := s.EnsureTaskLogs(); err != nil {
		t.Fatal(err)
	}
	if got := readDocument(t, y.TaskLogPath()); got != taskLogTemplate {
		t.Fatal("startup changed the new log")
	}
}

func TestEnsureTaskLogsPreservesLegacyWorkspace(t *testing.T) {
	root := t.TempDir()
	workspaces, archive := filepath.Join(root, "yesmen"), filepath.Join(root, "archive")
	y := Yesman{Name: "alice", Path: filepath.Join(workspaces, "alice")}
	legacy := map[string]string{
		".yesman/yesman.json":              `{"hiredAt":"2026-01-01T00:00:00Z","model":"provider/model:high","custom":"keep"}`,
		".yesman/knowledge/INDEX.md":       "# User index\n- [Old PR record](old.md)\n",
		".yesman/knowledge/old.md":         "Existing task details, intentionally not migrated.\n",
		".yesman/knowledge/deep/custom.md": "User conventions.\n",
		".yesman/board.json":               `{"tasks":[{"id":"t1","title":"Keep this","status":"doing"}]}`,
		".yesman/workers.json":             `[{"name":"w1","task":"Keep this worker","number":1}]`,
		".yesman/sessions/lead/run.jsonl":  "user's session data\n",
		"notes.txt":                        "unrelated user file\n",
	}
	for path, data := range legacy {
		abs := filepath.Join(y.Path, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 { // simulate repeated launches, using a fresh store each time
		if err := New(workspaces, archive).EnsureTaskLogs(); err != nil {
			t.Fatal(err)
		}
		for path, want := range legacy {
			if got := readDocument(t, filepath.Join(y.Path, filepath.FromSlash(path))); got != want {
				t.Errorf("startup rewrote %s: %q", path, got)
			}
		}
	}
	if got := readDocument(t, y.TaskLogPath()); got != taskLogTemplate {
		t.Fatal("missing legacy task log was not initialized")
	}
	before, err := os.Stat(y.TaskLogPath())
	if err != nil {
		t.Fatal(err)
	}
	if err := New(workspaces, archive).EnsureTaskLogs(); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(y.TaskLogPath())
	if err != nil || !os.SameFile(before, after) || !before.ModTime().Equal(after.ModTime()) {
		t.Fatalf("repeat startup replaced or touched the log: %v", err)
	}
}

func TestEnsureTaskLogsNeverOverwritesExistingPaths(t *testing.T) {
	for _, kind := range []string{"content", "empty", "directory", "symlink", "dangling-symlink"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			s := New(filepath.Join(root, "yesmen"), filepath.Join(root, "archive"))
			y, err := s.Hire("alice")
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(y.TaskLogPath()); err != nil {
				t.Fatal(err)
			}
			want := ""
			switch kind {
			case "content", "empty":
				if kind == "content" {
					want = "# My task history\nKeep every byte, including this format.\n"
				}
				err = os.WriteFile(y.TaskLogPath(), []byte(want), 0o600)
			case "directory":
				err = os.Mkdir(y.TaskLogPath(), 0o755)
			default:
				target := filepath.Join(root, "user-file.md")
				if kind == "symlink" {
					want = "user-owned target\n"
					if err := os.WriteFile(target, []byte(want), 0o644); err != nil {
						t.Fatal(err)
					}
				}
				if err := os.Symlink(target, y.TaskLogPath()); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			before, err := os.Lstat(y.TaskLogPath())
			if err != nil {
				t.Fatal(err)
			}
			for range 2 {
				if err := s.EnsureTaskLogs(); err != nil {
					t.Fatal(err)
				}
			}
			after, err := os.Lstat(y.TaskLogPath())
			if err != nil || !os.SameFile(before, after) || !before.ModTime().Equal(after.ModTime()) || before.Mode() != after.Mode() {
				t.Fatalf("existing %s was changed: %v", kind, err)
			}
			if kind == "content" || kind == "empty" || kind == "symlink" {
				if got := readDocument(t, y.TaskLogPath()); got != want {
					t.Fatalf("existing content changed: %q", got)
				}
			}
			if kind == "dangling-symlink" {
				if _, err := os.Stat(y.TaskLogPath()); !os.IsNotExist(err) {
					t.Fatalf("created dangling symlink target: %v", err)
				}
			}
		})
	}
}

func TestEnsureTaskLogsDoesNotHireDirectories(t *testing.T) {
	root := t.TempDir()
	s := New(filepath.Join(root, "yesmen"), filepath.Join(root, "archive"))
	if err := s.EnsureTaskLogs(); err != nil { // no workspaces yet
		t.Fatal(err)
	}
	path := filepath.Join(s.root, "unrelated")
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := s.EnsureTaskLogs(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(path, ".yesman")); !os.IsNotExist(err) {
		t.Fatalf("bootstrap initialized unrelated directory: %v", err)
	}
}
