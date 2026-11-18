package tasklog

// One installation intent covers the exclusive Link -> unlink window. It is
// not history and is never injected into context. Recovery runs only under the
// utility lock, with the caller's explicit offline/source-hash acknowledgment.

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"regexp"
	"strings"
)

const installIntentPath = ".yesman/task-log-install.json"

var temporaryName = regexp.MustCompile(`^\.tasklog-[A-Za-z0-9_-]{16,64}\.tmp$`)
var sourceHash = regexp.MustCompile(`^[0-9a-f]{64}$`)
var legacyInstallName = regexp.MustCompile(`^(original|[0-9]{4}|index-[0-9]{4})\.md$`)

type installIntent struct {
	Version   int    `json:"version"`
	Source    string `json:"sourceSHA256"`
	Target    string `json:"target"`
	Temporary string `json:"temporary"`
	SHA256    string `json:"sha256"`
	Bytes     int    `json:"bytes"`
}

func validInstall(i installIntent, expected string) bool {
	if i.Version != 1 || !sourceHash.MatchString(i.Source) || i.Source != expected || i.Bytes < 0 || i.Bytes > maxSource+(1<<20) || !sourceHash.MatchString(i.SHA256) {
		return false
	}
	if path.Dir(i.Temporary) != path.Dir(i.Target) || !temporaryName.MatchString(path.Base(i.Temporary)) {
		return false
	}
	if i.Target == TOC || i.Target == Marker || i.Target == retainIntentPath {
		return true
	}
	if suffix, ok := strings.CutPrefix(i.Target, Dir+"/legacy-"+expected+"-"); ok {
		return legacyInstallName.MatchString(suffix)
	}
	if suffix, ok := strings.CutPrefix(i.Target, generation(expected)+"/"); ok {
		return suffix == "manifest.json" || suffix == "toc.md" || suffix == "rollback-toc.md"
	}
	return false
}

