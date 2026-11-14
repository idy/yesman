package server

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestMain lets the test binary stand in for pi in RPC mode when
// YESMAN_SERVER_FAKE_PI is set: it answers every command, and writes the
// credential the app gave it to $FAKE_PI_TOKENS/<yesman>_<agent>, so tests can
// call the API as that agent.
func TestMain(m *testing.M) {
	if os.Getenv("YESMAN_SERVER_FAKE_PI") != "" {
		serverFakePi()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func serverFakePi() {
	if dir := os.Getenv("FAKE_PI_TOKENS"); dir != "" {
		name := strings.ReplaceAll(os.Getenv("YESMAN_AGENT"), "/", "_")
		_ = os.WriteFile(filepath.Join(dir, name), []byte(os.Getenv("YESMAN_TOKEN")), 0o600)
	}
	out := bufio.NewWriter(os.Stdout)
	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		var cmd struct {
			ID   string `json:"id"`
			Type string `json:"type"`
		}
		if json.Unmarshal(sc.Bytes(), &cmd) != nil || cmd.ID == "" {
			continue
		}
		var data any
		switch cmd.Type {
		case "get_state":
			data = map[string]any{"model": map[string]any{"id": "fixture", "provider": "local"}, "thinkingLevel": "off"}
		case "get_messages":
			data = map[string]any{"messages": []any{}}
		}
		b, _ := json.Marshal(map[string]any{"type": "response", "id": cmd.ID, "command": cmd.Type, "success": true, "data": data})
		out.Write(append(b, '\n'))
		out.Flush()
	}
}
