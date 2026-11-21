package main

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/idy/yesman/internal/pool"
	"github.com/idy/yesman/internal/safefs/linktest"
	"github.com/idy/yesman/internal/schedule"
	"github.com/idy/yesman/internal/server"
	"github.com/idy/yesman/internal/yesmen"
)

func deletionFixture(t *testing.T) (*service, yesmen.Yesman, yesmen.DeletionTarget) {
	t.Helper()
	s := hireService(t)
	y, err := s.HireNamed(t.Context(), "Paper Kites")
	must(t, err)
	target, err := s.PrepareWorkspaceDeletion(y.Name, y.HiredAt)
	must(t, err)
	return s, y, target
}

func TestDeleteWorkspaceAndReopen(t *testing.T) {
	s, y, target := deletionFixture(t)
	a := s.app
	bob, err := s.Hire(t.Context(), "bob")
	must(t, err)
	before := mustReadFile(t, filepath.Join(bob.MetaDir(), "yesman.json"))
	a.schedules = schedule.New(schedule.Config{Yesmen: a.yesmen, Agents: a.agents})
	t.Cleanup(a.schedules.Close)
	input := schedule.PlanInput{Title: "fixture", Instructions: "fixture", Schedule: schedule.Spec{Kind: "daily", Time: "23:00"}, Timezone: "UTC", Enabled: true}
	_, err = a.schedules.At(y.HiredAt).Save(y.Name, input)
	must(t, err)
	_, err = a.schedules.At(bob.HiredAt).Save(bob.Name, input)
	must(t, err)
	must(t, os.WriteFile(filepath.Join(y.Path, "draft.txt"), []byte("mine"), 0600))
	// Opening and cancelling the confirmation does not stop or write anything.
	for range 2 {
		got, err := s.PrepareWorkspaceDeletion(y.Name, y.HiredAt)
		must(t, err)
		if got != target || got.Label != y.Label() {
			t.Fatal(got)
		}
	}
	if got := string(mustReadFile(t, filepath.Join(y.Path, "draft.txt"))); got != "mine" {
		t.Fatal(got)
	}
	var callers sync.WaitGroup
	errs := make(chan error, 2)
	for range 2 {
		callers.Go(func() { errs <- s.DeleteWorkspace(t.Context(), target) })
	}
	callers.Wait()
	close(errs)
	success := 0
	for err := range errs {
		if err == nil {
			success++
		}
	}
	if success != 1 {
		t.Fatalf("%d commits", success)
	}
	if _, err := os.Stat(y.Path); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal(err)
	}
	freshStore := yesmen.New(a.home.Yesmen(), a.home.Archive())
	if ys, err := freshStore.List(); err != nil || len(ys) != 1 || ys[0] != bob {
		t.Fatalf("reopen %v %v", ys, err)
	}
	if list, err := freshStore.StoredWorkspaces(); err != nil || len(list) != 0 {
		t.Fatalf("archive after deletion %v %v", list, err)
	}
	fresh, err := s.Hire(t.Context(), y.Name)
	must(t, err)
	if fresh.HiredAt.Equal(y.HiredAt) {
		t.Fatal("same generation")
	}
	if err := s.DeleteWorkspace(t.Context(), target); err == nil {
		t.Fatal("stale dialog")
	}
	if _, err := s.SaveWorkspaceFile(y.Name, "draft.txt", "late autosave", "", y.HiredAt); err == nil {
		t.Fatal("stale editor")
	}
	if _, err := os.Stat(filepath.Join(fresh.Path, "draft.txt")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal(err)
	}
	if _, err := a.schedules.At(y.HiredAt).Save(y.Name, input); !errors.Is(err, schedule.ErrReplaced) {
		t.Fatal(err)
	}
	if v, err := s.Schedules(y.Name, fresh.HiredAt); err != nil || len(v.Plans) != 0 {
		t.Fatalf("new hire plans %v %v", v, err)
	}
	if v, err := s.Schedules(bob.Name, bob.HiredAt); err != nil || len(v.Plans) != 1 || !v.Plans[0].Enabled {
		t.Fatalf("other plans %v %v", v, err)
	}
	if got := mustReadFile(t, filepath.Join(bob.MetaDir(), "yesman.json")); string(got) != string(before) {
		t.Fatal("other workspace changed")
	}
}

