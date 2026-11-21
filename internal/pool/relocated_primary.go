package pool

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/idy/yesman/internal/yesmen"
)

// RelocatedPrimary finds a primary checkout that its owner moved with Git. It
// reads the exact pool repo's registrations, both Git pointers, the original
// private marker and the directory birth identity when the record carries one.
func (p *Pool) RelocatedPrimary(ctx context.Context, workspace string, rec yesmen.WorktreeRecord) (string, error) {
	if rec.Purpose != "primary" || !cleanupIDRE.MatchString(rec.ID) {
		return "", errors.New("missing primary checkout identity")
	}
	r, err := p.RecordedRepo(ctx, rec.Repo)
	if err != nil {
		return "", err
	}
	l := p.lock(r.Name)
	l.Lock()
	defer l.Unlock()
	raw, err := git(ctx, r.Path, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return "", err
	}
	var found string
	for item := range strings.SplitSeq(raw, "\x00\x00") {
		first, fields, _ := strings.Cut(item, "\x00")
		dest, ok := strings.CutPrefix(first, "worktree ")
		if !ok || !filepath.IsAbs(dest) || strings.Contains("\x00"+fields, "\x00prunable") || strings.Contains("\x00"+fields, "\x00bare\x00") || within(realPath(dest), realPath(workspace)) {
			continue
		}
		id, err := readWorktreeInstance(r.Path, dest)
		if err != nil || id != rec.ID {
			continue
		}
		actual, err := rootIdentity(r.Path, dest, id)
		if err != nil {
			return "", err
		}
		if rec.Root != nil && (actual.Ino != rec.Root.Ino || actual.Birth != rec.Root.Birth) {
			return "", errors.New("checkout directory identity changed")
		}
		if found != "" {
			return "", errors.New("multiple registrations match the primary checkout")
		}
		found = dest
	}
	if found == "" {
		return "", fmt.Errorf("registered checkout %s in %s could not be located", rec.ID, r.Name)
	}
	return found, nil
}
