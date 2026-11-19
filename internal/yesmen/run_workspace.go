package yesmen

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/idy/yesman/internal/safefs"
)

// The normal workspace slot reserves the CLI name. It has no .yesman, so
// desktop enumeration and dismissal never follow it into user-owned files.
const rootBindingFile = "run-root.json"

type rootBinding struct {
	Name    string      `json:"name"`
	Path    string      `json:"path"`
	HiredAt time.Time   `json:"hiredAt"`
	info    os.FileInfo // captured at bind/open, checked again by managed writes
}

// RunWorkspace opens a named workspace, or exclusively hires one when create
// is true. An explicit existing directory becomes the workspace itself. Its
// scoped Store uses the directory's real parent for handle-relative metadata
// access and never lists its siblings. Call while holding the profile lock.
func (s *Store) RunWorkspace(name, root, model string, create bool) (*Store, Yesman, error) {
	if err := ValidName(name); err != nil {
		return nil, Yesman{}, err
	}
	if root != "" {
		if !create {
			return nil, Yesman{}, errors.New("--root requires --new; resume by workspace name")
		}
		return s.attachRoot(name, root, model)
	}
	if create {
		y, err := s.HireAs(name, model)
		return s, y, err
	}
	// Open the slot itself without following a link. A normal workspace can
	// contain arbitrary project files, including one named run-root.json.
	slot, err := openRunDir(filepath.Join(s.root, name))
	if err != nil {
		return nil, Yesman{}, err
	}
	defer slot.Close()
	if meta, err := slot.OpenDir(".yesman"); err == nil {
		meta.Close()
		y, err := s.Get(name)
		return s, y, err
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, Yesman{}, err
	}
	data, err := safefs.ReadSmall(slot, rootBindingFile, 16<<10)
	if err != nil {
		return nil, Yesman{}, fmt.Errorf("open workspace %s: %w", name, err)
	}
	var b rootBinding
	if json.Unmarshal(data, &b) != nil || b.Name != name || !filepath.IsAbs(b.Path) || b.HiredAt.IsZero() {
		return nil, Yesman{}, fmt.Errorf("invalid task root registration for %s", name)
	}
	bound, err := s.boundTo(b)
	if err != nil {
		return nil, Yesman{}, err
	}
	y, err := bound.Get(name)
	return bound, y, err
}

// The selected parent may use a system alias, such as /var on macOS.
// The workspace entry itself must be a real directory in that parent.
func openRunDir(path string) (*safefs.Dir, error) {
	parent, err := safefs.Open(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	defer parent.Close()
	return parent.OpenDir(filepath.Base(path))
}

func (s *Store) boundTo(b rootBinding) (*Store, error) {
	dir, err := openRunDir(b.Path)
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	if b.info, err = dir.Stat(); err != nil {
		return nil, err
	}
	meta, err := dir.OpenDir(".yesman")
	if err != nil {
		return nil, err
	}
	meta.Close()
	scoped := New(filepath.Dir(b.Path), s.archive)
	scoped.attached = &b
	return scoped, nil
}

// Read the bound directory's own metadata through the opened handle. Never
// borrow a replaced workspace's generation or follow a metadata alias.
func (b *rootBinding) readMeta(dst any) error {
	dir, err := openRunDir(b.Path)
	if err != nil {
		return err
	}
	defer dir.Close()
	fi, err := dir.Stat()
	if err != nil || !os.SameFile(fi, b.info) {
		return errors.New("the attached task directory was replaced")
	}
	meta, err := dir.OpenDir(".yesman")
	if err != nil {
		return err
	}
	defer meta.Close()
	data, err := safefs.ReadSmall(meta, "yesman.json", maxYesmanMetadataBytes)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, dst)
}

// TaskRootInfo identifies the user-selected directory opened for this run.
func (s *Store) TaskRootInfo() os.FileInfo {
	if s.attached == nil {
		return nil
	}
	return s.attached.info
}

func (s *Store) attachRoot(name, root, model string) (*Store, Yesman, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, Yesman{}, err
	}
	dir, err := openRunDir(abs)
	if err != nil {
		return nil, Yesman{}, err
	}
	defer dir.Close()
	root, err = filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, Yesman{}, err
	}
	profile, err := filepath.EvalSymlinks(filepath.Dir(s.root))
	if err != nil {
		return nil, Yesman{}, err
	}
	inside := func(parent, child string) (bool, error) {
		parentInfo, err := os.Stat(parent)
		if err != nil {
			return false, err
		}
		for {
			info, err := os.Stat(child)
			if err != nil || os.SameFile(info, parentInfo) {
				return err == nil, err
			}
			next := filepath.Dir(child)
			if next == child {
				return false, nil
			}
			child = next
		}
	}
	inProfile, err := inside(profile, root)
	if err != nil {
		return nil, Yesman{}, err
	}
	containsProfile, err := inside(root, profile)
	if err != nil {
		return nil, Yesman{}, err
	}
	if filepath.Dir(root) == root || inProfile || containsProfile {
		return nil, Yesman{}, errors.New("choose a task directory separate from the Yes Man data folder")
	}
	// Reserve both the name and .yesman exclusively, also against another
	// profile attaching the same path. Only newly created metadata is ours.
	slot := filepath.Join(s.root, name)
	if err := os.Mkdir(slot, 0o700); err != nil {
		return nil, Yesman{}, fmt.Errorf("reserve workspace %s: %w", name, err)
	}
	b := rootBinding{Name: name, Path: root, HiredAt: time.Now().UTC()}
	y := Yesman{Name: name, Path: root, Model: model, TaskRoot: true, HiredAt: b.HiredAt}
	if err := writeYesmanMetadata(filepath.Join(slot, rootBindingFile), b); err != nil {
		return nil, Yesman{}, errors.Join(err, os.RemoveAll(slot))
	}
	if err := os.Mkdir(y.MetaDir(), 0o700); err != nil {
		return nil, Yesman{}, errors.Join(fmt.Errorf("reserve fresh task root metadata: %w", err), os.RemoveAll(slot))
	}
	if err := writeHireFiles(y); err != nil {
		return nil, Yesman{}, errors.Join(err, os.RemoveAll(y.MetaDir()), os.RemoveAll(slot))
	}
	bound, err := s.boundTo(b)
	return bound, y, err
}
