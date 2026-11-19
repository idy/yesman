// fakepi is a local, deterministic stand-in for pi in RPC mode for the t198
// restart E2E. It calls no provider. Every command it gets is appended to
// $FAKE_PI_LOG with its pid, agent and arguments. A prompt starting with
// "e2e-hold" starts a run that never settles (the agent stays Working);
// any other prompt runs and settles. It records a session file in its
// --session-dir, so the next start uses --continue.
package main

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func main() {
	logf, err := os.OpenFile(os.Getenv("FAKE_PI_LOG"), os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		os.Exit(2)
	}
	enc := json.NewEncoder(logf)
	note := func(kind string, v map[string]any) {
		v["kind"], v["pid"], v["agent"], v["at"] = kind, os.Getpid(), os.Getenv("YESMAN_AGENT"), time.Now().UTC().Format(time.RFC3339Nano)
		_ = enc.Encode(v)
	}
	args := os.Args[1:]
	note("start", map[string]any{"args": args})
	for i, a := range args {
		if a == "--session-dir" && i+1 < len(args) {
			_ = os.WriteFile(filepath.Join(args[i+1], "fake.jsonl"), []byte("{}\n"), 0o600)
		}
	}
	out := bufio.NewWriter(os.Stdout)
	write := func(v any) {
		b, _ := json.Marshal(v)
		out.Write(append(b, '\n'))
		out.Flush()
	}
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for sc.Scan() {
		var cmd struct {
			ID, Type, Message, StreamingBehavior string
		}
		if json.Unmarshal(sc.Bytes(), &cmd) != nil {
			continue
		}
		var data any = map[string]any{}
		switch cmd.Type {
		case "get_state":
			data = map[string]any{"model": map[string]any{"id": "fake", "provider": "local"}, "thinkingLevel": "off", "sessionId": "fake-session"}
		case "get_messages":
			data = map[string]any{"messages": []any{}}
		case "get_last_assistant_text":
			data = map[string]any{"text": "fake final message"}
		case "get_available_thinking_levels":
			data = map[string]any{"levels": []string{"off"}}
		case "prompt":
			note("prompt", map[string]any{"message": cmd.Message, "behavior": cmd.StreamingBehavior})
		}
		write(map[string]any{"type": "response", "id": cmd.ID, "command": cmd.Type, "success": true, "data": data})
		if cmd.Type == "prompt" {
			write(map[string]any{"type": "agent_start"})
			if !strings.HasPrefix(cmd.Message, "e2e-hold") {
				write(map[string]any{"type": "agent_settled", "aborted": false})
			}
		}
	}
	note("exit", map[string]any{})
}
