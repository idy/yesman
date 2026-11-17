package tasklog

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExclusiveInstallInterruptionResumesBeforeAliasChecks(t *testing.T) {
	for _, where := range []string{"marker", "part", "original", "manifest", "candidate", "toc"} {
		for _, phase := range []string{"prepared", "linked", "unlinked"} {
			t.Run(where+"-"+phase, func(t *testing.T) {
				b := []byte("# Original evidence\n[Knowledge](knowledge/rules.md)\n")
				ws := fixture(t, b)
				m, _ := Run(ws, Options{Action: "dry-run"})
				target := map[string]string{"marker": Marker, "part": m.Parts[0].Path, "original": m.Original, "manifest": manifestPath(m.SHA256), "candidate": generation(m.SHA256) + "/toc.md", "toc": TOC}[where]
				interrupted := errors.New("simulated interruption in exclusive installation")
				_, e := Run(ws, Options{Action: "apply", Offline: true, Expect: m.SHA256, Hook: func(stage string) error {
					if stage == "install-"+phase+":"+target {
						return interrupted
					}
					return nil
				}})
				if !errors.Is(e, interrupted) {
					t.Fatal(e)
				}
				// This is the real filesystem state produced by put, including a
				// two-link marker/TOC at the exact formerly unrecoverable boundary.
				if phase == "linked" {
					root, e := os.OpenRoot(ws)
					if e != nil {
						t.Fatal(e)
					}
					if e = Check(root, target, false); e == nil {
						t.Fatal("ordinary alias check was relaxed")
					}
					_ = root.Close()
				}
				if _, e = Run(ws, Options{Action: "apply", Offline: true, Expect: m.SHA256}); e != nil {
					t.Fatal(e)
				}
				if _, e = Run(ws, Options{Action: "verify"}); e != nil {
					t.Fatal(e)
				}
				if !bytes.Equal(file(t, ws, m.Original), b) || !bytes.Equal(file(t, ws, m.Retained), b) {
					t.Fatal("source lost")
				}
				if _, e = os.Lstat(filepath.Join(ws, installIntentPath)); !errors.Is(e, os.ErrNotExist) {
					t.Fatal("completed intent retained")
				}
			})
		}
	}
}

func TestInterruptedRollbackExclusiveInstallResumes(t *testing.T) {
	for _, targetName := range []string{"toc", "saved"} {
		b := []byte("# Original\n")
		ws := fixture(t, b)
		m := apply(t, ws, Options{})
		target := TOC
		if targetName == "saved" {
			target = generation(m.SHA256) + "/rollback-toc.md"
		}
		interrupted := errors.New("rollback installation interrupted")
		_, e := Run(ws, Options{Action: "rollback", Offline: true, Expect: m.SHA256, Hook: func(stage string) error {
			if stage == "install-linked:"+target {
				return interrupted
			}
			return nil
		}})
		if !errors.Is(e, interrupted) {
			t.Fatal(e)
		}
		if _, e = Run(ws, Options{Action: "rollback", Offline: true, Expect: m.SHA256}); e != nil {
			t.Fatal(e)
		}
		if !bytes.Equal(file(t, ws, TOC), b) {
			t.Fatal("rollback lost original")
		}
	}
}

func TestUnrelatedHardLinksAndTamperedInstallIntentStayProtected(t *testing.T) {
	for _, kind := range []string{"unregistered", "third-alias", "wrong-source", "wrong-target", "linked-intent", "partial-intent"} {
		t.Run(kind, func(t *testing.T) {
			b := []byte("# User history\n")
			ws := fixture(t, b)
			m, _ := Run(ws, Options{Action: "dry-run"})
			interrupted := errors.New("stop with registered marker installation")
			if kind == "partial-intent" {
				_ = os.WriteFile(filepath.Join(ws, installIntentPath), []byte("{"), 0o600)
			} else {
				_, e := Run(ws, Options{Action: "apply", Offline: true, Expect: m.SHA256, Hook: func(stage string) error {
					if stage == "install-linked:"+Marker {
						return interrupted
					}
					return nil
				}})
				if !errors.Is(e, interrupted) {
					t.Fatal(e)
				}
				raw := file(t, ws, installIntentPath)
				var i installIntent
				if e = json.Unmarshal(raw, &i); e != nil {
					t.Fatal(e)
				}
				switch kind {
				case "unregistered":
					_ = os.Remove(filepath.Join(ws, installIntentPath))
				case "third-alias":
					if e = os.Link(filepath.Join(ws, Marker), filepath.Join(t.TempDir(), "unrelated.md")); e != nil {
						t.Skip(e)
					}
				case "wrong-source":
					i.Source = strings.Repeat("0", 64)
					raw, _ = json.Marshal(i)
					_ = os.WriteFile(filepath.Join(ws, installIntentPath), raw, 0o600)
				case "wrong-target":
					i.Target = ".yesman/knowledge/rules.md"
					raw, _ = json.Marshal(i)
					_ = os.WriteFile(filepath.Join(ws, installIntentPath), raw, 0o600)
				case "linked-intent":
					if e = os.Link(filepath.Join(ws, installIntentPath), filepath.Join(t.TempDir(), "intent-copy.json")); e != nil {
						t.Skip(e)
					}
				}
			}
			before := file(t, ws, TOC)
			if _, e := Run(ws, Options{Action: "apply", Offline: true, Expect: m.SHA256}); e == nil {
				t.Fatal("unrelated/tampered install was accepted")
			}
			if !bytes.Equal(before, file(t, ws, TOC)) {
				t.Fatal("history changed")
			}
		})
	}
}
