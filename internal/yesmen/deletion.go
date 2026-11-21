package yesmen

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/idy/yesman/internal/safefs"
)

// DeletionTarget names the exact directory shown in a confirmation. Entry is
// a single store-issued name, including the UTC suffix for a legacy archive.
// A pending entry is a detached directory left by an interrupted deletion.
// No timer or startup pass consumes it.
type DeletionTarget struct {
	Entry    string    `json:"entry"`
	Archived bool      `json:"archived"`
	Instance string    `json:"instance"`
	HiredAt  time.Time `json:"hiredAt"`
	Label    string    `json:"label"`
	Pending  bool      `json:"pending,omitempty"`
	Problem  string    `json:"problem,omitempty"` // a list diagnostic, never actionable
}

// DeletionIssue keeps recovery instructions separate from the filenames and
// operating-system error, so the window can translate them without parsing text.
type DeletionIssue struct {
	Message string
	Args    []any
	Cause   error
}

func (e *DeletionIssue) Error() string { return e.Text(fmt.Sprintf) }
func (e *DeletionIssue) Unwrap() error { return e.Cause }
func (e *DeletionIssue) Text(translate func(string, ...any) string) string {
	message := translate(e.Message, e.Args...)
	if nested, ok := e.Cause.(*DeletionIssue); ok {
		return message + " " + nested.Text(translate)
	}
	if e.Cause != nil {
		return message + " " + e.Cause.Error()
	}
	return message
}

func deletionIssue(message string, args ...any) error {
	return &DeletionIssue{Message: message, Args: args}
}

const deletingPrefix = ".deleting-"
const archiveStamp = "20060102-150405"

// deletionEntry admits only internal yesman names, Store.Archive's exact
// spelling, or this operation's own staging name for one of those entries.
func deletionEntry(entry string, archived bool) (original, name, stagedID string, ok bool) {
	original = entry
	if rest, pending := strings.CutPrefix(entry, deletingPrefix); pending {
		var found bool
		stagedID, original, found = strings.Cut(rest, ".")
		if !found || len(stagedID) != 32 {
			return
		}
		if _, err := hex.DecodeString(stagedID); err != nil {
			return
		}
	}
	name = original
	if archived {
		i := len(original) - len(archiveStamp) - 1
		if i < 1 || original[i] != '-' {
			return
		}
		if _, err := time.Parse(archiveStamp, original[i+1:]); err != nil {
			return
		}
		name = original[:i]
	}
	ok = ValidName(name) == nil
	return
}

// The configured data folder may be an alias. Its yesmen/ and archive/
// children, every workspace and .yesman itself must be real directories.
func (s *Store) deletionParent(archived bool) (*os.Root, error) {
	path := s.root
	if archived {
		path = s.archive
	}
	home, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	defer home.Close()
	group, err := directDir(home, filepath.Base(path))
	if err != nil {
		return nil, err
	}
	if err := checkChildMount(home, group); err != nil {
		group.Close()
		return nil, err
	}
	return group, nil
}

// Open one component and compare the handle with the direct directory entry.
// All subsequent enumeration and removal are relative to that handle.
func directDir(parent *os.Root, name string) (*os.Root, error) {
	st, err := parent.Lstat(name)
	if err != nil {
		return nil, err
	}
	if st.Mode().Type() != fs.ModeDir {
		return nil, deletionIssue("Check %s. It must be a directory inside the data folder.", name)
	}
	dir, err := parent.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	opened, err := dir.Stat(".")
	if err != nil || !os.SameFile(st, opened) {
		dir.Close()
		return nil, fmt.Errorf("%s changed while opening it", name)
	}
	return dir, nil
}

func directoryIdentity(root *os.Root) (safefs.Identity, error) {
	f, err := root.Open(".")
	if err != nil {
		return safefs.Identity{}, err
	}
	defer f.Close()
	return safefs.FileIdentity(f)
}

// The device and mount are compared live. Only the inode and birth time go
// into the persistent staging identity, since device numbers change on reboot.
func sameMount(a, b safefs.Identity) bool {
	return a.Dev == b.Dev && a.Mount == b.Mount && (runtime.GOOS != "linux" || a.Mount != 0)
}

