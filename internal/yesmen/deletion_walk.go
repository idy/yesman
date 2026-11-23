package yesmen

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"strings"
)

type deletionWalk uint8

const (
	inspectDeletion deletionWalk = iota
	prepareDeletion
	purgeDeletion
)

type deletionCoverage struct {
	output *deletionInventory
	input  *deletionCursor
	subset bool
}

// Walk one verified directory handle at a time. A file or link is unlinked as
// an entry. Only an empty directory is ever removed.
func walkDeletion(ctx context.Context, ws *os.Root, phase deletionWalk, force *ForceDeletion, coverage deletionCoverage) error {
	volume, err := directoryIdentity(ws)
	if err != nil {
		return err
	}
	var git *localGitReview
	if force != nil && force.approval == nil && len(force.git) != 0 {
		git, err = force.reviewApprovedGit(ctx, ws)
		if err != nil {
			return err
		}
		defer git.root.Close()
	}
	admins := map[string]bool{}
	var approvedGit map[string]string
	var configs map[string][32]byte
	var removalReview *localGitReview
	if force != nil {
		approvedGit = force.approvedGit()
		for _, admin := range approvedGit {
			admins[admin] = true
		}
		if phase == purgeDeletion && force.approval != nil {
			configs = force.approvedGitConfigs()
			removalReview, err = newLocalGitReview(ctx, ws)
			if err != nil {
				return err
			}
			defer removalReview.root.Close()
		}
	}
	seen, seenTop := 0, 0
	legacyStage := isLegacyDeletionStage(ws)
	var visit func(*os.Root, string, bool, int) error
	visit = func(dir *os.Root, rel string, inGit bool, depth int) error {
		if depth > deletionDepthLimit {
			return deletionIssue("The workspace is too deeply nested to review for deletion.")
		}
		inGit = inGit || admins[rel]
		if err := ctx.Err(); err != nil {
			return err
		}
		id, err := directoryIdentity(dir)
		if err != nil {
			return err
		}
		if !sameMount(id, volume) {
			return deletionIssue("Unmount %s before deleting this workspace.", rel)
		}
		if admins[rel] && phase == purgeDeletion {
			if git != nil {
				binding := git.expected[rel]
				if id != binding.id {
					return errCheckoutPreviewChanged
				}
				if err := git.recheckAdmin(binding); err != nil {
					return err
				}
			} else if err := removalReview.checkRemovalDependencies(rel, configs[rel], true); err != nil {
				return err
			}
		}
		var checkEntry func(string) error
		if inGit {
			checkEntry = func(name string) error {
				if heldLocalGitEntry(rel, name, admins[rel]) {
					return holdGitData(ws, path.Join(rel, name))
				}
				return nil
			}
		}
		listing, err := readDeletionDirectory(ctx, dir, checkEntry)
		if err != nil {
			return err
		}
		entry, markers := deletionGitMarkers(dir, rel, listing.markers, legacyStage)
		var verified map[string]fs.FileInfo
		if len(markers) > 0 {
			current, err := observeCheckout(dir, entry, id, markers)
			if err != nil {
				return err
			}
			if git != nil && phase != purgeDeletion {
				if !force.paths[rel].matches(current) {
					return errCheckoutPreviewChanged
				}
				if err := git.validate(dir, current); err != nil {
					return err
				}
				if git.paths[rel] != force.git[rel] {
					return errCheckoutPreviewChanged
				}
			} else if approvedGit[rel] == "" {
				if err := checkCheckoutGit(ws, dir, current, rel); err != nil {
					return err
				}
			}
			if force == nil {
				return deletionIssue("%s contains a Git checkout. Move it with Git or finish its managed cleanup first.", rel)
			}
			if !force.paths[rel].matches(current) {
				return errCheckoutPreviewChanged
			}
			verified = current.markers
			seen++
		} else if force != nil {
			if _, approved := force.paths[rel]; approved {
				return errCheckoutPreviewChanged
			}
		}
		if !listing.empty() {
			if err := deletionDirectoryAccess(dir, rel, phase != inspectDeletion); err != nil {
				return err
			}
		}
		var handle *os.File
		sharing := len(admins) != 0 && force.checks.Entry != nil && (phase != purgeDeletion || inGit)
		inventory := coverage.input != nil || coverage.output != nil
		if !listing.empty() && (sharing || inventory) {
			handle, err = dir.Open(".")
			if err != nil {
				return err
			}
			defer handle.Close()
		}
		for e, err := range listing.sorted(ctx) {
			if err != nil {
				return err
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			name := e.Name()
			child := path.Join(rel, name)
			st, err := dir.Lstat(name)
			if err != nil {
				return err
			}
			if inGit && st.Mode().Type() != fs.ModeDir && !st.Mode().IsRegular() {
				return holdGitData(ws, child)
			}
			if sharing {
				if err := force.checks.Entry(handle, name); err != nil {
					return &DeletionIssue{Message: "Could not verify that %s is idle.", Args: []any{child}, Cause: err}
				}
			}
			if expected := verified[name]; expected != nil && !sameDeletionEntry(expected, st) {
				return errCheckoutPreviewChanged
			}
			if force != nil && rel == "." && force.top[name] != nil {
				if !sameDeletionEntry(force.top[name], st) {
					return errCheckoutPreviewChanged
				}
				seenTop++
			}
			if inventory {
				gitData := admins[rel] && (name == "HEAD" || name == "config") ||
					approvedGit[rel] != "" && strings.EqualFold(name, ".git")
				fingerprint, err := deletionFingerprint(dir, handle, name, st, volume, gitData)
				if err != nil {
					return err
				}
				if coverage.input != nil {
					if err := coverage.input.match(ctx, child, fingerprint, coverage.subset); err != nil {
						return err
					}
				}
				if coverage.output != nil {
					if err := coverage.output.add(child, fingerprint); err != nil {
						return err
					}
				}
			}
			switch {
			case st.Mode().Type() == fs.ModeDir:
				if rel == "." && name != ".yesman" && (force == nil || force.top[name] == nil) {
					return deletionIssue("Move %q out of the workspace first. For a worktree, use Git or its managed cleanup.", name)
				}
				d, err := directDir(dir, name)
				if err != nil {
					return err
				}
				opened, err := d.Stat(".")
				if err != nil || !os.SameFile(st, opened) {
					d.Close()
					return fmt.Errorf("%s changed during deletion", child)
				}
				err = visit(d, child, inGit, depth+1)
				d.Close()
				if err != nil {
					return err
				}
			case child == ".yesman":
				return errors.New(".yesman must be the workspace's own directory")
			case strings.HasPrefix(child, ".yesman/sessions/") || rel == ".yesman":
				// An active agent drain reads/writes here. Do not let it follow a
				// linked session path, wait on a pipe or truncate a linked temp.
				if !st.Mode().IsRegular() {
					return deletionIssue("Move the linked or special metadata entry %s out of the workspace first.", child)
				}
				if strings.HasSuffix(name, ".tmp") {
					if err := singleDeletionFile(dir, name, st); err != nil {
						return err
					}
				}
			}
			if phase == purgeDeletion {
				if name == ".git" && st.Mode().IsRegular() {
					if git != nil {
						if module := force.git[rel]; module.module {
							if err := git.recheckAdmin(module); err != nil {
								return err
							}
							if err := force.recheckModulePointer(git, rel, module); err != nil {
								return err
							}
						}
					} else if removalReview != nil {
						if admin := approvedGit[rel]; admin != "" {
							if err := removalReview.checkRemovalDependencies(admin, configs[admin], true); err != nil {
								return err
							}
						}
					}
				}
				now, err := dir.Lstat(name)
				if err != nil || !sameDeletionEntry(st, now) {
					return fmt.Errorf("%s changed during deletion", child)
				}
				if err := dir.Remove(name); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := visit(ws, ".", false, 0); err != nil {
		return err
	}
	if force != nil && (seen != len(force.paths) || seenTop != len(force.top)) {
		return errCheckoutPreviewChanged
	}
	if coverage.input != nil && !coverage.subset {
		if err := coverage.input.next(); !errors.Is(err, io.EOF) {
			return errCheckoutPreviewChanged
		}
	}
	return nil
}

func singleDeletionFile(dir *os.Root, name string, st fs.FileInfo) error {
	f, err := dir.OpenFile(name, os.O_RDONLY|openNoBlock, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(st, opened) || !opened.Mode().IsRegular() {
		return fmt.Errorf("%s changed during deletion", name)
	}
	n, err := linkCount(f, opened)
	if err != nil || n != 1 {
		return deletionIssue("Move the linked metadata file %s out of the workspace first.", name)
	}
	return nil
}
