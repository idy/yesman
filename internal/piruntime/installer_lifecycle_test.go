package piruntime

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Exercise the real installer process tree without a registry download. The
// synthetic npm executable merely creates a waiting child; it cannot install or
// inspect user packages/configuration. Close must kill/join both and remove only
// this attempt's staging directory.
func TestSharedInstallerCloseJoinsProcessTree(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node required for installer process check")
	}
	temp := t.TempDir()
	r, err := New(filepath.Join(temp, "cache"))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	pidfile := filepath.Join(temp, "pids.jsonl")
	encoded, _ := json.Marshal(pidfile)
	r.npm = filepath.Join(temp, "npm-fixture.mjs")
	code := `import {spawn} from 'node:child_process';import {appendFileSync} from 'node:fs';
const child=spawn(process.execPath,['-e','setInterval(()=>{},1000)'],{stdio:'ignore'});
appendFileSync(` + string(encoded) + `,JSON.stringify({npm:process.pid,child:child.pid})+'\n');setInterval(()=>{},1000);`
	if err = os.WriteFile(r.npm, []byte(code), 0600); err != nil {
		t.Fatal(err)
	}
	short, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() { _, _, err := r.Command(short); result <- err }()
	var pids struct{ Npm, Child int }
	deadline := time.Now().Add(5 * time.Second)
	for {
		data, _ := os.ReadFile(pidfile)
		if json.Unmarshal([]byte(strings.TrimSpace(string(data))), &pids) == nil && pids.Child > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("synthetic npm did not start")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	if err = <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("short wait: %v", err)
	}
	if r.State() != "preparing" {
		t.Fatal("short wait killed shared installer")
	}
	waiting := make(chan error, 1)
	go func() { _, _, err := r.Command(context.Background()); waiting <- err }()
	closed := make(chan struct{})
	go func() { r.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not join installer")
	}
	if err = <-waiting; !errors.Is(err, context.Canceled) {
		t.Fatalf("closed waiter: %v", err)
	}
	for _, pid := range []int{pids.Npm, pids.Child} {
		deadline = time.Now().Add(3 * time.Second)
		for {
			// Node's signal 0 works on Windows too. Retry briefly for OS child reaping.
			err = exec.Command(node, "-e", `try{process.kill(Number(process.argv[1]),0);process.exit(1)}catch{process.exit(0)}`, strconv.Itoa(pid)).Run()
			if err == nil {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("installer child %d remains alive", pid)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	data, _ := os.ReadFile(pidfile)
	if len(strings.Split(strings.TrimSpace(string(data)), "\n")) != 1 {
		t.Fatal("duplicate npm preparation")
	}
	if _, err = os.Stat(r.root); !os.IsNotExist(err) {
		t.Fatalf("partial build published: %v", err)
	}
	entries, _ := os.ReadDir(filepath.Dir(r.root))
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".prepare-") {
			t.Fatal("staging not cleaned on quit")
		}
	}
}
