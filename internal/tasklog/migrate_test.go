package tasklog

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func fixture(t *testing.T, b []byte) string {
	t.Helper()
	workspace := t.TempDir()
	if err := os.Mkdir(filepath.Join(workspace, ".yesman"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, TOC), b, 0o600); err != nil {
		t.Fatal(err)
	}
	return workspace
}
func file(t *testing.T, ws, path string) []byte {
	t.Helper()
	b, e := os.ReadFile(filepath.Join(ws, path))
	if e != nil {
		t.Fatal(e)
	}
	return b
}
func apply(t *testing.T, ws string, o Options) Manifest {
	t.Helper()
	m, e := Run(ws, Options{Action: "dry-run"})
	if e != nil {
		t.Fatal(e)
	}
	o.Action = "apply"
	o.Expect = m.SHA256
	o.Offline = true
	if m, e = Run(ws, o); e != nil {
		t.Fatal(e)
	}
	return m
}

func TestLosslessMigrationAndIdempotence(t *testing.T) {
	for _, b := range [][]byte{nil, []byte("no newline 中文"), []byte(strings.Repeat("## Same heading\r\n[Knowledge](knowledge/rules.md#anchor)\n```go\n# not heading\n````\n", 3000)), []byte(strings.Repeat("中", 40000))} {
		ws := fixture(t, b)
		before, e := os.Stat(filepath.Join(ws, TOC))
		if e != nil {
			t.Fatal(e)
		}
		// Windows Stat can resolve file identity lazily through its old path.
		// Compare against a handle while that path still names the source,
		// then close it before rename; the retained-ID assertion stays real.
		opened, e := os.Open(filepath.Join(ws, TOC))
		if e != nil {
			t.Fatal(e)
		}
		stable, e := opened.Stat()
		if e != nil || !os.SameFile(before, stable) {
			_ = opened.Close()
			t.Fatal("could not capture original identity", e)
		}
		if e = opened.Close(); e != nil {
			t.Fatal(e)
		}
		m, e := Run(ws, Options{Action: "dry-run"})
		if e != nil {
			t.Fatal(e)
		}
		if digest(file(t, ws, TOC)) != m.SHA256 {
			t.Fatal("dry-run changed source")
		}
		entries, e := os.ReadDir(filepath.Join(ws, ".yesman"))
		if e != nil || len(entries) != 1 {
			t.Fatalf("dry-run wrote files: %v", e)
		}
		m = apply(t, ws, Options{})
		var rebuilt []byte
		for _, p := range m.Parts {
			chunk := file(t, ws, p.Path)
			if len(chunk) > chunkSize || digest(chunk) != p.SHA256 {
				t.Fatal("invalid chunk")
			}
			rebuilt = append(rebuilt, chunk...)
		}
		if !bytes.Equal(b, rebuilt) || !bytes.Equal(b, file(t, ws, m.Original)) {
			t.Fatal("history loss")
		}
		after, e := os.Stat(filepath.Join(ws, m.Retained))
		if e != nil || !os.SameFile(before, after) {
			t.Fatal("original inode not retained")
		}
		for range 2 {
			if _, e := Run(ws, Options{Action: "apply", Offline: true, Expect: m.SHA256}); e != nil {
				t.Fatal(e)
			}
			if _, e := Run(ws, Options{Action: "verify"}); e != nil {
				t.Fatal(e)
			}
		}
		// Later lead entries survive an idempotent apply; archives remain exact.
		f, e := os.OpenFile(filepath.Join(ws, TOC), os.O_APPEND|os.O_WRONLY, 0)
		if e != nil {
			t.Fatal(e)
		}
		_, _ = f.WriteString("\n- 2026-01-16 · t4 · [Next task](task-log/2026-01-16-t4-next.md)\n")
		_ = f.Close()
		edited := file(t, ws, TOC)
		if _, e := Run(ws, Options{Action: "apply", Offline: true, Expect: m.SHA256}); e != nil {
			t.Fatal(e)
		}
		if !bytes.Equal(edited, file(t, ws, TOC)) {
			t.Fatal("repeat apply changed newer TOC")
		}
	}
}

func TestLargeLegacyCatalogsStayBounded(t *testing.T) {
	b := []byte(strings.Repeat("# Historical heading\n[Knowledge](knowledge/rules.md)\n", 60000))
	ws := fixture(t, b)
	m := apply(t, ws, Options{})
	if len(m.Catalogs) < 2 || len(file(t, ws, TOC)) > 16<<10 {
		t.Fatal("unbounded root TOC")
	}
	for _, catalog := range m.Catalogs {
		content := file(t, ws, catalog.Path)
		if len(content) > 16<<10 || digest(content) != catalog.SHA256 {
			t.Fatal("unbounded or invalid catalog")
		}
	}
	if _, e := Run(ws, Options{Action: "verify"}); e != nil {
		t.Fatal(e)
	}
	if len(file(t, ws, Marker)) > 4096 {
		t.Fatal("marker copied the entire manifest")
	}
}

