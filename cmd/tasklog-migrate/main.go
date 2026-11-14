// tasklog-migrate is an explicit offline migration/recovery utility.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"github.com/idy/yesman/internal/tasklog"
	"os"
)

func main() {
	workspace := flag.String("workspace", "", "workspace directory (required)")
	action := flag.String("action", "dry-run", "dry-run, apply, verify, or rollback")
	expected := flag.String("expect-sha256", "", "full SHA256 printed by dry-run")
	offline := flag.Bool("offline", false, "acknowledge the app and ALL history writers are stopped")
	flag.Parse()
	if *workspace == "" {
		fmt.Fprintln(os.Stderr, "--workspace is required")
		os.Exit(2)
	}
	m, err := tasklog.Run(*workspace, tasklog.Options{Action: *action, Expect: *expected, Offline: *offline})
	report := struct {
		Action   string           `json:"action"`
		Complete bool             `json:"complete"`
		Manifest tasklog.Manifest `json:"manifest"`
		Error    string           `json:"error,omitempty"`
	}{Action: *action, Complete: err == nil, Manifest: m}
	if err != nil {
		report.Error = err.Error()
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(report)
	if err != nil {
		os.Exit(1)
	}
}