func TestDeleteAfterDesktopWorktreeCleanup(t *testing.T) {
	s, y, target := deletionFixture(t)
	a := s.app
	a.pool, _ = poolWith(t, "project")
	a.desktopWorktrees.Server = &server.Server{Pool: a.pool, Yesmen: a.yesmen, Agents: a.agents}
	repos, err := a.pool.List()
	must(t, err)
	if len(repos) != 1 {
		t.Fatal(repos)
	}
	repo := repos[0]
	w, err := a.pool.AddWorktree(t.Context(), repo, pool.WorktreeOptions{Dest: filepath.Join(y.Path, "review"), Ref: "origin/main"})
	must(t, err)
	title, owner, status := "Inspect project", yesmen.LeadName, yesmen.TaskDropped
	board, err := a.yesmen.ChangeBoard(y.Name, yesmen.BoardChange{Add: []yesmen.TaskEdit{{Title: &title, Owner: &owner, Status: &status}}})
	must(t, err)
	rec := yesmen.WorktreeRecord{ID: w.CleanupID, Dir: w.Dir, Repo: w.Repo, Owner: owner, OwnerSince: y.HiredAt, Purpose: "review", Task: board.Tasks[0].ID, Head: w.Head}
	must(t, a.yesmen.SaveWorktreeRecords(y.Name, []yesmen.WorktreeRecord{rec}))
	if err := s.DeleteWorkspace(t.Context(), target); err == nil || !strings.Contains(err.Error(), w.Dir) {
		t.Fatalf("expected checkout blocker: %v", err)
	}
	preview, err := s.PreviewWorktreeCleanup(t.Context(), y.Name, y.HiredAt, &w, nil)
	must(t, err)
	if preview.Token == "" || len(preview.Rows) != 1 || preview.Rows[0].Reason != "" {
		t.Fatalf("cleanup preview: %+v", preview)
	}
	result, err := s.ConfirmWorktreeCleanup(t.Context(), y.Name, y.HiredAt, preview.Token, nil)
	must(t, err)
	if len(result) != 1 || !result[0].Removed || result[0].Reason != "" {
		t.Fatalf("cleanup result: %+v", result)
	}
	target, err = s.PrepareWorkspaceDeletion(y.Name, y.HiredAt)
	must(t, err)
	must(t, s.DeleteWorkspace(t.Context(), target))
	if _, err := os.Stat(y.Path); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal(err)
	}
	for _, ref := range []string{"refs/remotes/origin/main", "refs/yesman/worktrees/" + w.CleanupID} {
		got, err := exec.Command("git", "-C", repo.Path, "rev-parse", "--verify", ref).CombinedOutput()
		if err != nil {
			t.Fatalf("verify %s: %s: %v", ref, got, err)
		}
		if strings.TrimSpace(string(got)) != w.Head {
			t.Fatalf("%s: %s", ref, got)
		}
	}
}

func TestDeleteExactArchiveCohort(t *testing.T) {
	s, y, old := deletionFixture(t)
	for _, stamp := range []string{"20260101-000000", "20260202-000000"} {
		must(t, os.WriteFile(filepath.Join(y.Path, "draft.txt"), []byte(stamp), 0600))
		archive, err := s.Dismiss(y.Name)
		must(t, err)
		must(t, os.Rename(archive, filepath.Join(s.app.home.Archive(), y.Name+"-"+stamp)))
		y, err = s.HireNamed(t.Context(), "Paper Kites")
		must(t, err)
	}
	list, err := s.StoredWorkspaces()
	must(t, err)
	if len(list) != 2 || list[0].Instance == list[1].Instance || list[0].Label != list[1].Label {
		t.Fatal(list)
	}
	if err := s.DeleteWorkspace(t.Context(), old); err == nil {
		t.Fatal("stale active confirmation")
	}
	first := filepath.Join(s.app.home.Archive(), list[0].Entry)
	must(t, s.DeleteWorkspace(t.Context(), list[0]))
	if _, err := os.Stat(first); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal(err)
	}
	other := filepath.Join(s.app.home.Archive(), list[1].Entry)
	if got := string(mustReadFile(t, filepath.Join(other, "draft.txt"))); got != "20260202-000000" {
		t.Fatal(got)
	}
	// Even copying back the very same metadata under the original entry name
	// does not recreate its directory incarnation.
	must(t, os.CopyFS(first, os.DirFS(other)))
	meta := filepath.Join(first, ".yesman", "yesman.json")
	must(t, os.WriteFile(meta, []byte(`{"displayName":"Paper Kites","hiredAt":"`+list[0].HiredAt.Format("2006-01-02T15:04:05.999999999Z07:00")+`"}`), 0600))
	if err := s.DeleteWorkspace(t.Context(), list[0]); err == nil {
		t.Fatal("replacement archive was deleted")
	}
	for _, name := range []string{"../repos", ".", "", "a/../b", "paper-kites-20260233-111111", filepath.Join(first, ".yesman")} {
		bad := list[0]
		bad.Entry = name
		if err := s.DeleteWorkspace(t.Context(), bad); err == nil {
			t.Fatalf("accepted %q", name)
		}
	}
	if got, err := s.app.yesmen.Get(y.Name); err != nil || got != y {
		t.Fatal(got, err)
	}
}