func checkChildMount(parent, child *os.Root) error {
	p, err := directoryIdentity(parent)
	if err != nil {
		return err
	}
	c, err := directoryIdentity(child)
	if err != nil {
		return err
	}
	if !sameMount(p, c) {
		return deletionIssue("Unmount %s before deleting this workspace.", child.Name())
	}
	return nil
}

func (s *Store) deletionTarget(parent *os.Root, entry string, archived bool) (DeletionTarget, error) {
	original, name, stagedID, ok := deletionEntry(entry, archived)
	t := DeletionTarget{Entry: entry, Archived: archived, Pending: stagedID != "", Label: name}
	if !ok {
		return t, errors.New("invalid workspace record")
	}
	ws, err := directDir(parent, entry)
	if err != nil {
		return t, err
	}
	defer ws.Close()
	return s.deletionIdentity(parent, ws, t, original, stagedID)
}

func (s *Store) deletionIdentity(parent, ws *os.Root, t DeletionTarget, original, stagedID string) (DeletionTarget, error) {
	parentID, err := directoryIdentity(parent)
	if err != nil {
		return t, err
	}
	id, err := directoryIdentity(ws)
	if err != nil {
		return t, err
	}
	if !id.Usable() || !sameMount(id, parentID) {
		return t, errors.New("cannot verify the workspace directory identity on this filesystem")
	}
	sum := sha256.Sum256(fmt.Appendf(nil, "%t/%s/%d/%d/%d/%d", t.Archived, original, parentID.Ino, parentID.Birth, id.Ino, id.Birth))
	t.Instance = hex.EncodeToString(sum[:16])
	if stagedID != "" {
		if stagedID != t.Instance {
			return t, errors.New("the interrupted deletion directory was replaced")
		}
		// Some or all metadata may already be gone. Its directory identity
		// remains independently verifiable even in the final empty state.
		return t, nil
	}
	meta, err := directDir(ws, ".yesman")
	if err != nil {
		return t, err
	}
	defer meta.Close()
	if err := checkChildMount(ws, meta); err != nil {
		return t, err
	}
	raw, err := readRegular(meta, "yesman.json", maxYesmanMetadataBytes, &s.metaMu)
	if errors.Is(err, fs.ErrNotExist) {
		return t, nil
	} // pre-metadata workspace
	if err != nil {
		return t, fmt.Errorf("read workspace identity: %w", err)
	}
	var m *struct {
		HiredAt     time.Time `json:"hiredAt"`
		DisplayName string    `json:"displayName"`
	}
	if err := json.Unmarshal(raw, &m); err != nil || m == nil {
		return t, errors.New("cannot read the workspace identity in yesman.json")
	}
	t.HiredAt = m.HiredAt
	if m.DisplayName != "" {
		t.Label = m.DisplayName
	}
	return t, nil
}

// PrepareDeletion binds the active workspace that opened a menu. Nothing is
// stopped, written or reserved when preparing or cancelling a confirmation.
func (s *Store) PrepareDeletion(name string, since time.Time) (DeletionTarget, error) {
	if err := ValidName(name); err != nil {
		return DeletionTarget{}, err
	}
	parent, err := s.deletionParent(false)
	if err != nil {
		return DeletionTarget{}, err
	}
	defer parent.Close()
	t, err := s.deletionTarget(parent, name, false)
	if err == nil && !t.HiredAt.Equal(since) {
		err = errors.New("the workspace was replaced. Reopen its menu")
	}
	return t, err
}