func TestMigrationCrashStagesResumeAndBootstrap(t *testing.T) {
	for _, stage := range []string{"marker", "prepared", "retained", "installed"} {
		t.Run(stage, func(t *testing.T) {
			b := []byte(strings.Repeat("# Evidence\nfull refs and links\n", 3000))
			ws := fixture(t, b)
			m, _ := Run(ws, Options{Action: "dry-run"})
			crash := errors.New("simulated interruption")
			_, e := Run(ws, Options{Action: "apply", Offline: true, Expect: m.SHA256, Hook: func(s string) error {
				if s == stage {
					return crash
				}
				return nil
			}})
			if !errors.Is(e, crash) {
				t.Fatal(e)
			}
			if e := Bootstrap(ws); e != nil {
				t.Fatal(e)
			}
			if stage == "retained" {
				if _, e := os.Stat(filepath.Join(ws, TOC)); !errors.Is(e, os.ErrNotExist) {
					t.Fatal("bootstrap filled interrupted switch")
				}
			}
			if _, e := Run(ws, Options{Action: "apply", Offline: true, Expect: m.SHA256}); e != nil {
				t.Fatal(e)
			}
			if _, e := Run(ws, Options{Action: "verify"}); e != nil {
				t.Fatal(e)
			}
			if !bytes.Equal(file(t, ws, m.Original), b) {
				t.Fatal("original lost")
			}
		})
	}
}

func TestConcurrentAppenderAndFailureNeverLoseSource(t *testing.T) {
	for _, stage := range []string{"prepared", "retained", "installed"} {
		t.Run(stage, func(t *testing.T) {
			b := []byte("# Historical snapshot\n")
			ws := fixture(t, b)
			m, _ := Run(ws, Options{Action: "dry-run"})
			// Keep an actual writer handle open through rename.
			f, e := os.OpenFile(filepath.Join(ws, TOC), os.O_APPEND|os.O_WRONLY, 0)
			if e != nil {
				t.Fatal(e)
			}
			defer f.Close()
			hooked := false
			_, e = Run(ws, Options{Action: "apply", Offline: true, Expect: m.SHA256, Hook: func(s string) error {
				if s == stage {
					hooked = true
					_, e := f.WriteString("late append\n")
					return e
				}
				return nil
			}})
			if e == nil {
				t.Fatal("concurrent append accepted")
			}
			if runtime.GOOS == "windows" && !hooked && strings.Contains(e.Error(), "rename") {
				if !bytes.Equal(file(t, ws, TOC), b) {
					t.Fatal("Windows move refusal changed source")
				}
				_ = f.Close()
				if _, retry := Run(ws, Options{Action: "apply", Offline: true, Expect: m.SHA256}); retry != nil {
					t.Fatal(retry)
				}
				t.Log("Windows refused rename while an external writer was open; closing it allowed safe resume")
				return
			}
			path := TOC
			if stage != "prepared" {
				path = m.Retained
			}
			if !bytes.Equal(file(t, ws, path), append(b, []byte("late append\n")...)) {
				t.Fatal("late append lost")
			}
			if _, e := Run(ws, Options{Action: "verify"}); e == nil {
				t.Fatal("conflicting migration verified")
			}
			recovered, e := Run(ws, Options{Action: "rollback", Offline: true, Expect: m.SHA256})
			if e != nil {
				t.Fatal(e)
			}
			want := append(b, []byte("late append\n")...)
			if !bytes.Equal(file(t, ws, TOC), want) || recovered.RestoredSHA256 != digest(want) {
				t.Fatal("rollback lost late writer bytes")
			}
		})
	}
	ws := fixture(t, []byte("original"))
	m, _ := Run(ws, Options{Action: "dry-run"})
	ioFailure := errors.New("simulated disk failure")
	if _, e := Run(ws, Options{Action: "apply", Offline: true, Expect: m.SHA256, Hook: func(s string) error {
		if s == "prepared" {
			return ioFailure
		}
		return nil
	}}); !errors.Is(e, ioFailure) {
		t.Fatal(e)
	}
	if string(file(t, ws, TOC)) != "original" {
		t.Fatal("failure touched original")
	}
}

