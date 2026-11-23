package yesmen

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
)

const relocationFolder = ".worktree-relocations"

var errRelocationReceiptChanged = deletionIssue("The worktree relocation proof changed. Check the relocated checkout, then review the deletion again.")

type relocationReceipt struct {
	name string
	data []byte
}

type durableRelocationReceipt struct {
	dir  fs.FileInfo
	file fs.FileInfo
}

// Preserve the receipt's encoding so a cold retry can verify and sync the
// same record. The name binds the workspace, original record and destination.
func newRelocationReceipt(target DeletionTarget, original json.RawMessage, destination string) (relocationReceipt, error) {
	entry, _, _, _ := deletionEntry(target.Entry, target.Archived)
	receipt := struct {
		Archive     string          `json:"archive,omitempty"`
		Workspace   string          `json:"workspace,omitempty"`
		Instance    string          `json:"instance"`
		Record      json.RawMessage `json:"record"`
		Destination string          `json:"destination"`
	}{Instance: target.Instance, Record: original, Destination: destination}
	if target.Archived {
		receipt.Archive = entry
	} else {
		receipt.Workspace = entry
	}
	data, err := json.Marshal(receipt)
	if err != nil {
		return relocationReceipt{}, err
	}
	data = append(data, '\n')
	return relocationReceipt{fmt.Sprintf("%s-%x.json", target.Instance, sha256.Sum256(data)), data}, nil
}

func (s *Store) recordPrimaryRelocation(parent *os.Root, receipt relocationReceipt) (durableRelocationReceipt, error) {
	if err := parent.Mkdir(relocationFolder, 0700); err != nil && !errors.Is(err, fs.ErrExist) {
		return durableRelocationReceipt{}, err
	}
	dir, err := openRelocationFolder(parent)
	if err != nil {
		return durableRelocationReceipt{}, err
	}
	defer dir.Close()
	// The folder may have been created on an interrupted attempt. In either
	// case persist its name in the parent before relying on anything inside it.
	if err := s.syncRelocationDir(parent); err != nil {
		return durableRelocationReceipt{}, err
	}
	prior, file, err := readRegularFile(dir, receipt.name, int64(len(receipt.data)), &s.metaMu)
	if errors.Is(err, fs.ErrNotExist) {
		if err := publishRelocationReceipt(dir, receipt.name, receipt.data); err != nil {
			return durableRelocationReceipt{}, err
		}
		prior, file, err = readRegularFile(dir, receipt.name, int64(len(receipt.data)), &s.metaMu)
	}
	if err != nil {
		return durableRelocationReceipt{}, err
	}
	if !bytes.Equal(prior, receipt.data) {
		return durableRelocationReceipt{}, errRelocationReceiptChanged
	}
	folder, err := dir.Stat(".")
	if err != nil {
		return durableRelocationReceipt{}, err
	}
	// A matching existing link may be waiting for this flush after a previous
	// interruption. Publish proof to this operation only after it succeeds.
	if err := s.syncRelocationDir(dir); err != nil {
		return durableRelocationReceipt{}, err
	}
	return durableRelocationReceipt{folder, file}, nil
}

func openRelocationFolder(parent *os.Root) (*os.Root, error) {
	dir, err := directDir(parent, relocationFolder)
	if err != nil {
		return nil, err
	}
	if err := checkChildMount(parent, dir); err != nil {
		dir.Close()
		return nil, err
	}
	return dir, nil
}

// Reuse only an exact receipt whose directory and file were verified during
// a successful flush in this deletion. The final recheck stays read-only.
func (s *Store) verifyRelocationReceipt(parent *os.Root, receipt relocationReceipt, saved durableRelocationReceipt) error {
	if saved.file == nil {
		return errRelocationReceiptChanged
	}
	dir, err := openRelocationFolder(parent)
	if err != nil {
		return errRelocationReceiptChanged
	}
	defer dir.Close()
	folder, err := dir.Stat(".")
	if err != nil || !os.SameFile(saved.dir, folder) {
		return errRelocationReceiptChanged
	}
	data, file, err := readRegularFile(dir, receipt.name, int64(len(receipt.data)), &s.metaMu)
	if err != nil || !sameDeletionEntry(saved.file, file) || !bytes.Equal(data, receipt.data) {
		return errRelocationReceiptChanged
	}
	return nil
}

func syncRelocationDirectory(dir *os.Root) error {
	f, err := dir.Open(".")
	if err != nil {
		return err
	}
	if err := errors.Join(flushReceiptDir(f), f.Close()); err != nil {
		return fmt.Errorf("sync worktree relocation directory %s: %w", dir.Name(), err)
	}
	return nil
}

func publishRelocationReceipt(dir *os.Root, name string, data []byte) error {
	tmp := ".writing-" + rand.Text()
	f, err := dir.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer dir.Remove(tmp)
	_, writeErr := f.Write(data)
	if err := errors.Join(writeErr, f.Sync(), f.Close()); err != nil {
		return err
	}
	return dir.Link(tmp, name)
}
