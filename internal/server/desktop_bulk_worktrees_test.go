package server

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/idy/yesman/internal/yesmen"
)

func TestDesktopBulkWorktreeDirtyAndLegacy(t *testing.T) {
	for _, kind := range []string{"canceled-no-pr", "closed-pr", "legacy-integration", "dead-pointer"} {
		t.Run(kind, func(t *testing.T) {
			f := newCleanupFixture(t)
			branch := "fix/old-work"
			if kind == "legacy-integration" {
				branch = "integrate/old-local"
			}
			w, err := f.s.checkoutOwned(t.Context(), f.owner, f.y, CheckoutRequest{Repo: f.repo.Name, Dir: "historical", Branch: branch, Base: f.head, Purpose: "author", Task: "t1"})
			if err != nil {
				t.Fatal(err)
			}
			d := &DesktopWorktrees{Server: f.s}
			status := yesmen.TaskDone
			if kind == "canceled-no-pr" || kind == "closed-pr" {
				status = yesmen.TaskDropped
			}
			_, err = f.s.Yesmen.ChangeBoard(f.y.Name, yesmen.BoardChange{Update: []yesmen.TaskEdit{{ID: "t1", Status: &status}}})
			if err != nil {
				t.Fatal(err)
			}
			recs, err := f.s.Yesmen.WorktreeRecords(f.y.Name)
			if err != nil {
				t.Fatal(err)
			}
			if kind == "legacy-integration" {
				recs[0].CreatedFor, recs[0].Root, recs[0].OwnerSince = nil, nil, time.Time{}
			}
			if err := f.s.Yesmen.SaveWorktreeRecords(f.y.Name, recs); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"README", "untracked", "ignored"} {
				if err := os.WriteFile(filepath.Join(w.Path, name), []byte("local bytes"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			privatePath := cleanupGit(t, w.Path, "rev-parse", "--absolute-git-dir")
			var staleAdmin string
			if kind == "dead-pointer" {
				if recs[0].Root == nil {
					t.Fatal("fixture requires a recorded directory identity")
				}
				staleAdmin = filepath.Join(t.TempDir(), "kept-admin")
				if err := os.Rename(privatePath, staleAdmin); err != nil {
					t.Fatal(err)
				}
			} else if kind != "legacy-integration" {
				previewTree(t, d, f.y, &w, false)
			}
			before := cleanupGit(t, f.repo.Path, "for-each-ref", "--format=%(refname) %(objectname)")
			p := previewTree(t, d, f.y, nil, true)
			previewDir, err := os.Stat(p.Rows[0].Path)
			actualDir, actualErr := os.Stat(w.Path)
			if err != nil || actualErr != nil || !os.SameFile(previewDir, actualDir) || p.Rows[0].TaskStatus != status {
				t.Fatalf("preview selected a different physical checkout: %+v, %v, %v", p, err, actualErr)
			}
			if _, err := os.Stat(filepath.Join(w.Path, "ignored")); err != nil {
				t.Fatal("preview wrote files", err)
			}
			confirmTree(t, d, f.y, p.Token, true)
			if _, err := os.Lstat(w.Path); !os.IsNotExist(err) {
				t.Fatal("directory remains", err)
			}
			if _, err := os.Lstat(privatePath); !os.IsNotExist(err) {
				t.Fatal("Git private directory remains", err)
			}
			if registered, err := f.s.Pool.HasWorktreeRegistration(t.Context(), w.Repo, w.Path); err != nil || registered {
				t.Fatal("registration remains", registered, err)
			}
			if staleAdmin != "" {
				if data, err := os.ReadFile(filepath.Join(staleAdmin, "HEAD")); err != nil || string(data) != "ref: refs/heads/"+branch+"\n" {
					t.Fatal("stale private directory changed", string(data), err)
				}
			}
			if after := cleanupGit(t, f.repo.Path, "for-each-ref", "--format=%(refname) %(objectname)"); before != after {
				t.Fatal("cleanup changed shared refs")
			}
			b, err := f.s.Yesmen.Board(f.y.Name)
			if err != nil || b.Tasks[0].Status != status {
				t.Fatal("cleanup modified task status", b, err)
			}
			recs, err = f.s.Yesmen.WorktreeRecords(f.y.Name)
			if err != nil || !recs[0].Removed || recs[0].Completion != nil || recs[0].CleanupHead != "" {
				t.Fatal("cleanup fabricated recovery evidence", recs, err)
			}
		})
	}
}

func TestDesktopBulkWorktreeHoldsLateUseAndReplacement(t *testing.T) {
	f := newCleanupFixture(t)
	w := f.checkout(t, "author", "historical")
	f.finishTask(t)
	d := &DesktopWorktrees{Server: f.s}
	p := previewTree(t, d, f.y, nil, true)
	release, err := f.s.Pool.HoldWorktreeEditor(w.Path, w.Repo, w.CleanupID)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if row := confirmTree(t, d, f.y, p.Token, false); !strings.Contains(row.Reason, "editor is open") {
		t.Fatal(row)
	}
	release()
	if runtime.GOOS == "darwin" || runtime.GOOS == "linux" {
		p = previewTree(t, d, f.y, nil, true)
		child := exec.Command(os.Args[0], "-test.run=^TestDesktopBulkActiveProcessFixture$")
		child.Dir, child.Env = w.Path, append(os.Environ(), "YESMAN_BULK_HOLD_CWD=1")
		out, err := child.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		in, err := child.StdinPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err := child.Start(); err != nil {
			t.Fatal(err)
		}
		func() {
			defer func() { in.Close(); child.Wait() }()
			if got, err := bufio.NewReader(out).ReadString('\n'); got != "ready\n" || err != nil {
				t.Fatal(got, err)
			}
			if row := confirmTree(t, d, f.y, p.Token, false); !strings.Contains(row.Reason, "process is using") {
				t.Fatal(row)
			}
		}()
	}
	p = previewTree(t, d, f.y, nil, true)
	if err := os.Rename(w.Path, w.Path+".protected"); err != nil {
		t.Fatal(err)
	}
	if err := os.CopyFS(w.Path, os.DirFS(w.Path+".protected")); err != nil {
		t.Fatal(err)
	}
	confirmTree(t, d, f.y, p.Token, false)
	for _, dir := range []string{w.Path, w.Path + ".protected"} {
		if _, err := os.Stat(filepath.Join(dir, "README")); err != nil {
			t.Fatal("confirmation removed a different directory", err)
		}
	}
}

func TestDesktopBulkActiveProcessFixture(t *testing.T) {
	if os.Getenv("YESMAN_BULK_HOLD_CWD") != "1" {
		return
	}
	fmt.Println("ready")
	_, _ = io.Copy(io.Discard, os.Stdin)
}