func TestDeleteWorkspaceBlockers(t *testing.T) {
	for _, tc := range []struct {
		name, reason string
		setup        func(*testing.T, *service, yesmen.Yesman)
	}{
		{"unknown directory", "private-folder", func(t *testing.T, _ *service, y yesmen.Yesman) {
			must(t, os.Mkdir(filepath.Join(y.Path, "private-folder"), 0700))
		}},
		{"nested checkout", "Git checkout", func(t *testing.T, _ *service, y yesmen.Yesman) {
			must(t, os.MkdirAll(filepath.Join(y.MetaDir(), "tmp", "nested", ".git"), 0700))
		}},
		{"bare Git data", "Git repository data", func(t *testing.T, _ *service, y yesmen.Yesman) {
			p := filepath.Join(y.MetaDir(), "tmp", "bare")
			must(t, os.MkdirAll(filepath.Join(p, "objects"), 0700))
			must(t, os.WriteFile(filepath.Join(p, "HEAD"), nil, 0600))
		}},
		{"unrecovered registration", "task \"t1\"", func(t *testing.T, s *service, y yesmen.Yesman) {
			must(t, s.app.yesmen.SaveWorktreeRecords(y.Name, []yesmen.WorktreeRecord{{Dir: "missing-tree", Owner: "worker", Purpose: "author", Task: "t1"}}))
		}},
		{"unreadable registry", "worktrees.json", func(t *testing.T, _ *service, y yesmen.Yesman) {
			must(t, os.WriteFile(filepath.Join(y.MetaDir(), "worktrees.json"), []byte("{"), 0600))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, y, target := deletionFixture(t)
			tc.setup(t, s, y)
			err := s.DeleteWorkspace(t.Context(), target)
			if err == nil || !strings.Contains(err.Error(), tc.reason) {
				t.Fatalf("got %v", err)
			}
			if got, err := s.app.yesmen.Get(y.Name); err != nil || got != y {
				t.Fatal(got, err)
			}
		})
	}
}

func TestDeleteUnlinksExternalLinkOnly(t *testing.T) {
	s, y, target := deletionFixture(t)
	outside := t.TempDir()
	file := filepath.Join(outside, "keep")
	must(t, os.WriteFile(file, []byte("outside"), 0600))
	linktest.Symlink(t, outside, filepath.Join(y.Path, "link"))
	must(t, s.DeleteWorkspace(t.Context(), target))
	if got := string(mustReadFile(t, file)); got != "outside" {
		t.Fatal(got)
	}
}

func TestDeleteRefusesLinkedRoots(t *testing.T) {
	for _, which := range []string{"yesmen", "archive", "workspace", "metadata"} {
		t.Run(which, func(t *testing.T) {
			s, y, target := deletionFixture(t)
			var p string
			switch which {
			case "yesmen":
				p = s.app.home.Yesmen()
			case "archive":
				_, err := s.Dismiss(y.Name)
				must(t, err)
				list, err := s.StoredWorkspaces()
				must(t, err)
				target = list[0]
				p = s.app.home.Archive()
			case "workspace":
				p = y.Path
			case "metadata":
				p = y.MetaDir()
			}
			kept := filepath.Join(t.TempDir(), "kept")
			must(t, os.Rename(p, kept))
			linktest.Symlink(t, kept, p)
			if err := s.DeleteWorkspace(context.Background(), target); err == nil {
				t.Fatal("followed " + which + " link")
			}
			if _, err := os.Stat(kept); err != nil {
				t.Fatal(err)
			}
		})
	}
}
