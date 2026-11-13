//go:build e2e

package main

// Builds tagged e2e can be driven by mygo-automation through the socket
// named by $MYGO_AUTOMATION, for end-to-end checks.
import (
	"os"

	"github.com/egoist/mygo"
	_ "github.com/egoist/mygo/plugins/automation"
)

// Automation cannot reach native dialogs: YESMAN_E2E_SAVE and
// YESMAN_E2E_OPEN answer the configuration file's. YESMAN_E2E_NAME names
// the app otherwise, so that it runs beside another yesman rather than
// handing over to it.
func init() {
	if name := os.Getenv("YESMAN_E2E_NAME"); name != "" {
		appName = name
	}
	if path := os.Getenv("YESMAN_E2E_SAVE"); path != "" {
		configSaveDialog = func(mygo.SaveDialogOptions) (string, error) { return path, nil }
	}
	if path := os.Getenv("YESMAN_E2E_OPEN"); path != "" {
		configOpenDialog = func(mygo.OpenDialogOptions) ([]string, error) { return []string{path}, nil }
	}
}
