package yesmen

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/idy/yesman/internal/safefs"
)

// LocalGitDeletionChecks are supplied only by the desktop. Idle checks for
// observable use. Entry adds the platform's handle-sharing check when needed.
type LocalGitDeletionChecks struct {
	Idle  func(context.Context, string) error
	Entry func(*os.File, string) error
}

type localGitBinding struct {
	admin    string
	id       safefs.Identity
	sum      [32]byte
	pointer  [32]byte
	worktree string
	module   bool
	bare     bool
}

type localGitReview struct {
	ctx            context.Context
	ws             *os.Root
	root           *safefs.Dir
	volume         safefs.Identity
	stores         map[string]localGitBinding // keyed by Git directory
	paths          map[string]localGitBinding // keyed by checkout observation
	moduleCheckout map[string]string
	expected       map[string]localGitBinding // previous validated stores by admin path
}

func newLocalGitReview(ctx context.Context, ws *os.Root) (*localGitReview, error) {
	root, err := safefs.Open(ws.Name())
	if err != nil {
		return nil, err
	}
	id, err := directoryIdentity(ws)
	if err != nil {
		root.Close()
		return nil, err
	}
	actual, err := root.Identity()
	if err != nil || !id.Usable() || id != actual {
		root.Close()
		return nil, errCheckoutPreviewChanged
	}
	return &localGitReview{ctx: ctx, ws: ws, root: root, volume: id, stores: map[string]localGitBinding{}, paths: map[string]localGitBinding{}, moduleCheckout: map[string]string{}}, nil
}

func (f *ForceDeletion) reviewApprovedGit(ctx context.Context, ws *os.Root) (*localGitReview, error) {
	review, err := newLocalGitReview(ctx, ws)
	if err != nil {
		return nil, err
	}
	review.expected = make(map[string]localGitBinding, len(f.git))
	for _, binding := range f.git {
		review.expected[binding.admin] = binding
	}
	return review, nil
}

// The process probe runs after the staged inventory. Recheck the approved
// stores and their intact module pointers before the first unlink.
func (f *ForceDeletion) recheckLocalGit(ctx context.Context, ws *os.Root) error {
	if f == nil || len(f.git) == 0 {
		return nil
	}
	review, err := f.reviewApprovedGit(ctx, ws)
	if err != nil {
		return err
	}
	defer review.root.Close()
	for _, admin := range slices.Sorted(maps.Keys(review.expected)) {
		binding := review.expected[admin]
		if err := review.recheckAdmin(binding); err != nil {
			return err
		}
	}
	for checkout, binding := range f.git {
		if !binding.module || checkout == binding.admin {
			continue
		}
		if err := f.recheckModulePointer(review, checkout, binding); err != nil {
			return err
		}
	}
	return nil
}

func (f *ForceDeletion) recheckModulePointer(review *localGitReview, checkout string, binding localGitBinding) error {
	if err := review.ctx.Err(); err != nil {
		return err
	}
	dir, err := review.open(checkout)
	if err != nil {
		return holdGitData(review.ws, binding.admin)
	}
	defer dir.Close()
	id, err := dir.Identity()
	if err != nil || id != f.paths[checkout].directory {
		return holdGitData(review.ws, binding.admin)
	}
	pointer, err := review.read(dir, ".git", gitDeletionFileLimit)
	if err != nil || sha256.Sum256(pointer) != binding.pointer {
		return holdGitData(review.ws, binding.admin)
	}
	return nil
}

func (r *localGitReview) recheckAdmin(binding localGitBinding) error {
	if _, _, err := r.inspect(binding.admin, binding.bare, binding.module); err != nil {
		if ctxErr := r.ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return holdGitData(r.ws, binding.admin)
	}
	return nil
}

