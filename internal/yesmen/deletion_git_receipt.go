package yesmen

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/idy/yesman/internal/safefs"
)

// This record lives inside the verified, detached workspace. It can only
// identify leftover Git registrations in that stage. A fresh confirmation
// still has to authorize every path the user asks Yes Man to delete.
const stagedGitReceipt = ".yesman-deletion-git.json"
const stagedGitReceiptLimit = 2 << 20

type deletionGitDirectory struct {
	Inode uint64 `json:"inode"`
	Birth int64  `json:"birth"`
}

func deletionGitIdentity(id safefs.Identity) deletionGitDirectory {
	return deletionGitDirectory{id.Ino, id.Birth}
}

type deletionGitRegistration struct {
	Private  string               `json:"private"`
	Gitfile  string               `json:"gitfile"`
	Common   deletionGitDirectory `json:"commonIdentity"`
	Instance deletionGitDirectory `json:"privateIdentity"`
	Linked   deletionGitDirectory `json:"linkedIdentity"`
	Forward  [32]byte             `json:"forwardHash"`
	Back     [32]byte             `json:"gitdirHash"`
	Shared   [32]byte             `json:"commondirHash"`
	Removing bool                 `json:"removing,omitempty"`
}

func newDeletionGitRegistration(private, gitfile string, common, instance, linked safefs.Identity, forward, back, shared []byte) deletionGitRegistration {
	return deletionGitRegistration{
		Private: private, Gitfile: gitfile, Common: deletionGitIdentity(common),
		Instance: deletionGitIdentity(instance), Linked: deletionGitIdentity(linked),
		Forward: sha256.Sum256(forward), Back: sha256.Sum256(back), Shared: sha256.Sum256(shared),
	}
}

type deletionGitReceipt struct {
	Stage         string                    `json:"stage"`
	Registrations []deletionGitRegistration `json:"registrations"`
}

func readStagedGitReceipt(ws *os.Root, target DeletionTarget) (map[string]deletionGitRegistration, error) {
	data, _, err := readRegularEntry(ws, stagedGitReceipt, stagedGitReceiptLimit, nil, true)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, unresolvedGitDeletion(stagedGitReceipt)
	}
	var receipt deletionGitReceipt
	if json.Unmarshal(data, &receipt) != nil || receipt.Stage != target.Entry || len(receipt.Registrations) > gitDeletionWorktreeLimit {
		return nil, unresolvedGitDeletion(stagedGitReceipt)
	}
	out := make(map[string]deletionGitRegistration, len(receipt.Registrations))
	for _, registration := range receipt.Registrations {
		if !validStagedGitPath(registration.Gitfile) || path.Base(registration.Gitfile) != ".git" ||
			!validStagedGitPath(registration.Private) || path.Base(path.Dir(registration.Private)) != "worktrees" ||
			registration.Common.Inode == 0 || registration.Common.Birth == 0 || registration.Instance.Inode == 0 || registration.Instance.Birth == 0 ||
			registration.Linked.Inode == 0 || registration.Linked.Birth == 0 || out[registration.Private].Private != "" {
			return nil, unresolvedGitDeletion(stagedGitReceipt)
		}
		out[registration.Private] = registration
	}
	return out, nil
}

func validStagedGitPath(name string) bool {
	return name != "." && fs.ValidPath(name) && filepath.IsLocal(filepath.FromSlash(name)) && !strings.ContainsRune(name, '\\')
}

func encodeDeletionGitReceipt(receipt deletionGitReceipt) ([]byte, error) {
	data, err := json.Marshal(receipt)
	if err != nil || len(data) > stagedGitReceiptLimit {
		return nil, unresolvedGitDeletion(stagedGitReceipt)
	}
	return data, nil
}

func writeStagedGitReceipt(ws *os.Root, receipt deletionGitReceipt) error {
	data, err := encodeDeletionGitReceipt(receipt)
	if err != nil {
		return err
	}
	if err := publishRelocationReceipt(ws, stagedGitReceipt, data); err != nil {
		return &DeletionIssue{Message: "Could not save the interrupted deletion record at %s.", Args: []any{ws.Name()}, Cause: err}
	}
	return syncRelocationDirectory(ws)
}

