package yesmen

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
)

func TestRenamePersistsWithoutChangingIdentity(t *testing.T) {
	base := t.TempDir()
	s := New(filepath.Join(base, "yesmen"), filepath.Join(base, "archive"))
	original, err := s.HireAs("alice", "synthetic/model-alpha:high")
	if err != nil {
		t.Fatal(err)
	}
	if original.Label() != "alice" || original.Model != "synthetic/model-alpha:high" {
		t.Fatal(original)
	}
	if err := s.AddWorker("alice", Worker{Name: "worker-one", Task: "synthetic task"}); err != nil {
		t.Fatal(err)
	}
	title, owner := "Synthetic task", "worker-one"
	if _, err := s.ChangeBoard("alice", BoardChange{Add: []TaskEdit{{Title: &title, Owner: &owner}}}); err != nil {
		t.Fatal(err)
	}
	session := original.SessionDir("worker-one")
	if err := os.MkdirAll(session, 0755); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(session, "synthetic.jsonl")
	if err := os.WriteFile(log, []byte("synthetic session"), 0644); err != nil {
		t.Fatal(err)
	}
	beforeWorkers, _ := s.Workers("alice")
	beforeBoard, _ := s.Board("alice")
	metadata := filepath.Join(original.MetaDir(), "yesman.json")
	var meta map[string]any
	if err := readJSON(metadata, &meta); err != nil {
		t.Fatal(err)
	}
	meta["futureField"] = "preserved"
	if err := writeJSON(metadata, meta); err != nil {
		t.Fatal(err)
	}
	renamed, err := s.Rename("alice", "  项目 Alpha 团队  ")
	if err != nil {
		t.Fatal(err)
	}
	if renamed.Model != original.Model {
		t.Fatalf("rename lost the lead's configured model: %q", renamed.Model)
	}
	if renamed.Name != original.Name || renamed.Path != original.Path || !renamed.HiredAt.Equal(original.HiredAt) || renamed.Label() != "项目 Alpha 团队" {
		t.Fatalf("identity or label changed incorrectly: %+v", renamed)
	}
	restarted := New(s.root, s.archive)
	got, err := restarted.Get("alice")
	if err != nil || got != renamed {
		t.Fatalf("reload: %+v %v", got, err)
	}
	listed, err := restarted.List()
	if err != nil || len(listed) != 1 || listed[0] != renamed {
		t.Fatalf("list: %+v %v", listed, err)
	}
	workers, _ := restarted.Workers("alice")
	board, _ := restarted.Board("alice")
	if !reflect.DeepEqual(workers, beforeWorkers) || !reflect.DeepEqual(board, beforeBoard) {
		t.Fatal("worker or board association changed")
	}
	if data, err := os.ReadFile(log); err != nil || string(data) != "synthetic session" {
		t.Fatalf("session lost: %v", err)
	}
	if err := readJSON(metadata, &meta); err != nil || meta["futureField"] != "preserved" || meta["model"] != original.Model {
		t.Fatalf("metadata lost: %v", err)
	}
	if _, err := restarted.Hire("bob"); err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.Rename("bob", renamed.Label()); err != nil {
		t.Fatalf("duplicate labels must be allowed: %v", err)
	}
	if err := restarted.Use("alice", func(y Yesman) error { return restarted.RemoveWorker(y.Name, "worker-one") }); err != nil {
		t.Fatal(err)
	}
}

func TestRenameRejectsInvalidAndPreservesMetadataOnFailure(t *testing.T) {
	base := t.TempDir()
	s := New(filepath.Join(base, "yesmen"), filepath.Join(base, "archive"))
	y, err := s.Hire("alice")
	if err != nil {
		t.Fatal(err)
	}
	for _, label := range []string{"", " \t\n", "a\nb", "a\x00b", "a\u0085b", string([]byte{0xff})} {
		if _, err := s.Rename("alice", label); err == nil {
			t.Fatalf("accepted %q", label)
		}
	}
	if _, err := s.Rename("missing", "Valid"); err == nil {
		t.Fatal("accepted missing yesman")
	}
	if _, err := s.Rename("../alice", "Valid"); err == nil {
		t.Fatal("accepted invalid identity")
	}
	path := filepath.Join(y.MetaDir(), "yesman.json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// A directory at the atomic temp path simulates a failed write on every OS.
	if err := os.Mkdir(path+".tmp", 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Rename("alice", "New label"); err == nil {
		t.Fatal("write failure ignored")
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("failed rename changed metadata")
	}
	os.Remove(path + ".tmp")
	if err := os.WriteFile(path, []byte("{invalid"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Rename("alice", "New label"); err == nil {
		t.Fatal("corrupt metadata overwritten")
	}
	if data, _ := os.ReadFile(path); json.Valid(data) {
		t.Fatal("corrupt metadata replaced")
	}
}

func TestRenameConcurrentReadersAndWorkers(t *testing.T) {
	base := t.TempDir()
	s := New(filepath.Join(base, "yesmen"), filepath.Join(base, "archive"))
	if _, err := s.Hire("alice"); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 3 {
		wg.Go(func() {
			for range 20 {
				if _, err := s.Get("alice"); err != nil {
					t.Error(err)
				}
				if _, err := s.List(); err != nil {
					t.Error(err)
				}
				if _, err := s.Rename("alice", "项目 Alpha"); err != nil {
					t.Error(err)
				}
			}
		})
	}
	wg.Go(func() {
		for range 20 {
			if err := s.AddWorker("alice", Worker{Name: "worker", Task: "synthetic"}); err != nil {
				t.Error(err)
			}
			if err := s.RemoveWorker("alice", "worker"); err != nil {
				t.Error(err)
			}
		}
	})
	wg.Wait()
}
