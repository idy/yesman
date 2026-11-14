package tasklog

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestRetainNeverReplacesLateOccupant(t *testing.T) {
	b := []byte("# Captured source\n")
	ws := fixture(t, b)
	m, _ := Run(ws, Options{Action: "dry-run"})
	sentinel := []byte("unrelated recovery file\n")
	_, err := Run(ws, Options{Action: "apply", Offline: true, Expect: m.SHA256, Hook: func(stage string) error {
		if stage == "prepared" {
			return os.WriteFile(filepath.Join(ws, m.Retained), sentinel, 0o600)
		}
		return nil
	}})
	if err == nil {
		t.Fatal("retained collision accepted")
	}
	if !bytes.Equal(file(t, ws, TOC), b) || !bytes.Equal(file(t, ws, m.Retained), sentinel) {
		t.Fatal("source or unknown retained occupant changed")
	}
}

func TestRetainExclusiveLinkRejectsLastMomentOccupant(t *testing.T) {
	b := []byte("# Preserve source and target\n")
	ws := fixture(t, b)
	m, _ := Run(ws, Options{Action: "dry-run"})
	sentinel := []byte("appeared immediately before Link\n")
	_, err := Run(ws, Options{Action: "apply", Offline: true, Expect: m.SHA256, Hook: func(stage string) error {
		if stage == "retain-before-link" {
			return os.WriteFile(filepath.Join(ws, m.Retained), sentinel, 0o600)
		}
		return nil
	}})
	if err == nil || !bytes.Equal(file(t, ws, TOC), b) || !bytes.Equal(file(t, ws, m.Retained), sentinel) {
		t.Fatal("exclusive source Link replaced an unknown target")
	}
}

func TestPreparedSourceIdentitySurvivesRestart(t *testing.T) {
	b := []byte("# Same bytes and mtime, different inode\n")
	ws := fixture(t, b)
	m, _ := Run(ws, Options{Action: "dry-run"})
	stop := errors.New("stop after preparation")
	_, err := Run(ws, Options{Action: "apply", Offline: true, Expect: m.SHA256, Hook: func(s string) error {
		if s == "prepared" {
			return stop
		}
		return nil
	}})
	if !errors.Is(err, stop) {
		t.Fatal(err)
	}
	source := filepath.Join(ws, TOC)
	st, err := os.Stat(source)
	if err != nil {
		t.Fatal(err)
	}
	saved := source + ".captured-source"
	if err = os.Rename(source, saved); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(source, b, 0o600); err != nil {
		t.Fatal(err)
	}
	if err = os.Chtimes(source, st.ModTime(), st.ModTime()); err != nil {
		t.Fatal(err)
	}
	old, err := os.OpenFile(saved, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close()
	if _, err = Run(ws, Options{Action: "apply", Offline: true, Expect: m.SHA256}); err == nil {
		t.Fatal("replacement source identity accepted")
	}
	tail := []byte("late append to the captured source generation\n")
	if _, err = old.Write(tail); err != nil {
		t.Fatal(err)
	}
	if _, err = Run(ws, Options{Action: "verify"}); err == nil {
		t.Fatal("replacement generation verified")
	}
	if !bytes.Equal(file(t, ws, TOC+".captured-source"), append(b, tail...)) || !bytes.Equal(file(t, ws, TOC), b) {
		t.Fatal("source generation data changed")
	}
}

func TestRetainExclusiveStagesResume(t *testing.T) {
	for _, stage := range []string{"retain-prepared", "retain-linked", "retain-unlinked"} {
		t.Run(stage, func(t *testing.T) {
			b := []byte("# Original inode\n")
			ws := fixture(t, b)
			m, _ := Run(ws, Options{Action: "dry-run"})
			stop := errors.New("retention interrupted")
			_, err := Run(ws, Options{Action: "apply", Offline: true, Expect: m.SHA256, Hook: func(s string) error {
				if s == stage {
					return stop
				}
				return nil
			}})
			if !errors.Is(err, stop) {
				t.Fatal(err)
			}
			if _, err = Run(ws, Options{Action: "apply", Offline: true, Expect: m.SHA256}); err != nil {
				t.Fatal(err)
			}
			if _, err = Run(ws, Options{Action: "verify"}); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(file(t, ws, m.Retained), b) {
				t.Fatal("retained source differs")
			}
		})
	}
}

func TestWrongExpectedHashCannotRecoverRetention(t *testing.T) {
	for _, stage := range []string{"retain-prepared", "retain-linked", "retain-unlinked"} {
		t.Run(stage, func(t *testing.T) {
			ws := fixture(t, []byte("# Source preserved on rejected command\n"))
			m, _ := Run(ws, Options{Action: "dry-run"})
			stop := errors.New("interrupt retention")
			_, err := Run(ws, Options{Action: "apply", Offline: true, Expect: m.SHA256, Hook: func(s string) error {
				if s == stage {
					return stop
				}
				return nil
			}})
			if !errors.Is(err, stop) {
				t.Fatal(err)
			}
			snapshot := func() map[string]string {
				result := map[string]string{}
				for _, p := range []string{TOC, m.Retained, Marker, retainIntentPath} {
					b, e := os.ReadFile(filepath.Join(ws, p))
					if errors.Is(e, os.ErrNotExist) {
						result[p] = "absent"
						continue
					}
					if e != nil {
						t.Fatal(e)
					}
					f, e := os.Open(filepath.Join(ws, p))
					if e != nil {
						t.Fatal(e)
					}
					n, e := links(f)
					_ = f.Close()
					if e != nil {
						t.Fatal(e)
					}
					result[p] = fmt.Sprintf("%d:%s", n, digest(b))
				}
				return result
			}
			before := snapshot()
			for _, action := range []string{"apply", "rollback"} {
				if _, err = Run(ws, Options{Action: action, Offline: true, Expect: strings.Repeat("0", 64)}); err == nil {
					t.Fatal("wrong hash accepted")
				}
				if !reflect.DeepEqual(before, snapshot()) {
					t.Fatal("wrong-hash command changed source paths or retention intent")
				}
			}
		})
	}
}

func TestLegacyMetadataWithoutIdentityIsRefused(t *testing.T) {
	b := []byte("# Preserve old evidence\n")
	ws := fixture(t, b)
	m, _ := Run(ws, Options{Action: "dry-run"})
	old := Manifest{Version: 1, SHA256: m.SHA256, SourceModTime: m.SourceModTime, Bytes: m.Bytes}
	raw, _ := json.Marshal(old)
	if err := os.WriteFile(filepath.Join(ws, Marker), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"apply", "verify", "rollback"} {
		if _, err := Run(ws, Options{Action: action, Offline: true, Expect: m.SHA256}); err == nil {
			t.Fatalf("%s guessed legacy identity", action)
		}
	}
	if !bytes.Equal(file(t, ws, TOC), b) || !bytes.Equal(file(t, ws, Marker), raw) {
		t.Fatal("legacy evidence was changed")
	}
}
