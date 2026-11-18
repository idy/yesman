// QA-only normal managed-runtime control. No GUI, provider or signing inputs.
package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/idy/yesman/internal/piruntime"
)

func main() {
	out := map[string]any{"schema": 1, "passed": false, "phase": "admission"}
	code := 1
	defer func() {
		_ = json.NewEncoder(os.Stdout).Encode(out)
		os.Exit(code)
	}()
	for _, key := range []string{"GH_TOKEN", "GITHUB_TOKEN", "MYGO_UPDATER_PRIVATE_KEY", "OPENAI_API_KEY", "ANTHROPIC_API_KEY"} {
		if os.Getenv(key) != "" {
			out["reason"] = "credential-present"
			return
		}
	}
	if len(os.Args) != 2 || !filepath.IsAbs(os.Args[1]) || filepath.Clean(os.Args[1]) != os.Getenv("T199_CONTROL_DATA") {
		out["reason"] = "private-control-root-required"
		return
	}
	root := filepath.Clean(os.Args[1])
	if st, err := os.Lstat(root); err != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
		out["reason"] = "ordinary-control-root-required"
		return
	}
	out["phase"] = "production-runtime"
	r, err := piruntime.New(filepath.Join(root, ".runtime"))
	if err != nil {
		out["reason"] = "node-npm-discovery-failed"
		return
	}
	defer r.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	node, args, err := r.Command(ctx)
	out["state"] = r.State()
	out["contentKey"] = filepath.Base(r.Directory())
	if err != nil {
		out["reason"] = "production-preparation-failed"
		return
	}
	out["phase"] = "managed-cli-version"
	cmd := exec.CommandContext(ctx, node, append(args, "--version")...)
	cmd.Dir = root
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME"), "PI_CODING_AGENT_DIR=" + filepath.Join(root, "synthetic-pi"), "NO_UPDATE_NOTIFIER=1"}
	raw, err := cmd.Output()
	if err != nil || strings.TrimSpace(string(raw)) != "1.5.2" {
		out["reason"] = "managed-version-failed"
		return
	}
	out["cliVersion"] = "1.5.2"
	out["phase"] = "done"
	out["passed"] = true
	out["productionVerificationCompleted"] = true
	out["appOrProviderExecuted"] = false
	out["credentialsUsed"] = false
	code = 0
}