// Follow only ordinary, directly opened directories on the workspace mount.
func (r *localGitReview) open(rel string) (*safefs.Dir, error) {
	dir, err := r.root.Dup()
	if err != nil {
		return nil, err
	}
	if rel == "." {
		return dir, nil
	}
	for _, part := range strings.Split(rel, "/") {
		next, err := dir.OpenDir(part)
		dir.Close()
		if err != nil {
			return nil, err
		}
		id, err := next.Identity()
		if err != nil || !sameMount(id, r.volume) {
			next.Close()
			return nil, errCheckoutPreviewChanged
		}
		dir = next
	}
	return dir, nil
}

func (r *localGitReview) read(dir *safefs.Dir, name string, limit int64) ([]byte, error) {
	file, err := dir.OpenFile(name, os.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	st, err := file.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Size() > limit {
		return nil, errCheckoutPreviewChanged
	}
	id, err := safefs.FileIdentity(file)
	if err != nil || !sameMount(id, r.volume) {
		return nil, errCheckoutPreviewChanged
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || int64(len(data)) > limit {
		return nil, errCheckoutPreviewChanged
	}
	return data, nil
}

func absentGitEntry(dir *safefs.Dir, name string) bool {
	file, err := dir.OpenFile(name, os.O_RDONLY, 0)
	if err == nil {
		file.Close()
	}
	return errors.Is(err, fs.ErrNotExist)
}

func heldLocalGitEntry(dir, name string, admin bool) bool {
	name = strings.ToLower(name)
	if strings.HasSuffix(name, ".lock") || strings.HasSuffix(name, ".promisor") {
		return true
	}
	if admin {
		switch name {
		case "worktrees", "commondir", "gitdir", "config.worktree":
			return true
		}
	}
	return path.Base(dir) == "info" && path.Base(path.Dir(dir)) == "objects" && (name == "alternates" || name == "http-alternates")
}

func gitPointer(data []byte) (string, bool) {
	value, ok := strings.CutPrefix(string(data), "gitdir: ")
	value = strings.TrimRight(value, "\r\n")
	return value, ok && strings.TrimSpace(value) != "" && !strings.ContainsAny(value, "\r\n\x00") && (filepath.IsAbs(value) || filepath.VolumeName(value) == "")
}

func (r *localGitReview) validate(dir *os.Root, ob checkoutObservation) error {
	rel := ob.Path
	if ob.Bare {
		if id, module := r.moduleParent(rel); module {
			binding, err := r.module(rel, id)
			if err != nil {
				return err
			}
			r.paths[rel] = binding
			return nil
		}
		if len(ob.markers) != 2 || ob.markers["HEAD"] == nil || ob.markers["objects"] == nil {
			return holdGitData(r.ws, rel)
		}
		return r.independent(rel, rel, true)
	}
	if len(ob.markers) != 1 || ob.markers[".git"] == nil {
		return checkCheckoutGit(r.ws, dir, ob, rel)
	}
	st := ob.markers[".git"]
	if st.IsDir() {
		if _, module := r.moduleParent(rel); module {
			return holdGitData(r.ws, rel)
		}
		return r.independent(rel, path.Join(rel, ".git"), false)
	}
	if !st.Mode().IsRegular() {
		return checkCheckoutGit(r.ws, dir, ob, rel)
	}
	data, now, err := readRegularEntry(dir, ".git", gitDeletionFileLimit, nil, true)
	if err != nil || !sameDeletionEntry(st, now) {
		return holdGitMarker(r.ws, path.Join(rel, ".git"))
	}
	pointer, ok := gitPointer(data)
	if !ok {
		return holdGitMarker(r.ws, path.Join(rel, ".git"))
	}
	target := pointer
	if !filepath.IsAbs(target) {
		target = filepath.Join(dir.Name(), target)
	}
	admin, local := workspaceGitPath(r.ws.Name(), target)
	if !local {
		return nil
	}
	admin = filepath.ToSlash(admin)
	owner, module := r.moduleParent(admin)
	if !module || filepath.IsAbs(pointer) {
		return holdGitData(r.ws, admin)
	}
	binding, err := r.module(admin, owner)
	if err != nil {
		return err
	}
	if r.moduleCheckout[admin] != rel {
		return holdGitData(r.ws, admin)
	}
	r.paths[rel] = binding
	return nil
}

func (r *localGitReview) moduleParent(admin string) (string, bool) {
	for parent := path.Dir(admin); parent != "." && parent != "/"; parent = path.Dir(parent) {
		if path.Base(parent) == "modules" {
			owner := path.Dir(parent)
			if store, ok := r.stores[owner]; ok && !store.bare {
				return owner, true
			}
		}
	}
	return "", false
}

func (r *localGitReview) independent(rel, admin string, bare bool) error {
	binding, worktree, err := r.inspect(admin, bare, false)
	if err != nil || worktree != "" {
		return holdGitData(r.ws, admin)
	}
	r.stores[admin], r.paths[rel] = binding, binding
	return nil
}

func (r *localGitReview) module(admin, owner string) (localGitBinding, error) {
	if prior, ok := r.stores[admin]; ok {
		return prior, nil
	}
	binding, worktree, err := r.inspect(admin, false, true)
	if err != nil || worktree == "" || filepath.IsAbs(worktree) || filepath.VolumeName(worktree) != "" || strings.ContainsAny(worktree, "\x00\r\n") {
		return localGitBinding{}, holdGitData(r.ws, admin)
	}
	checkout := path.Clean(path.Join(admin, filepath.ToSlash(worktree)))
	ownerCheckout := path.Dir(owner)
	if r.stores[owner].module {
		ownerCheckout = r.moduleCheckout[owner]
	}
	if checkout == "." || !filepath.IsLocal(filepath.FromSlash(checkout)) || checkout == ownerCheckout || (ownerCheckout != "." && !strings.HasPrefix(checkout, ownerCheckout+"/")) {
		return localGitBinding{}, holdGitData(r.ws, admin)
	}
	dir, err := r.open(checkout)
	if err != nil {
		return localGitBinding{}, holdGitData(r.ws, admin)
	}
	defer dir.Close()
	data, err := r.read(dir, ".git", gitDeletionFileLimit)
	pointer, valid := gitPointer(data)
	if err != nil || !valid || filepath.IsAbs(pointer) || path.Clean(path.Join(checkout, filepath.ToSlash(pointer))) != admin {
		return localGitBinding{}, holdGitData(r.ws, admin)
	}
	binding.pointer = sha256.Sum256(data)
	r.stores[admin] = binding
	r.moduleCheckout[admin] = checkout
	return binding, nil
}

func (r *localGitReview) inspect(admin string, bare, module bool) (localGitBinding, string, error) {
	if err := r.ctx.Err(); err != nil {
		return localGitBinding{}, "", err
	}
	dir, err := r.open(admin)
	if err != nil {
		return localGitBinding{}, "", err
	}
	defer dir.Close()
	id, err := dir.Identity()
	if err != nil || !id.Usable() {
		return localGitBinding{}, "", errCheckoutPreviewChanged
	}
	for _, name := range []string{"worktrees", "commondir", "gitdir", "config.worktree", "config.lock", "shallow.lock", "index.lock", "packed-refs.lock", "HEAD.lock"} {
		if !absentGitEntry(dir, name) {
			return localGitBinding{}, "", errCheckoutPreviewChanged
		}
	}
	for _, rel := range []string{"objects", "refs", "objects/info", "objects/pack"} {
		child, err := r.open(path.Join(admin, rel))
		if err != nil {
			return localGitBinding{}, "", err
		}
		if rel == "objects/info" {
			for _, name := range []string{"alternates", "http-alternates"} {
				if !absentGitEntry(child, name) {
					child.Close()
					return localGitBinding{}, "", errCheckoutPreviewChanged
				}
			}
		}
		if rel == "objects/pack" {
			for {
				if err := r.ctx.Err(); err != nil {
					child.Close()
					return localGitBinding{}, "", err
				}
				names, err := child.ReadNames(128)
				for _, name := range names {
					if strings.HasSuffix(strings.ToLower(name), ".promisor") {
						child.Close()
						return localGitBinding{}, "", errCheckoutPreviewChanged
					}
				}
				if errors.Is(err, io.EOF) {
					break
				}
				if err != nil {
					child.Close()
					return localGitBinding{}, "", err
				}
			}
		}
		child.Close()
	}
	head, err := r.read(dir, "HEAD", gitDeletionFileLimit)
	if err != nil || !validLocalHEAD(head) {
		return localGitBinding{}, "", errCheckoutPreviewChanged
	}
	config, err := r.read(dir, "config", 64<<10)
	if err != nil {
		return localGitBinding{}, "", err
	}
	binding := localGitBinding{admin: admin, id: id, sum: sha256.Sum256(append(append(head, 0), config...)), module: module, bare: bare}
	if r.expected != nil {
		want, ok := r.expected[admin]
		if !ok || want.id != binding.id || want.sum != binding.sum || want.bare != bare || want.module != module {
			return localGitBinding{}, "", errCheckoutPreviewChanged
		}
		binding.worktree = want.worktree
	} else {
		binding.worktree, err = localGitConfig(r.ctx, config, bare)
		if err != nil {
			return localGitBinding{}, "", err
		}
	}
	return binding, binding.worktree, nil
}

func validLocalHEAD(data []byte) bool {
	value := strings.TrimSuffix(string(data), "\n")
	value = strings.TrimSuffix(value, "\r")
	if ref, ok := strings.CutPrefix(value, "ref: refs/"); ok {
		return ref != "" && !strings.ContainsAny(ref, " \t\r\n\x00\\")
	}
	return (len(value) == 40 || len(value) == 64) && strings.Trim(value, "0123456789abcdefABCDEF") == ""
}

// Use Git's parser on bounded bytes from the opened file. Git never opens
// repository config, includes or objects for this command.
func localGitConfig(ctx context.Context, data []byte, bare bool) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "config", "--file", "-", "--no-includes", "--null", "--list")
	cmd.Env = []string{"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=" + os.DevNull, "LC_ALL=C"}
	cmd.Stdin = bytes.NewReader(data)
	out, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	if err = cmd.Start(); err != nil {
		return "", err
	}
	const limit = 256 << 10
	parsed, readErr := io.ReadAll(io.LimitReader(out, limit+1))
	out.Close()
	waitErr := cmd.Wait()
	if err := errors.Join(readErr, waitErr); err != nil || len(parsed) > limit {
		return "", fmt.Errorf("cannot read Git configuration: %v", err)
	}
	var worktree string
	seen := map[string]bool{}
	for record := range strings.SplitSeq(string(parsed), "\x00") {
		if record == "" {
			continue
		}
		key, value, hasValue := strings.Cut(record, "\n")
		lower := strings.ToLower(key)
		switch {
		case lower == "core.bare", lower == "core.worktree", lower == "core.repositoryformatversion", strings.HasPrefix(lower, "extensions."):
			if seen[lower] || !hasValue {
				return "", errCheckoutPreviewChanged
			}
			seen[lower] = true
		case lower == "include.path", strings.HasPrefix(lower, "includeif."), strings.HasSuffix(lower, ".promisor"), strings.HasSuffix(lower, ".partialclonefilter"):
			return "", errCheckoutPreviewChanged
		}
		switch lower {
		case "core.bare":
			if value != fmt.Sprint(bare) {
				return "", errCheckoutPreviewChanged
			}
		case "core.worktree":
			worktree = value
			if worktree == "" {
				return "", errCheckoutPreviewChanged
			}
		case "core.repositoryformatversion":
			if value != "0" && value != "1" {
				return "", errCheckoutPreviewChanged
			}
		case "extensions.objectformat":
			if value != "sha1" && value != "sha256" {
				return "", errCheckoutPreviewChanged
			}
		case "extensions.refstorage":
			if value != "files" {
				return "", errCheckoutPreviewChanged
			}
		default:
			if strings.HasPrefix(lower, "extensions.") {
				return "", errCheckoutPreviewChanged
			}
		}
	}
	if !seen["core.bare"] || !seen["core.repositoryformatversion"] {
		return "", errCheckoutPreviewChanged
	}
	return worktree, nil
}
