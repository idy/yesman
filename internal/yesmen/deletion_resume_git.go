package yesmen

import (
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"strings"

	"github.com/idy/yesman/internal/safefs"
)

// A durable approval identifies the Git data that was admitted before the
// first unlink. It does not certify current usage or dependencies. Check the
// surviving controls after the process scan and again on entry to each admin.
func (f *ForceDeletion) checkGitRemoval(ctx context.Context, ws *os.Root) error {
	if f == nil || (len(f.git) == 0 && f.approval == nil) {
		return nil
	}
	review, err := newLocalGitReview(ctx, ws)
	if err != nil {
		return err
	}
	defer review.root.Close()
	for rel, pointer := range f.approvedModulePointers() {
		dir, err := review.open(rel)
		if f.approval != nil && errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return holdGitMarker(ws, path.Join(rel, ".git"))
		}
		data, err := review.read(dir, ".git", gitDeletionFileLimit)
		dir.Close()
		if f.approval != nil && errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil || sha256.Sum256(data) != pointer {
			return holdGitMarker(ws, path.Join(rel, ".git"))
		}
	}
	for admin, config := range f.approvedGitConfigs() {
		if err := review.checkRemovalDependencies(admin, config, f.approval != nil); err != nil {
			return err
		}
	}
	return nil
}

func (r *localGitReview) checkRemovalDependencies(admin string, config [32]byte, partial bool) error {
	if err := r.ctx.Err(); err != nil {
		return err
	}
	dir, err := r.open(admin)
	if partial && errors.Is(err, fs.ErrNotExist) {
		return nil // the authenticated inventory also checks surviving names
	}
	if err != nil {
		return holdGitData(r.ws, admin)
	}
	defer dir.Close()
	if data, err := r.read(dir, "config", 64<<10); err == nil {
		if sha256.Sum256(data) != config {
			return holdGitData(r.ws, path.Join(admin, "config"))
		}
	} else if !partial || !errors.Is(err, fs.ErrNotExist) {
		return holdGitData(r.ws, path.Join(admin, "config"))
	}
	for _, name := range []string{"worktrees", "commondir", "gitdir", "config.worktree", "config.lock", "shallow.lock", "index.lock", "packed-refs.lock", "HEAD.lock"} {
		if !absentGitEntry(dir, name) {
			return holdGitData(r.ws, path.Join(admin, name))
		}
	}
	for _, child := range []string{"objects/info", "objects/pack"} {
		rel := path.Join(admin, child)
		d, err := r.open(rel)
		if partial && errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return holdGitData(r.ws, rel)
		}
		err = checkObjectsDependencies(r.ctx, d, child)
		d.Close()
		if err != nil {
			return holdGitData(r.ws, rel)
		}
	}
	return nil
}

func checkObjectsDependencies(ctx context.Context, dir *safefs.Dir, child string) error {
	if child == "objects/info" {
		for _, name := range []string{"alternates", "http-alternates"} {
			if !absentGitEntry(dir, name) {
				return errCheckoutPreviewChanged
			}
		}
		return nil
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		names, err := dir.ReadNames(128)
		for _, name := range names {
			if strings.HasSuffix(strings.ToLower(name), ".promisor") {
				return errCheckoutPreviewChanged
			}
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
	}
}
