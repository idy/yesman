//go:build unix

package yesmen

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"time"
)

// Adapted from the original PR68 probe: the FIFO exists before the public
// call. A real connected writer proves the old reader is inside FIFO I/O
// before Bob starts; it stays open until Bob's responsiveness is checked.
func TestMetadataFIFOBeforeCall(t *testing.T) {
	for _, entry := range []string{"Get", "List", "Use"} {
		t.Run(entry, func(t *testing.T) {
			s, y := metadataStore(t)
			if _, err := s.Hire("bob"); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(y.MetaDir(), "yesman.json")
			original, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := syscall.Mkfifo(path, 0o600); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() {
				var err error
				switch entry {
				case "Get":
					_, err = s.Get("alice")
				case "List":
					_, err = s.List()
				case "Use":
					err = s.Use("alice", func(Yesman) error { return nil })
				}
				done <- err
			}()
			var writer *os.File
			var peer chan error
			blocked := false
			finished := false
			var callErr error
			// Even a failed assertion must drain and join the old implementation.
			defer func() {
				if writer != nil {
					if _, err := writer.Write(original); err != nil {
						t.Error(err)
					}
					if err := writer.Close(); err != nil {
						t.Error(err)
					}
				}
				if !finished {
					select {
					case <-done:
					case <-time.After(3 * time.Second):
						t.Error("FIFO caller did not join after writer drain")
					}
				}
				if blocked {
					select {
					case <-peer:
					case <-time.After(3 * time.Second):
						t.Error("Bob did not join after writer drain")
					}
				}
			}()
			deadline := time.Now().Add(3 * time.Second)
			for writer == nil && !finished && time.Now().Before(deadline) {
				select {
				case callErr = <-done:
					finished = true
				default:
				}
				if finished {
					break
				}
				w, err := os.OpenFile(path, os.O_WRONLY|syscall.O_NONBLOCK, 0)
				if err == nil {
					writer = w
				} else if !errors.Is(err, syscall.ENXIO) {
					t.Fatal(err)
				}
				runtime.Gosched()
			}
			peer = make(chan error, 1)
			go func() { _, err := s.Get("bob"); peer <- err }()
			select {
			case err := <-peer:
				if err != nil {
					t.Error(err)
				}
			case <-time.After(300 * time.Millisecond):
				blocked = true
			}
			t.Logf("%s: readerHeldFIFO=%v bobBlocked=%v", entry, writer != nil, blocked)
			if writer != nil || blocked || !finished {
				t.Errorf("%s follows a FIFO yesman.json or blocks another workspace", entry)
			}
			if finished && callErr != nil {
				t.Errorf("%s: unexpected fallback error %v", entry, callErr)
			}
		})
	}
}

func TestMetadataAliasesAndDevice(t *testing.T) {
	for _, alias := range []string{"root", "metadata", "device"} {
		t.Run(alias, func(t *testing.T) {
			s, original := metadataStore(t)
			path := filepath.Join(original.MetaDir(), "yesman.json")
			switch alias {
			case "root":
				link := filepath.Join(t.TempDir(), "root-alias")
				if err := os.Symlink(s.root, link); err != nil {
					t.Fatal(err)
				}
				s = New(link, s.archive)
				original.Path = filepath.Join(link, original.Name)
			case "metadata", "device":
				target := filepath.Join(t.TempDir(), "regular-metadata.json")
				if alias == "metadata" {
					if err := os.Rename(path, target); err != nil {
						t.Fatal(err)
					}
				} else {
					target = os.DevNull
					if err := os.Remove(path); err != nil {
						t.Fatal(err)
					}
				}
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			}
			got, err := s.Get("alice")
			if alias == "device" {
				var decoded any
				if readYesmanMetadata(path, &decoded) == nil || decoded != nil {
					t.Fatal("device metadata accepted")
				}
				if err != nil || !got.HiredAt.IsZero() || got.DisplayName != "" || got.Model != "" {
					t.Fatalf("device fallback: %+v %v", got, err)
				}
			} else {
				if err != nil || got != original {
					t.Fatalf("alias changed metadata: %+v %v, want %+v", got, err, original)
				}
				list, err := s.List()
				if err != nil || len(list) != 1 || list[0] != original {
					t.Fatalf("alias List = %+v %v", list, err)
				}
			}
		})
	}
}
