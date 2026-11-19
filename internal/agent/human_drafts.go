package agent

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/idy/yesman/internal/safefs"
)

// Originals are separate from the v2 receipt file so older readers can still
// read receipts and never interpret this file as work to enqueue.
const humanDraftFile = "human-drafts.json"
const maxHumanDraftFile = 40 << 20

func readHumanDrafts(path string) (map[string]RecallPayload, error) {
	dir, err := safefs.Open(filepath.Dir(path))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	data, err := safefs.ReadSmall(dir, humanDraftFile, maxHumanDraftFile)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var drafts map[string]RecallPayload
	if err := json.Unmarshal(data, &drafts); err != nil {
		return nil, errors.New("could not read retained message drafts")
	}
	if len(drafts) > maxRecallRecords {
		return nil, errors.New("too many retained message drafts")
	}
	return drafts, nil
}

func writeHumanDrafts(path string, drafts map[string]RecallPayload) error {
	data, err := json.Marshal(drafts)
	if err != nil {
		return err
	}
	if len(data) > maxHumanDraftFile {
		return errors.New("retained message drafts exceed their byte limit")
	}
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer root.Close()
	var suffix [16]byte
	_, _ = rand.Read(suffix[:])
	tmp := ".human-drafts-" + hex.EncodeToString(suffix[:])
	f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer root.Remove(tmp)
	_, writeErr := f.Write(data)
	if err := errors.Join(writeErr, f.Sync(), f.Close()); err != nil {
		return err
	}
	return root.Rename(tmp, humanDraftFile)
}

func recoverableDelivery(state string) bool {
	return state == "interrupted" || state == "unconfirmed" || state == "failed"
}

func retainsHumanDraft(d Delivery) bool {
	return pendingDelivery(d.State) || recoverableDelivery(d.State) || d.State == "cancelled" || d.AwaitingRecord
}