// Mark intent immediately before removing the exact approved gitfile. A
// cold retry requires this intent as well as the verified path's absence.
func (r *deletionGitReceipt) beginGitfileRemoval(ws, parent *os.Root, rel string, st fs.FileInfo) error {
	for i := range r.Registrations {
		registration := &r.Registrations[i]
		if registration.Gitfile != rel {
			continue
		}
		id, err := directoryIdentity(parent)
		if err != nil || !id.Usable() || deletionGitIdentity(id) != registration.Linked {
			return errCheckoutPreviewChanged
		}
		forward, fi, err := readRegularEntry(parent, path.Base(rel), gitDeletionFileLimit, nil, true)
		if err != nil || !sameDeletionEntry(st, fi) || sha256.Sum256(forward) != registration.Forward {
			return errCheckoutPreviewChanged
		}
		before, err := encodeDeletionGitReceipt(*r)
		if err != nil {
			return err
		}
		existing, fi, err := readRegularEntry(ws, stagedGitReceipt, stagedGitReceiptLimit, nil, true)
		if err != nil || !bytes.Equal(existing, before) {
			return unresolvedGitDeletion(stagedGitReceipt)
		}
		registration.Removing = true
		data, err := encodeDeletionGitReceipt(*r)
		if err != nil {
			return err
		}
		return replaceStagedGitReceipt(ws, fi, data)
	}
	return nil
}

func replaceStagedGitReceipt(ws *os.Root, expected fs.FileInfo, data []byte) error {
	name := ".writing-deletion-" + rand.Text()
	file, err := ws.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer ws.Remove(name)
	_, err = file.Write(data)
	if err := errors.Join(err, file.Sync(), file.Close()); err != nil {
		return err
	}
	if current, err := ws.Lstat(stagedGitReceipt); err != nil || !sameDeletionEntry(expected, current) {
		return unresolvedGitDeletion(stagedGitReceipt)
	}
	if err := ws.Rename(name, stagedGitReceipt); err != nil {
		return err
	}
	return syncRelocationDirectory(ws)
}

func (f *ForceDeletion) checkStagedGitRegistration(ws *os.Root, privateRel string, commonID, privateID safefs.Identity, back, shared []byte) error {
	registration, ok := f.stagedGit[privateRel]
	if !f.target.Pending || !ok || !registration.Removing || !commonID.Usable() || !privateID.Usable() ||
		registration.Common != deletionGitIdentity(commonID) || registration.Instance != deletionGitIdentity(privateID) ||
		registration.Back != sha256.Sum256(back) || registration.Shared != sha256.Sum256(shared) {
		return unresolvedGitDeletion(privateRel)
	}
	// Interpret only the saved staged-relative path. The absolute backpointer
	// can now name a different hire. Never inspect that directory.
	volume, err := directoryIdentity(ws)
	if err != nil {
		return err
	}
	if !stagedGitfileAbsent(ws, registration.Gitfile, volume, registration.Linked) {
		return unresolvedGitDeletion(registration.Gitfile)
	}
	return nil
}

func stagedGitfileAbsent(ws *os.Root, rel string, volume safefs.Identity, linked deletionGitDirectory) bool {
	var visit func(*os.Root, string) bool
	visit = func(dir *os.Root, rest string) bool {
		first, next, hasNext := strings.Cut(rest, "/")
		if !hasNext {
			id, err := directoryIdentity(dir)
			if err != nil || !id.Usable() || (linked.Birth != 0 && linked != deletionGitIdentity(id)) {
				return false
			}
			_, err = dir.Lstat(first)
			return errors.Is(err, fs.ErrNotExist)
		}
		child, err := directDir(dir, first)
		if err != nil {
			return errors.Is(err, fs.ErrNotExist)
		}
		defer child.Close()
		id, err := directoryIdentity(child)
		return err == nil && sameMount(volume, id) && visit(child, next)
	}
	return visit(ws, rel)
}
