//go:build unix

package schedule

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// A FIFO at plans.json, or in place of the workspace, is refused without
// being opened: nothing waits for a writer, and it is left as it is.
func TestFIFOIsNotOpened(t *testing.T) {
	for _, c := range []struct {
		name string
		path func(e *env) string
	}{
		{"plans.json", func(e *env) string { return filepath.Join(e.root, "yesmen", ws, ".yesman", "schedules", "plans.json") }},
		{"workspace", func(e *env) string { return filepath.Join(e.root, "yesmen", ws) }},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t, "", utc("2026-11-16T08:00:00Z"))
			e.save(PlanInput{Schedule: daily("09:00")})
			e.close()
			path := c.path(e)
			if err := os.Rename(path, filepath.Join(e.root, "moved")); err != nil {
				t.Fatal(err)
			}
			if err := syscall.Mkfifo(path, 0o600); err != nil {
				t.Skip("no FIFOs here")
			}
			e.restart("")
			if v, err := e.svc.View(ws); err == nil && v.Error == "" {
				t.Fatalf("read a FIFO: %+v", v)
			}
			if _, err := e.svc.Save(ws, PlanInput{Title: "T", Instructions: "I", Schedule: daily("10:00"), Timezone: "UTC"}); err == nil {
				t.Fatal("saved over a FIFO")
			}
			if info, err := os.Lstat(path); err != nil || info.Mode()&os.ModeNamedPipe == 0 {
				t.Fatalf("the FIFO was replaced: %v", err)
			}
		})
	}
}
