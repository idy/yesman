package agent

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"

	"github.com/idy/yesman/internal/safefs"
)

// Originals are separate from the v2 receipt file so older readers can still
// read receipts and never interpret this file as work to enqueue.
const humanDraftFile = "human-drafts.json"
const humanDraftStage = ".human-drafts-v1.stage"
const maxHumanDraftFile = 40 << 20

func openHumanDraftDir(path string) (*safefs.Dir, error) {
	agentDir := filepath.Dir(path)
	workspace := filepath.Dir(filepath.Dir(filepath.Dir(agentDir)))
	root, err := safefs.Open(workspace)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	return safefs.OpenPath(root, []string{".yesman", "sessions", filepath.Base(agentDir)}, nil)
}

func readHumanDrafts(path string) (map[string]RecallPayload, error) {
	dir, err := openHumanDraftDir(path)
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
	if err := json.Unmarshal(data, &drafts); err != nil || drafts == nil {
		return nil, errors.New("could not read retained message drafts")
	}
	if len(drafts) > maxRecallRecords {
		return nil, errors.New("too many retained message drafts")
	}
	return drafts, nil
}

// The application has a single owner and creates a writer for this agent only
// after recovery. A stage surviving startup represents an unpublished snapshot.
func retireHumanDraftStage(path string) error {
	root, err := openHumanDraftRoot(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer root.Close()
	st, err := root.Lstat(humanDraftStage)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !st.Mode().IsRegular() || st.Size() > maxHumanDraftFile || runtime.GOOS != "windows" && st.Mode().Perm() != 0600 {
		return errors.New("unexpected retained draft staging file. Review this agent's session directory before restarting Yes Man")
	}
	return root.Remove(humanDraftStage)
}

func openHumanDraftRoot(path string) (*os.Root, error) {
	dir, err := openHumanDraftDir(path)
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	want, err := dir.Stat()
	if err == nil {
		if got, statErr := root.Stat("."); statErr == nil && os.SameFile(want, got) {
			return root, nil
		}
	}
	root.Close()
	return nil, errors.New("the retained message drafts directory changed")
}

func writeHumanDrafts(path string, drafts map[string]RecallPayload) error {
	data, err := json.Marshal(drafts)
	if err != nil {
		return err
	}
	if len(data) > maxHumanDraftFile {
		return errors.New("retained message drafts exceed their byte limit")
	}
	root, err := openHumanDraftRoot(path)
	if err != nil {
		return err
	}
	defer root.Close()
	f, err := root.OpenFile(humanDraftStage, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer root.Remove(humanDraftStage)
	_, writeErr := f.Write(data)
	if err := errors.Join(writeErr, f.Sync(), f.Close()); err != nil {
		return err
	}
	return root.Rename(humanDraftStage, humanDraftFile)
}

func recoverableDelivery(state string) bool {
	return state == "interrupted" || state == "unconfirmed" || state == "failed"
}

func retainsHumanDraft(d Delivery) bool {
	return pendingDelivery(d.State) || recoverableDelivery(d.State) || d.State == "cancelled" || d.AwaitingRecord
}