// StoredWorkspaces is an on-demand list for Settings. Only direct archive
// records and interrupted deletions are opened. An unreadable entry stays
// visible with its own diagnostic and cannot grant deletion authority.
func (s *Store) StoredWorkspaces() ([]DeletionTarget, error) {
	out := []DeletionTarget{}
	for _, archived := range []bool{true, false} {
		parent, err := s.deletionParent(archived)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		entries, err := fs.ReadDir(parent.FS(), ".")
		if err == nil {
			for _, e := range entries {
				_, _, staged, ok := deletionEntry(e.Name(), archived)
				if !ok || (!archived && staged == "") {
					continue
				}
				t, err := s.deletionTarget(parent, e.Name(), archived)
				if err != nil {
					t.Instance, t.Problem = "", err.Error()
				}
				out = append(out, t)
			}
		}
		parent.Close()
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// Delete takes Archive's exclusive lifecycle lock for a live hire. A checkout
// admitted earlier can still finish during Stop, so all guards run again after
// retirement. The one rename detaches the confirmed instance before the first
// unlink. A new hire can reserve the old path from then on. Cleanup operates
// solely on the staged instance. detached reports a committed live removal
// even when the remaining bytes still need attention.
func (s *Store) Delete(ctx context.Context, want DeletionTarget, registrations func(context.Context, string) error, stop func(Yesman) error, relocated ...RelocatedPrimary) (detached bool, err error) {
	original, name, staged, ok := deletionEntry(want.Entry, want.Archived)
	if registrations == nil || (!want.Archived && staged == "" && stop == nil) {
		return false, errors.New("workspace deletion requires worktree and agent lifecycle checks")
	}
	if !ok || want.Problem != "" || want.Instance == "" {
		return false, errors.New("invalid workspace deletion confirmation")
	}
	// A retry on detached bytes must serialize with the first purge, but
	// must never drain or lock a new live hire of the same name.
	l := s.lockFor(fmt.Sprintf("deletion/%t/%s", want.Archived, original))
	l.Lock()
	defer l.Unlock()
	var release func()
	if !want.Archived && staged == "" {
		live := s.lockFor(name)
		live.Lock()
		release = sync.OnceFunc(live.Unlock)
		defer release()
	}
	parent, err := s.deletionParent(want.Archived)
	if err != nil {
		return false, err
	}
	defer parent.Close()
	check := func() error {
		now, err := s.deletionTarget(parent, want.Entry, want.Archived)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return errors.New("This workspace is no longer here. Reopen its menu.")
			}
			return err
		}
		if now.Instance != want.Instance || now.Label != want.Label || now.Pending != want.Pending || !now.HiredAt.Equal(want.HiredAt) {
			return errors.New("the workspace changed since confirmation. Reopen its menu")
		}
		return ctx.Err()
	}
	if err := check(); err != nil {
		return false, err
	}
	preflight := func(phase deletionWalk) error {
		ws, err := directDir(parent, want.Entry)
		if err != nil {
			return err
		}
		defer ws.Close()
		if err := registrations(ctx, ws.Name()); err != nil {
			return err
		}
		if err := walkDeletion(ctx, ws, phase); err != nil {
			return err
		}
		return s.checkWorktreeRecords(ctx, parent, ws, want, relocated)
	}
	if err := preflight(inspectDeletion); err != nil {
		return false, err
	}
	if !want.Archived && staged == "" {
		if err := check(); err != nil {
			return false, err
		}
		if err := stop(Yesman{Name: name, Path: filepath.Join(parent.Name(), name), HiredAt: want.HiredAt}); err != nil {
			return false, &DeletionIssue{Message: "The agents could not finish saving. Check the workspace and try again.", Cause: err}
		}
	}
	entry := want.Entry
	if staged == "" {
		// Recheck after draining. Prepare verified cache directories before
		// detaching, so a permission refusal leaves the original recoverable.
		if err := check(); err != nil {
			return false, err
		}
		if err := preflight(prepareDeletion); err != nil {
			return false, err
		}
		entry = deletingPrefix + want.Instance + "." + original
		// Hire, worker metadata and board transactions share this mutex. Keep
		// their paths valid through the move, then release it before the purge.
		err := func() error {
			s.mu.Lock()
			defer s.mu.Unlock()
			if _, err := parent.Lstat(entry); !errors.Is(err, fs.ErrNotExist) {
				return fmt.Errorf("%s is occupied or unreadable. Check interrupted deletions in Storage", entry)
			}
			if err := check(); err != nil {
				return err
			}
			return parent.Rename(want.Entry, entry)
		}()
		if err != nil {
			return false, err
		}
		detached = !want.Archived
		if release != nil {
			release()
		}
	}
	// Reopen and verify the moved object before touching any bytes, including
	// on retry after process restart. Never RemoveAll a reusable name.
	err = s.purgeDetached(ctx, parent, entry, want.Archived)
	if err != nil {
		return detached, &DeletionIssue{Message: "Deletion is incomplete at %s. Check the remaining files, then retry in Settings → Storage → Stored workspaces.", Args: []any{filepath.Join(parent.Name(), entry)}, Cause: err}
	}
	return detached, nil
}

