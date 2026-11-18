package tasklog

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type admissionArtifact struct {
	Mode     os.FileMode
	Size     int64
	ModTime  int64
	Links    uint64
	Identity Identity
	Data     string
}

// Snapshot every artifact, including directories, lock, private temporary names,
// installation/retention intents and the original inode. Each refusal case owns
// a new workspace; a failed attempt is never used to prepare the next case.
func admissionSnapshot(t *testing.T, ws string) map[string]admissionArtifact {
	t.Helper()
	result := map[string]admissionArtifact{}
	err := filepath.WalkDir(ws, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		st, err := os.Lstat(p)
		if err != nil {
			return err
		}
		a := admissionArtifact{Mode: st.Mode(), Size: st.Size(), ModTime: st.ModTime().UnixNano()}
		if st.Mode().IsRegular() {
			f, e := os.Open(p)
			if e != nil {
				return e
			}
			a.Links, e = links(f)
			if e == nil {
				a.Identity, e = fileIdentity(f)
			}
			_ = f.Close()
			if e != nil {
				return e
			}
			b, e := os.ReadFile(p)
			if e != nil {
				return e
			}
			a.Data = string(b)
		}
		rel, err := filepath.Rel(ws, p)
		if err != nil {
			return err
		}
		result[rel] = a
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func pendingAdmission(t *testing.T, location string) (string, Manifest) {
	t.Helper()
	ws := fixture(t, []byte("# Complete original\n[Knowledge](knowledge/rules.md)\n```md\n## nested evidence\n```\n"))
	m, err := Run(ws, Options{Action: "dry-run"})
	if err != nil {
		t.Fatal(err)
	}
	stage := "install-prepared:" + TOC
	if strings.HasPrefix(location, "marker-") {
		stage = "install-" + strings.TrimPrefix(location, "marker-") + ":" + Marker
	}
	stop := errors.New("interrupted at exact installation stage")
	_, err = Run(ws, Options{Action: "apply", Offline: true, Expect: m.SHA256, Hook: func(s string) error {
		if s == stage {
			return stop
		}
		return nil
	}})
	if !errors.Is(err, stop) {
		t.Fatal(err)
	}
	return ws, m
}

func TestRecoveryAdmissionRefusesInvalidGenerationBeforeInstall(t *testing.T) {
	for _, location := range []string{"toc-prepared", "marker-prepared", "marker-linked", "marker-unlinked"} {
		for _, bad := range []string{"zero-birth", "missing-identity", "legacy", "malformed", "wrong-expect", "wrong-marker-source", "oversized", "missing-marker"} {
			if bad == "missing-marker" && location != "toc-prepared" {
				continue
			}
			for _, action := range []string{"apply", "rollback"} {
				t.Run(location+"/"+bad+"/"+action, func(t *testing.T) {
					ws, m := pendingAdmission(t, location)
					raw := file(t, ws, installIntentPath)
					var i installIntent
					if err := json.Unmarshal(raw, &i); err != nil {
						t.Fatal(err)
					}
					expect := m.SHA256
					if bad == "wrong-expect" {
						expect = strings.Repeat("0", 64)
					} else if bad == "missing-marker" {
						if err := os.Remove(filepath.Join(ws, Marker)); err != nil {
							t.Fatal(err)
						}
					} else {
						name := Marker
						if location == "marker-prepared" {
							name = i.Temporary
						}
						b := file(t, ws, name)
						var marker Manifest
						if err := json.Unmarshal(b, &marker); err != nil {
							t.Fatal(err)
						}
						switch bad {
						case "zero-birth":
							marker.SourceIdentity = Identity{Platform: "darwin", Volume: "1", ID: "2", Birth: "0:0"}
						case "missing-identity":
							marker.SourceIdentity = Identity{}
						case "legacy":
							marker.Version = 1
						case "wrong-marker-source":
							marker.SHA256 = strings.Repeat("0", 64)
						}
						b = manifestBytes(marker)
						if bad == "malformed" {
							b = []byte("{invalid marker\n")
						}
						if bad == "oversized" {
							b = append(b, []byte(strings.Repeat(" ", 1<<20))...)
						}
						if err := os.WriteFile(filepath.Join(ws, name), b, 0600); err != nil {
							t.Fatal(err)
						}
						if i.Target == Marker {
							i.Bytes = len(b)
							i.SHA256 = digest(b)
							raw, _ = json.Marshal(i)
							if err := os.WriteFile(filepath.Join(ws, installIntentPath), raw, 0600); err != nil {
								t.Fatal(err)
							}
						}
					}
					before := admissionSnapshot(t, ws)
					if _, err := Run(ws, Options{Action: action, Offline: true, Expect: expect}); err == nil {
						t.Error("invalid generation accepted")
					}
					if !reflect.DeepEqual(before, admissionSnapshot(t, ws)) {
						t.Fatal("rejected command changed interrupted installation artifacts")
					}
				})
			}
		}
	}
}

func TestRecoveryAdmissionValidPendingInstall(t *testing.T) {
	for _, location := range []string{"toc-prepared", "marker-prepared", "marker-linked", "marker-unlinked"} {
		for _, action := range []string{"apply", "rollback"} {
			t.Run(location+"/"+action, func(t *testing.T) {
				ws, m := pendingAdmission(t, location)
				originalBytes := []byte("# Complete original\n[Knowledge](knowledge/rules.md)\n```md\n## nested evidence\n```\n")
				if _, err := Run(ws, Options{Action: action, Offline: true, Expect: m.SHA256}); err != nil {
					t.Fatal(err)
				}
				if action == "apply" {
					if _, err := Run(ws, Options{Action: "verify"}); err != nil {
						t.Fatal(err)
					}
					if string(file(t, ws, m.Original)) != string(originalBytes) {
						t.Fatal("original bytes changed")
					}
				} else if string(file(t, ws, TOC)) != string(originalBytes) {
					t.Fatal("rollback lost original")
				}
			})
		}
	}
}
