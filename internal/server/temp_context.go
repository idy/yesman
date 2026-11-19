package server

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/idy/yesman/internal/agent"
	"github.com/idy/yesman/internal/yesmen"
)

// writeTemporaryFilesContext states where task files go, the variables the
// app sets, and exactly what is enforced. It gives rules and paths only,
// never the contents of any evidence.
func writeTemporaryFilesContext(b *strings.Builder, y yesmen.Yesman, me agent.Info) {
	w := func(format string, args ...any) { fmt.Fprintf(b, format, args...) }
	tmp := filepath.Join(y.Path, ".yesman", "tmp")
	evidence := filepath.Join(y.Path, ".yesman", "evidence")
	w("\n## Temporary files and evidence\n\n")
	if me.Role == agent.RoleLead {
		w("Workers keep task-generated temporary files (scripts, downloads, test profiles, build output) under %s and the small reports, screenshots and reconstruction notes worth keeping under %s, inside this workspace, never at its root or in the system's temporary directory. Link to evidence from the Working log rather than copying it. Existing older temporary paths and evidence stay where they are.\n", tmp, evidence)
		return
	}
	source := "your own worktree"
	if y.TaskRoot {
		source = "the supplied project directory or your own worktree"
	}
	w("Keep task-generated temporary files (scripts, downloads, test profiles, private HOME or config of a test, build output) under %s and the small reports, screenshots and reconstruction notes worth keeping under %s, never at the workspace root or in /tmp, /private/tmp or another system temporary directory. Source changes still go in %s. Keep only what supports a conclusion or reconstruction, without secrets.\n", filepath.Join(tmp, "<task-id>", me.Name), filepath.Join(evidence, "<task-id>", me.Name), source)
	w("- Every command you run has TMPDIR, TMP, TEMP and GOTMPDIR set to a scratch directory of this agent process ($YESMAN_TMP), and YESMAN_EVIDENCE set to its evidence directory; GOCACHE, GOMODCACHE and npm_config_cache point to this workspace's shared caches in %s. Your settings' variables cannot move them. Keep your real HOME and credentials: do not redirect HOME except for a synthetic test of its own.\n", filepath.Join(tmp, "_cache"))
	grants := "your own author or release worktrees"
	if y.TaskRoot {
		grants = "the supplied project directory, your own author or release worktrees"
	}
	w("- The write and edit tools are done by yesman and change only files in %s and in $YESMAN_TMP and $YESMAN_EVIDENCE; other paths, links, files with several hard links, yesman's metadata and other agents' or workspaces' files are refused. Write task directories with shell commands.\n", grants)
	w("- That is the whole enforcement. Shell commands, programs they start, other tools and extensions write with your own permissions anywhere you can: the variables only choose defaults, and there is no sandbox. Keep their output in the places above yourself.\n")
}