func (s *Store) purgeDetached(ctx context.Context, parent *os.Root, entry string, archived bool) error {
	ws, err := directDir(parent, entry)
	if err != nil {
		return err
	}
	defer ws.Close()
	original, _, stageID, ok := deletionEntry(entry, archived)
	if !ok || stageID == "" {
		return errors.New("invalid interrupted deletion record")
	}
	if _, err := s.deletionIdentity(parent, ws, DeletionTarget{Archived: archived}, original, stageID); err != nil {
		return err
	}
	st, err := ws.Stat(".")
	if err != nil {
		return err
	}
	if now, err := parent.Lstat(entry); err != nil || !os.SameFile(st, now) {
		return errors.New("the detached directory changed")
	}
	if err := walkDeletion(ctx, ws, purgeDeletion); err != nil {
		return err
	}
	if now, err := parent.Lstat(entry); err != nil || !os.SameFile(st, now) {
		return errors.New("the detached directory changed")
	}
	return parent.Remove(entry) // empty directory only
}

// RelocatedPrimary proves the registered checkout now lives outside the selected
// archive. Callers without access to the pool keep the original refusal.
type RelocatedPrimary func(context.Context, string, WorktreeRecord) (string, error)

func (s *Store) checkWorktreeRecords(ctx context.Context, parent, ws *os.Root, target DeletionTarget, relocated []RelocatedPrimary) error {
	meta, err := directDir(ws, ".yesman")
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	} // final stage of cleanup
	if err != nil {
		return err
	}
	defer meta.Close()
	raw, _, err := readRegularEntry(meta, "worktrees.json", maxWorktreeState, &s.metaMu, true)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("check .yesman/worktrees.json before deleting: %w", err)
	}
	var records []json.RawMessage
	if err := json.Unmarshal(raw, &records); err != nil {
		return fmt.Errorf("check .yesman/worktrees.json before deleting: %w", err)
	}
	for _, record := range records {
		var r WorktreeRecord
		if err := json.Unmarshal(record, &r); err != nil {
			return fmt.Errorf("check .yesman/worktrees.json before deleting: %w", err)
		}
		if r.Removed {
			continue
		}
		if r.Purpose == "primary" && len(relocated) == 1 && relocated[0] != nil {
			if !filepath.IsLocal(r.Dir) || filepath.Base(r.Dir) != r.Dir || r.Dir == "." {
				return errors.New("invalid primary directory in the worktree record")
			}
			if _, err := ws.Lstat(r.Dir); !errors.Is(err, fs.ErrNotExist) {
				return deletionIssue("Use Git to move the primary worktree %q outside %s before deleting it.", r.Dir, ws.Name())
			}
			moved, err := relocated[0](ctx, ws.Name(), r)
			if err != nil {
				return &DeletionIssue{Message: "Could not verify the new location of primary worktree %q. Move it with Git and keep its registration.", Args: []any{r.Dir}, Cause: err}
			}
			if err := s.recordPrimaryRelocation(parent, target, record, moved); err != nil {
				return &DeletionIssue{Message: "Could not save the relocation record for %q. Check write permissions and filesystem sync support for %s, then try again.", Args: []any{r.Dir, filepath.Join(parent.Name(), relocationFolder)}, Cause: err}
			}
			continue
		}
		return deletionIssue("Worktree %q (%s, owner %q, task %q) is still recorded. Finish its managed cleanup or recover the checkout and its registration first.", r.Dir, r.Purpose, r.Owner, r.Task)
	}
	return nil
}
