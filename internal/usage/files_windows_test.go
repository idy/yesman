//go:build windows

package usage

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// A junction is not followed, for sessions or an agent's directory. CI
// makes junctions, so there it must run rather than skip.
func TestSessionsSkipJunctions(t *testing.T) {
	line := resp{id: "a1", provider: "p", model: "m", at: t0, duration: 1000, output: 5}.line()
	outside := outsideLog(t, 777)
	for _, at := range []string{"w", "sessions"} {
		ws := workspace(t, map[string]string{"w": line, "v": line})
		path, target := filepath.Join(ws, ".yesman", "sessions", "w"), filepath.Join(outside, "w")
		if at == "sessions" {
			path, target = filepath.Join(ws, ".yesman", "sessions"), outside
		}
		if err := os.RemoveAll(path); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command("cmd", "/c", "mklink", "/J", path, target).CombinedOutput(); err != nil {
			if os.Getenv("GITHUB_ACTIONS") == "true" {
				t.Fatalf("mklink /J: %v %s", err, out)
			}
			t.Skipf("no junctions here: %v %s", err, out)
		}
		want := int64(5) // v, beside a linked w
		if at == "sessions" {
			want = 0
		}
		records, st, err := sessionsScan(t, &Sessions{}, ws)
		if err != nil || outputs(records) != want || !hasError(st, at+": not a directory") {
			t.Fatalf("junction %s: %d %+v %v", at, outputs(records), st, err)
		}
	}
}