func TestMigrationRefusesUnknownAndLinkedPaths(t *testing.T) {
	for _, kind := range []string{"part", "part-directory", "root-link", "parent-link", "hardlink", "nonutf8", "oversize", "wrong-hash", "toc"} {
		t.Run(kind, func(t *testing.T) {
			b := []byte("# Keep every byte\n")
			ws := fixture(t, b)
			m, _ := Run(ws, Options{Action: "dry-run"})
			target := filepath.Join(t.TempDir(), "sentinel")
			_ = os.WriteFile(target, b, 0o600)
			switch kind {
			case "part", "part-directory":
				_ = os.Mkdir(filepath.Join(ws, Dir), 0o755)
				if kind == "part" {
					_ = os.WriteFile(filepath.Join(ws, m.Parts[0].Path), []byte("unknown"), 0o600)
				} else {
					_ = os.Mkdir(filepath.Join(ws, m.Parts[0].Path), 0o755)
				}
			case "root-link":
				if e := os.Symlink(filepath.Dir(target), filepath.Join(ws, Dir)); e != nil {
					t.Skip(e)
				}
			case "parent-link":
				_ = os.Rename(filepath.Join(ws, ".yesman"), filepath.Join(ws, "saved"))
				if e := os.Symlink(filepath.Join(ws, "saved"), filepath.Join(ws, ".yesman")); e != nil {
					t.Skip(e)
				}
			case "hardlink":
				_ = os.Remove(filepath.Join(ws, TOC))
				if e := os.Link(target, filepath.Join(ws, TOC)); e != nil {
					t.Skip(e)
				}
			case "nonutf8":
				_ = os.WriteFile(filepath.Join(ws, TOC), []byte{0xff, 0xfe}, 0o600)
			case "oversize":
				f, e := os.OpenFile(filepath.Join(ws, TOC), os.O_WRONLY, 0)
				if e != nil {
					t.Fatal(e)
				}
				_ = f.Truncate(maxSource + 1)
				_ = f.Close()
			case "wrong-hash":
				m.SHA256 = strings.Repeat("0", 64)
			case "toc":
				_ = os.WriteFile(filepath.Join(ws, TOC), []byte(Template), 0o600)
			}
			before := file(t, ws, TOC)
			if _, e := Run(ws, Options{Action: "apply", Offline: true, Expect: m.SHA256}); e == nil {
				t.Fatal("unsafe migration allowed")
			}
			if !bytes.Equal(before, file(t, ws, TOC)) || !bytes.Equal(b, file(t, filepath.Dir(target), "sentinel")) {
				t.Fatal("user file changed")
			}
		})
	}
}

func TestMigrationExcludesConcurrentUtilityAndIOCollision(t *testing.T) {
	ws := fixture(t, []byte("# Original\n"))
	m, _ := Run(ws, Options{Action: "dry-run"})
	entered, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, e := Run(ws, Options{Action: "apply", Offline: true, Expect: m.SHA256, Hook: func(stage string) error {
			if stage == "prepared" {
				close(entered)
				<-release
			}
			return nil
		}})
		done <- e
	}()
	<-entered
	_, e := Run(ws, Options{Action: "apply", Offline: true, Expect: m.SHA256})
	close(release)
	if e == nil || !strings.Contains(e.Error(), "lock") {
		t.Fatalf("concurrent utility was not excluded: %v", e)
	}
	if e := <-done; e != nil {
		t.Fatal(e)
	}
	ws = fixture(t, []byte("# Original\n"))
	m, _ = Run(ws, Options{Action: "dry-run"})
	_, e = Run(ws, Options{Action: "apply", Offline: true, Expect: m.SHA256, Hook: func(stage string) error {
		if stage == "marker" {
			return os.WriteFile(filepath.Join(ws, Dir), []byte("occupied parent"), 0o600)
		}
		return nil
	}})
	if e == nil {
		t.Fatal("real filesystem collision ignored")
	}
	if string(file(t, ws, TOC)) != "# Original\n" {
		t.Fatal("filesystem failure changed source")
	}
}

func TestRollbackAndEditedTOCProtection(t *testing.T) {
	b := []byte("# original\r\n[link](knowledge/rules.md)\n")
	ws := fixture(t, b)
	m := apply(t, ws, Options{})
	if _, e := Run(ws, Options{Action: "rollback", Expect: m.SHA256, Offline: true}); e != nil {
		t.Fatal(e)
	}
	if !bytes.Equal(b, file(t, ws, TOC)) || !bytes.Equal(b, file(t, ws, m.Original)) {
		t.Fatal("rollback lost history")
	}
	if _, e := Run(ws, Options{Action: "rollback", Expect: m.SHA256, Offline: true}); e != nil {
		t.Fatal(e)
	}
	ws = fixture(t, b)
	m = apply(t, ws, Options{})
	f, _ := os.OpenFile(filepath.Join(ws, TOC), os.O_APPEND|os.O_WRONLY, 0)
	_, _ = f.WriteString("new entry")
	_ = f.Close()
	if _, e := Run(ws, Options{Action: "rollback", Expect: m.SHA256, Offline: true}); e == nil {
		t.Fatal("rollback erased newer entries")
	}
}