// saveIntent deliberately does not use put: the intent must have one link.
// A partial/failed intent write occurs before any Link and is preserved with
// an explicit recovery error; no unknown file is overwritten or auto-deleted.
func saveIntent(root *os.Root, i installIntent) error {
	if err := Check(root, installIntentPath, true); err != nil {
		return err
	}
	f, err := root.OpenFile(installIntentPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	b, err := json.Marshal(i)
	if err == nil {
		_, err = f.Write(append(b, '\n'))
	}
	return errors.Join(err, f.Sync(), f.Close())
}

// installedFile permits two links only inside this recovery routine, after
// validating a matching installation intent. All ordinary reads still reject
// aliases. Parent/file types, identity, exact count and bytes are checked.
func installedFile(root *os.Root, name string, i installIntent) (os.FileInfo, uint64, error) {
	_, st, n, err := installedContents(root, name, i)
	return st, n, err
}

// Consume the same verified descriptor, including held-parent validation and a
// bounded read. Marker admission also uses these exact bytes before recovery.
func installedContents(root *os.Root, name string, i installIntent) ([]byte, os.FileInfo, uint64, error) {
	if i.Target == Marker && i.Bytes > 1<<20 {
		return nil, nil, 0, errors.New("installation marker exceeds metadata byte limit")
	}
	f, err := openChecked(root, name, 2)
	if err != nil {
		return nil, nil, 0, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, nil, 0, err
	}
	if st.Size() != int64(i.Bytes) {
		return nil, nil, 0, fmt.Errorf("changed install size: %s", name)
	}
	b, err := readOpened(f, int64(i.Bytes))
	if err != nil {
		return nil, nil, 0, err
	}
	n, err := links(f.File)
	if err != nil {
		return nil, nil, 0, err
	}
	if len(b) != i.Bytes || digest(b) != i.SHA256 {
		return nil, nil, 0, fmt.Errorf("install content mismatch: %s", name)
	}
	return b, st, n, nil
}

// admitInstallRecovery is read-only. Never publish an output or consume its
// intent before the persisted source generation and expected hash are admitted.
// A marker may itself be pending installation: strict single-link loadMarker
// cannot read that state, so admit only the exact registered pair and its bytes.
func admitInstallRecovery(root *os.Root, expected string) error {
	raw, err := read(root, installIntentPath, 4096)
	pending := err == nil
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	var i installIntent
	if pending {
		if err = json.Unmarshal(raw, &i); err != nil || !validInstall(i, expected) {
			return errors.New("invalid or unrelated installation intent; preserve it and inspect recovery before retry (no file was removed)")
		}
	}
	var m Manifest
	if pending && i.Target == Marker {
		tb, target, tn, te := installedContents(root, i.Target, i)
		pb, temp, pn, pe := installedContents(root, i.Temporary, i)
		switch {
		case errors.Is(te, os.ErrNotExist) && pe == nil && pn == 1:
			raw = pb
		case te == nil && pe == nil && tn == 2 && pn == 2 && os.SameFile(target, temp):
			raw = tb
		case te == nil && tn == 1 && errors.Is(pe, os.ErrNotExist):
			raw = tb
		default:
			return fmt.Errorf("marker installation conflicts with files; preserve both paths: target=%v temporary=%v", te, pe)
		}
		m, err = decodeMarker(raw)
	} else {
		m, err = loadMarker(root)
		if !pending && errors.Is(err, os.ErrNotExist) {
			return nil // Fresh migration, with no output installation to recover.
		}
	}
	if err != nil {
		return err
	}
	if m.SHA256 != expected {
		return errors.New("prepared source SHA256 differs from expected snapshot; no installation or retention recovery performed")
	}
	return nil
}

func recoverInstall(root *os.Root, expected string) error {
	return recoverInstallState(root, expected, false)
}

func recoverInstallState(root *os.Root, expected string, linked bool) error {
	raw, err := read(root, installIntentPath, 4096)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var i installIntent
	if err = json.Unmarshal(raw, &i); err != nil || !validInstall(i, expected) {
		return errors.New("invalid or unrelated installation intent; preserve it and inspect recovery before retry (no file was removed)")
	}
	target, tn, te := installedFile(root, i.Target, i)
	temp, pn, pe := installedFile(root, i.Temporary, i)
	switch {
	case errors.Is(te, os.ErrNotExist) && pe == nil && pn == 1:
		if linked {
			return errors.New("install target changed repeatedly during recovery")
		}
		if err = root.Link(i.Temporary, i.Target); err != nil {
			return err
		}
		// Re-read the now-linked pair through the same validation path.
		return recoverInstallState(root, expected, true)
	case te == nil && pe == nil && tn == 2 && pn == 2 && os.SameFile(target, temp):
		// Only this registered second name is removed. A third/unrelated alias,
		// changed content, other workspace or linked parent is never accepted.
		if err = root.Remove(i.Temporary); err != nil {
			return err
		}
	case te == nil && tn == 1 && errors.Is(pe, os.ErrNotExist):
		// The process exited after unlink but before clearing the intent.
	default:
		return fmt.Errorf("installation intent conflicts with files; preserve both paths: target=%v temporary=%v", te, pe)
	}
	if _, n, e := installedFile(root, i.Target, i); e != nil || n != 1 {
		return errors.Join(errors.New("installed target was changed during recovery"), e)
	}
	// The registered intent itself must still be ordinary, unaliased and exact.
	current, err := read(root, installIntentPath, 4096)
	if err != nil {
		return err
	}
	if !bytes.Equal(current, raw) {
		return errors.New("installation intent changed during recovery")
	}
	return root.Remove(installIntentPath)
}

// Hex validation remains intentionally independent of path parsing.
func validExpectedSource(s string) bool {
	_, err := hex.DecodeString(s)
	return len(s) == 64 && err == nil && sourceHash.MatchString(s)
}
