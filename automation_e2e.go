//go:build e2e

package main

// Builds tagged e2e can be driven by mygo-automation through the socket
// named by $MYGO_AUTOMATION, for end-to-end checks.
import _ "github.com/egoist/mygo/plugins/automation"
