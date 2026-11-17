package yesmen

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// Bootstrap must keep the partial-List contract while initializing only the
// healthy workspace. Oversized metadata cannot become a zero-generation hire.
func TestEnsureTaskLogsWithPartialMetadata(t *testing.T) {
	s, alice := metadataStore(t)
	bob, err := s.HireAs("bob", "synthetic/model")
	if err != nil {
		t.Fatal(err)
	}
	for _, y := range []Yesman{alice, bob} {
		if err := os.Remove(y.TaskLogPath()); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(alice.MetaDir(), "yesman.json")
	metadata, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	metadata = append(metadata, bytes.Repeat([]byte(" "), maxYesmanMetadataBytes+1-len(metadata))...)
	if err := os.WriteFile(path, metadata, 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		err := s.EnsureTaskLogs()
		var partial MetadataErrors
		if !errors.As(err, &partial) || len(partial) != 1 {
			t.Fatalf("bootstrap lost partial metadata diagnostic: %v", err)
		}
		if got := readDocument(t, bob.TaskLogPath()); got != taskLogTemplate {
			t.Fatal("healthy workspace did not get the TOC")
		}
		if _, err := os.Lstat(alice.TaskLogPath()); !os.IsNotExist(err) {
			t.Fatalf("unreadable workspace was initialized: %v", err)
		}
		data, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(data, metadata) {
			t.Fatalf("metadata changed: %v", err)
		}
		after, err := os.Stat(path)
		if err != nil || !os.SameFile(before, after) || !before.ModTime().Equal(after.ModTime()) {
			t.Fatalf("metadata identity changed: %v", err)
		}
	}
	got, err := s.Get("bob")
	if err != nil || !got.HiredAt.Equal(bob.HiredAt) || got.Model != bob.Model {
		t.Fatalf("healthy workspace identity changed: %+v %v", got, err)
	}
}
