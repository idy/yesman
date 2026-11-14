package tasklog

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"
)

const retainIntentPath = ".yesman/task-log-retain.json"

type retainIntent struct {
	Version    int      `json:"version"`
	Source     string   `json:"sourceSHA256"`
	Identity   Identity `json:"sourceIdentity"`
	Generation string   `json:"migrationID"`
}
type retainedFile struct {
	data     []byte
	identity Identity
	n        uint64
}

func retentionFile(root *os.Root, name string, m Manifest, changed bool) (retainedFile, error) {
	f, err := openChecked(root, name, 2)
	if err != nil {
		return retainedFile{}, err
	}
	defer f.Close()
	if err = requireIdentity(f.File, m.SourceIdentity); err != nil {
		return retainedFile{}, err
	}
	data, err := readOpened(f, maxSource)
	if err != nil {
		return retainedFile{}, err
	}
	st, err := f.Stat()
	if err != nil {
		return retainedFile{}, err
	}
	n, err := links(f.File)
	if err != nil {
		return retainedFile{}, err
	}
	if !changed && (digest(data) != m.SHA256 || len(data) != m.Bytes || st.ModTime().UTC().Format(time.RFC3339Nano) != m.SourceModTime) {
		return retainedFile{}, errors.New("prepared source content changed; retained bytes are preserved for offline rollback")
	}
	return retainedFile{data: data, identity: m.SourceIdentity, n: n}, nil
}

// Retaining the source is itself exclusive. Unlike Rename, Link cannot replace
// a late source.md occupant. This intent makes its two-name window recoverable.
func retainSource(root *os.Root, m Manifest, o Options) error {
	if _, err := retentionFile(root, TOC, m, false); err != nil {
		return err
	}
	i := retainIntent{2, m.SHA256, m.SourceIdentity, m.MigrationID}
	raw, err := json.Marshal(i)
	if err != nil {
		return err
	}
	if err = put(root, retainIntentPath, append(raw, '\n'), m.SHA256, o.Hook); err != nil {
		return err
	}
	if err = hook(o, "retain-prepared"); err != nil {
		return err
	}
	return recoverRetain(root, m, o)
}

func recoverRetain(root *os.Root, m Manifest, o Options) error {
	raw, err := read(root, retainIntentPath, 4096)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var i retainIntent
	if err = json.Unmarshal(raw, &i); err != nil || i.Version != 2 || i.Source != m.SHA256 || i.Identity != m.SourceIdentity || i.Generation != m.MigrationID {
		return errors.New("retention intent cannot prove this source generation; preserve all paths")
	}
	changed := o.Action == "rollback"
	for attempt := 0; attempt < 2; attempt++ {
		from, fe := retentionFile(root, TOC, m, changed)
		to, te := retentionFile(root, m.Retained, m, changed)
		switch {
		case fe == nil && from.n == 1 && errors.Is(te, os.ErrNotExist):
			if attempt != 0 {
				return errors.New("source paths changed repeatedly during retention")
			}
			if err = hook(o, "retain-before-link"); err != nil {
				return err
			}
			if err = root.Link(TOC, m.Retained); err != nil {
				return err
			}
			if err = hook(o, "retain-linked"); err != nil {
				return err
			}
			continue
		case fe == nil && te == nil && from.n == 2 && to.n == 2 && from.identity == to.identity && bytes.Equal(from.data, to.data):
			// Both names are the persisted source generation. No unknown target is
			// removed, and an extra alias is refused by the verified descriptor read.
			if err = root.Remove(TOC); err != nil {
				return err
			}
			if err = hook(o, "retain-unlinked"); err != nil {
				return err
			}
		case errors.Is(fe, os.ErrNotExist) && te == nil && to.n == 1:
			// Source unlink finished; only intent cleanup was interrupted.
		default:
			return fmt.Errorf("source retention conflicts with existing paths; preserve both: source=%v retained=%v", fe, te)
		}
		if _, err = retentionFile(root, m.Retained, m, changed); err != nil {
			return err
		}
		current, e := read(root, retainIntentPath, 4096)
		if e != nil {
			return e
		}
		if !bytes.Equal(current, raw) {
			return errors.New("retention intent changed during recovery")
		}
		return root.Remove(retainIntentPath)
	}
	return errors.New("source retention did not reach a verified state")
}
