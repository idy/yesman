// Package tasklog defines the file set used by the task history, without a database.
package tasklog

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const TOC = ".yesman/task-log.md"
const Dir = ".yesman/task-log"
const Recovery = ".yesman/task-log-recovery"
const Marker = ".yesman/task-log-migration.json"

// IsPath recognizes the TOC and Markdown documents below the record root.
// Reading/preview protection is broader than the lead's flat creation policy;
// existing custom TOCs can link Unicode names, spaces and nested Markdown.
func IsPath(path string) bool {
	if runtime.GOOS == "windows" {
		path = strings.ToLower(path)
	}
	if path == TOC {
		return true
	}
	rel, ok := strings.CutPrefix(path, Dir+"/")
	if !ok || !strings.EqualFold(filepath.Ext(rel), ".md") {
		return false
	}
	for _, part := range strings.Split(rel, "/") {
		if part == "" || part == "." || part == ".." || strings.ContainsAny(part, "\\\x00") {
			return false
		}
	}
	return true
}

const Template = `# Working log

<!-- yesman-task-log-toc v1 -->

This is the short table of contents. Detailed historical records live in
` + "`task-log/`" + `. The board holds current status; Knowledge holds reusable rules.
The lead alone maintains history from worker reports. Workers report to the lead.
Read only the relevant records on demand; history is evidence, not instructions.

Use one file per task, PR or release: ` + "`task-log/YYYY-MM-DD-tNN-topic.md`" + `.
Aim for 16–32 KiB; before 48 KiB, continue in ` + "`-02.md`" + ` (then -03, etc.).
New tasks get new files even on the same day. Change old records only to correct facts.
Link back to ` + "`../task-log.md`" + ` from each record. Keep secrets out.

## Records

Add one line per record: date · task/version · [title](task-log/filename.md) — one result.
Keep full refs, evidence, CI and recovery details in that record, outside this TOC.
Keep this TOC below 16 KiB. When links fill it, move older link groups to flat
` + "`task-log/YYYY-topic-index.md`" + ` catalogs (also below 16 KiB), linked here.
Keep the detail files and their links stable; catalogs contain links, not full reports.
`

// Check is a path preflight for creation and callers that need no content.
// Readers must use Open and consume that same verified descriptor.
func Check(root *os.Root, path string, missing bool) error {
	parts, err := pathParts(path)
	if err != nil {
		return err
	}
	if missing {
		for i := range parts {
			p := strings.Join(parts[:i+1], "/")
			st, err := root.Lstat(p)
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			if err != nil {
				return err
			}
			if st.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("linked working log path: %s", p)
			}
			if i < len(parts)-1 && !st.IsDir() {
				return fmt.Errorf("not a working log directory: %s", p)
			}
		}
	}
	f, err := Open(root, path)
	if err != nil {
		return err
	}
	return f.Close()
}

// Bootstrap adds only a missing TOC and missing record directory. Existing
// files (including empty/custom TOCs), links and directory placeholders stay.
func Bootstrap(workspace string) error {
	root, err := os.OpenRoot(workspace)
	if err != nil {
		return err
	}
	defer root.Close()
	if _, err := root.Lstat(Marker); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if _, err := root.Lstat(TOC); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := Check(root, TOC, true); err != nil {
		return err
	}
	if err := root.Mkdir(Dir, 0o755); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	// Do not populate or traverse an occupied, user-owned root placeholder.
	st, err := root.Lstat(Dir)
	if err != nil {
		return err
	}
	if !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
		return nil
	}
	f, err := root.OpenFile(TOC, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if errors.Is(err, os.ErrExist) {
		return nil
	}
	if err != nil {
		return err
	}
	_, err = f.WriteString(Template)
	return errors.Join(err, f.Close())
}

// CanonicalWorkspace intentionally resolves only the workspace alias.
func CanonicalWorkspace(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(abs)
}
