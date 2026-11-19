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

// Keep the archive record and its verified destination after deleting the
// archive. A content-addressed, create-only receipt also makes a retry safe.
func (s *Store) recordArchiveRelocation(parent *os.Root, entry, instance string, original json.RawMessage, destination string) error {
	data, err := json.Marshal(struct {
		Archive     string          `json:"archive"`
		Instance    string          `json:"instance"`
		Record      json.RawMessage `json:"record"`
		Destination string          `json:"destination"`
	}{entry, instance, original, destination})
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := parent.Mkdir(relocationFolder, 0700); err != nil && !errors.Is(err, fs.ErrExist) {
		return err
	}
	dir, err := directDir(parent, relocationFolder)
	if err != nil {
		return err
	}
	defer dir.Close()
	if err := checkChildMount(parent, dir); err != nil {
		return err
	}
	// The folder may have been created on an interrupted attempt. In either
	// case persist its name in archive before relying on anything inside it.
	if err := s.syncRelocationDir(parent); err != nil {
		return err
	}
	name := fmt.Sprintf("%s-%x.json", instance, sha256.Sum256(data))
	prior, err := readRegular(dir, name, int64(len(data)), &s.metaMu)
	if err == nil {
		if !bytes.Equal(prior, data) {
			return errors.New("worktree relocation receipt changed")
		}
	} else if errors.Is(err, fs.ErrNotExist) {
		if err := publishRelocationReceipt(dir, name, data); err != nil {
			return err
		}
	} else {
		return err
	}
	// A matching existing link proves its contents but may still be waiting
	// for this flush. Failure leaves the original archive available to retry.
	return s.syncRelocationDir(dir)
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
