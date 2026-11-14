package yesmen

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

var errWorkspaceExists = errors.New("workspace already exists")

// Hire creates a yesman's workspace using the caller's stable internal name.
func (s *Store) Hire(name string) (Yesman, error) { return s.HireAs(name, "") }

// HireAs creates a yesman's workspace, with the model its lead starts as.
// Existing callers retain exact-name validation and conflict semantics.
func (s *Store) HireAs(name, model string) (Yesman, error) {
	if err := ValidName(name); err != nil {
		return Yesman{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hire(name, "", model)
}

// HireNamed creates a workspace for a display name, allocating its stable ID.
// Duplicate labels are allowed; a directory collision gets a numbered suffix.
// Labels, paths and the lead's model are committed together on the first hire.
func (s *Store) HireNamed(label, model string) (Yesman, error) {
	label, err := normalizeDisplayName(label)
	if err != nil {
		return Yesman{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	base := workspaceName(label)
	for n := 1; ; n++ {
		name := base
		if n > 1 {
			suffix := "-" + strconv.Itoa(n)
			name = strings.TrimRight(base[:min(len(base), 32-len(suffix))], "-") + suffix
		}
		y, err := s.hire(name, label, model)
		if errors.Is(err, errWorkspaceExists) {
			continue
		}
		return y, err
	}
}

// workspaceName keeps ASCII letters/digits, folds case and separates runs of
// other characters with a dash. Non-Latin labels use a neutral base. Avoid DOS
// device names too, so an ID allocated on macOS can later be used on Windows.
func workspaceName(label string) string {
	var b strings.Builder
	for _, c := range strings.ToLower(label) {
		if c >= 'a' && c <= 'z' || c >= '0' && c <= '9' {
			b.WriteRune(c)
		} else if b.Len() > 0 && !strings.HasSuffix(b.String(), "-") {
			b.WriteByte('-')
		}
		if b.Len() >= 32 {
			break
		}
	}
	name := strings.TrimRight(b.String(), "-")
	if name == "" {
		return "yesman"
	}
	if name == "con" || name == "prn" || name == "aux" || name == "nul" ||
		len(name) == 4 && (strings.HasPrefix(name, "com") || strings.HasPrefix(name, "lpt")) && name[3] >= '1' && name[3] <= '9' {
		return "yesman-" + name
	}
	return name
}

// hire reserves a new directory exclusively. Never use Stat followed by
// MkdirAll: another Store/process, or a dangling link, may already own it.
// s.mu serializes allocation within this Store; Mkdir covers other callers.
func (s *Store) hire(name, label, model string) (Yesman, error) {
	if err := os.MkdirAll(s.root, 0o755); err != nil {
		return Yesman{}, err
	}
	y := Yesman{Name: name, DisplayName: label, Path: filepath.Join(s.root, name), HiredAt: time.Now().UTC(), Model: model}
	if err := os.Mkdir(y.Path, 0o755); err != nil {
		if errors.Is(err, os.ErrExist) {
			return Yesman{}, fmt.Errorf("a yesman named %s already exists: %w", name, errWorkspaceExists)
		}
		return Yesman{}, fmt.Errorf("create workspace %s: %w", name, err)
	}
	return initializeHire(y)
}

// initializeHire owns the reserved directory. Stage the complete metadata out
// of Get/List's view and publish .yesman with one rename only after every write
// succeeds. Failure removes the reservation, allowing an unchanged retry.
func initializeHire(y Yesman) (_ Yesman, err error) {
	defer func() {
		if err != nil {
			err = errors.Join(err, os.RemoveAll(y.Path))
		}
	}()
	stage, err := os.MkdirTemp(y.Path, ".hire-")
	if err != nil {
		return Yesman{}, err
	}
	defer os.RemoveAll(stage)
	staged := y
	staged.Path = stage
	for _, dir := range []string{staged.KnowledgeDir(), filepath.Join(staged.MetaDir(), "sessions")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return Yesman{}, err
		}
	}
	if err := os.WriteFile(filepath.Join(staged.KnowledgeDir(), "INDEX.md"), []byte(indexTemplate), 0o644); err != nil {
		return Yesman{}, err
	}
	if err := ensureTaskLog(staged); err != nil {
		return Yesman{}, err
	}
	meta := map[string]any{"hiredAt": y.HiredAt}
	if y.Model != "" {
		meta["model"] = y.Model
	}
	if y.DisplayName != "" {
		meta["displayName"] = y.DisplayName
	}
	if err := writeJSON(filepath.Join(staged.MetaDir(), "yesman.json"), meta); err != nil {
		return Yesman{}, err
	}
	if err := os.Rename(staged.MetaDir(), y.MetaDir()); err != nil {
		return Yesman{}, err
	}
	return y, nil
}
