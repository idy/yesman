package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestBindingsGenerationDoesNotInitializeStorage(t *testing.T) {
	if os.Getenv("YESMAN_BINDINGS_HELPER") == "1" {
		main()
		return
	}
	dir := t.TempDir()
	out, data := filepath.Join(dir, "client.ts"), filepath.Join(dir, "must remain absent")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "-test.run=^TestBindingsGenerationDoesNotInitializeStorage$")
	cmd.Env = append(os.Environ(), "YESMAN_BINDINGS_HELPER=1", "MYGO_GENERATE="+out, "YESMAN_HOME="+data, "YESMAN_PI="+filepath.Join(dir, "must-not-run"))
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generator: %v: %s", err, b)
	}
	b, err := os.ReadFile(out)
	if err != nil || !strings.Contains(string(b), "Yesman.State") {
		t.Fatalf("missing real service bindings: %v", err)
	}
	if _, err := os.Stat(data); !os.IsNotExist(err) {
		t.Fatalf("generation initialized user storage: %v", err)
	}
}
