package yesmen

import (
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

const gitDeletionFileLimit = 8 << 10
const stagedGitReceipt = ".yesman-deletion-git.json"

func holdGitData(ws *os.Root, rel string) error {
	return deletionIssue("Git data at %s may be used by another checkout. Check its dependencies before removing it, or move the complete repository and its dependent worktrees to safety with Git. Then check again.", filepath.Join(ws.Name(), filepath.FromSlash(rel)))
}

func holdGitMarker(ws *os.Root, rel string) error {
	return deletionIssue("Cannot verify the Git entry at %s. Check where it points and use Git or managed cleanup to move the checkout to safety. Then check again.", filepath.Join(ws.Name(), filepath.FromSlash(rel)))
}

// A local pointer or registration can establish one consumer of a Git store.
// Git can also open that store through unregistered pointers that exist only
// outside the workspace. Nothing about the local layout proves exclusivity.
// A force-confirmed symbolic link is only a local directory entry. The rooted
// walker unlinks it without opening its referent. Embedded stores, junctions
// and unfamiliar reparse points still require independent verification.
func checkCheckoutGit(ws, dir *os.Root, checkout checkoutObservation, rel string) error {
	if checkout.Bare {
		return holdGitData(ws, rel)
	}
	for name, st := range checkout.markers {
		marker := path.Join(rel, name)
		if st.IsDir() {
			return holdGitData(ws, marker)
		}
		if _, link := checkout.links[name]; link {
			continue
		}
		if !st.Mode().IsRegular() {
			return holdGitMarker(ws, marker)
		}
		data, current, err := readRegularEntry(dir, name, gitDeletionFileLimit, nil, true)
		if err != nil || !sameDeletionEntry(st, current) {
			return holdGitMarker(ws, marker)
		}
		value, ok := strings.CutPrefix(string(data), "gitdir: ")
		value = strings.TrimRight(value, "\r\n")
		if !ok || strings.TrimSpace(value) == "" || strings.ContainsAny(value, "\r\n\x00") ||
			(!filepath.IsAbs(value) && filepath.VolumeName(value) != "") {
			return holdGitMarker(ws, marker)
		}
		// Git interprets relative gitfile paths from the containing checkout.
		// Never use a filesystem lookup to resolve components of the referent.
		if !filepath.IsAbs(value) {
			value = filepath.Join(dir.Name(), value)
		}
		if target, local := workspaceGitPath(ws.Name(), value); local {
			return holdGitData(ws, target)
		}
	}
	return nil
}

func workspaceGitPath(workspace, target string) (string, bool) {
	rel, err := filepath.Rel(workspace, target)
	if err == nil && filepath.IsLocal(rel) {
		return rel, true
	}
	// Resolve only the trusted workspace path to handle a platform alias
	// (for example /var and /private/var on macOS).
	root, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		return ".", true
	}
	rel, err = filepath.Rel(root, target)
	return rel, err == nil && filepath.IsLocal(rel)
}

// An interrupted experimental deletion can have removed Git markers while
// leaving common or private metadata behind. Keep its recovery evidence in
// place. A receipt for specific registered worktrees cannot establish that
// every consumer of the Git store was local.
func checkStagedGitHold(ws *os.Root) error {
	_, err := ws.Lstat(stagedGitReceipt)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return deletionIssue("The interrupted deletion at %s still has a Git recovery record at %s. Preserve the remaining Git data and check its dependencies before retrying in Storage.", ws.Name(), filepath.Join(ws.Name(), stagedGitReceipt))
}
