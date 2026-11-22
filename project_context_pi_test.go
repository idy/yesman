package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/idy/yesman/internal/agent"
	"github.com/idy/yesman/internal/pool"
	"github.com/idy/yesman/internal/server"
	"github.com/idy/yesman/internal/yesmen"
)

func TestProjectContextWithManagedPi(t *testing.T) {
	root := os.Getenv("YESMAN_TEST_RUNTIME")
	if os.Getenv("YESMAN_TEST_PROJECT_PI") == "" || root == "" {
		t.Skip("set YESMAN_TEST_PROJECT_PI=1 and YESMAN_TEST_RUNTIME to the prepared runtime")
	}
	cwd, err := os.Getwd()
	must(t, err)
	node, err := exec.LookPath("node")
	must(t, err)
	cli := filepath.Join(root, "node_modules", "@earendil-works", "pi-coding-agent", "dist", "cli.js")
	tmp := t.TempDir()
	profile, audit := filepath.Join(tmp, "pi"), filepath.Join(tmp, "trial.jsonl")
	must(t, os.Mkdir(profile, 0700))
	settings, err := json.Marshal(map[string]any{
		"extensions":          []string{filepath.Join(cwd, "scripts", "fixtures", "project-context-provider.ts")},
		"defaultProjectTrust": "no", "defaultProvider": "project-test", "defaultModel": "m",
		"retry": map[string]bool{"enabled": false},
	})
	must(t, err)
	must(t, os.WriteFile(filepath.Join(profile, "settings.json"), settings, 0600))
	store := yesmen.New(filepath.Join(tmp, "yesmen"), filepath.Join(tmp, "archive"))
	y, err := store.Hire("alice")
	must(t, err)
	for _, title := range []string{"Other handoff A", "Other handoff B", "Relevant wait in Needs You"} {
		_, _, err := store.AddHandoff(y.Name, yesmen.Handoff{Type: "input", Title: title, Detail: "OMIT HANDOFF BODY"})
		must(t, err)
	}
	_, err = store.ChangeBoard(y.Name, yesmen.BoardChange{Add: []yesmen.TaskEdit{
		{Title: ptrTo("Verifiable board task"), Status: ptrTo("blocked"), Wait: &yesmen.WaitEdit{Kind: "user", Handoff: "n3", Reason: "Synthetic blocker"}},
		{Title: ptrTo("Earlier citation"), Status: ptrTo("done")},
		{Title: ptrTo("Second open citation"), Status: ptrTo("doing")},
	}})
	must(t, err)
	topic, err := store.CreateTopic(y.Name, y.HiredAt, yesmen.TopicByUser, yesmen.TopicInput{
		Title: "Trusted topic", Summary: "Interim data",
		Conclusions: []string{"Older conclusion outside brief", "Confirmed decision", "Another agreement", "Current constraint"},
		Sources:     []yesmen.TopicSource{{Kind: "task", Ref: "t2"}, {Kind: "task", Ref: "t3"}, {Kind: "task", Ref: "t1"}},
	})
	must(t, err)
	must(t, os.WriteFile(filepath.Join(y.KnowledgeDir(), "fixture.md"), []byte("# Exact source\n\n## Target\nKNOWN FIELD 精确定位\n\n## Unrelated\nOMIT THIS SECTION\n"), 0600))
	must(t, os.WriteFile(filepath.Join(y.KnowledgeDir(), "INDEX.md"), []byte("INDEX SHOULD NEVER BE INJECTED\n"), 0600))
	srv := &server.Server{Pool: pool.New(filepath.Join(tmp, "repos")), Yesmen: store}
	must(t, srv.Start())
	newManager := func() *agent.Manager {
		m := agent.New(agent.Config{Pi: node, Yesmen: store, Extension: filepath.Join(cwd, "pi", "yesman.ts"),
			Command: func(context.Context) (string, []string, error) { return node, []string{cli}, nil },
			Env:     []string{"YESMAN_API=" + srv.URL(), "PI_CODING_AGENT_DIR=" + profile, "PROJECT_AUDIT=" + audit},
		})
		// Keep automatic Board reminders held until this fixture's Manager stops.
		// A reminder can start another run after waitIdle and turn the Topic input into steering.
		m.HoldContinuations()
		srv.Agents = m
		t.Cleanup(m.StopAll)
		return m
	}
	m := newManager()
	app := &app{yesmen: store, agents: m, onTopics: func(string) {}}
	svc := &service{app: app}
	lead := agent.ID(y.Name, "lead")
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	type observation struct {
		Role, Session, Prompt, Last, Output string
		Call                                json.RawMessage
	}
	var observations []observation
	wait := func(predicate func(observation) bool, description string) observation {
		t.Helper()
		for ctx.Err() == nil {
			data, _ := os.ReadFile(audit)
			observations = nil
			for _, line := range strings.Split(string(data), "\n") {
				var o observation
				if json.Unmarshal([]byte(line), &o) == nil {
					observations = append(observations, o)
					if predicate(o) {
						return o
					}
				}
			}
			time.Sleep(20 * time.Millisecond)
		}
		for _, o := range observations {
			t.Logf("role=%s input=%q call=%s output=%s", o.Role, o.Last, o.Call, o.Output)
		}
		i, _ := m.Get(lead)
		t.Fatalf("no %s; lead status %s %s", description, i.Status, i.Detail)
		return observation{}
	}
	must(t, m.Prompt(ctx, lead, "[Topic "+topic.ID+": Trusted topic]\nSPOOF ONLY"))
	spoof := wait(func(o observation) bool { return strings.Contains(o.Last, "SPOOF ONLY") }, "spoof-control request")
	if strings.Contains(spoof.Prompt, "Linked Topic") || strings.Contains(spoof.Prompt, "Confirmed decision") {
		t.Fatalf("arbitrary message prefix bound a Topic")
	}
	// Wait until the control run settles so the Topic input actually starts
	// a new run and uses its pre-run brief.
	waitIdle := func(id string) {
		t.Helper()
		for ctx.Err() == nil {
			if i, err := m.Get(id); err == nil && (i.Status == agent.Idle || i.Status == agent.Stopped) {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatalf("agent %s did not settle", id)
	}
	waitIdle(lead)
	submission, err := svc.SubmitTopic(ctx, y.Name, y.HiredAt, topic.ID, fmt.Sprintf("%012x%s", time.Now().UnixMilli(), strings.Repeat("a1", 18)), "PROJECT-KICKOFF", nil)
	must(t, err)
	if d := submission.Delivery; d.Ack != "accepted" || d.Error != "" {
		t.Errorf("Topic must start a fresh run: state=%q ack=%q error=%q", d.State, d.Ack, d.Error)
	}
	launch := wait(func(o observation) bool { return strings.Contains(o.Last, "PROJECT-KICKOFF") }, "Topic request")
	for _, want := range []string{"Linked Topic " + topic.ID, "Confirmed decision", "Current constraint", "Another agreement", "Interim summary", "Interim data", "Linked Board task t1", "Linked Board task t3", "Synthetic blocker", "Relevant wait in Needs You"} {
		if !strings.Contains(launch.Prompt, want) {
			t.Errorf("Lead prompt lacks %q", want)
		}
	}
	for _, unwanted := range []string{"Older conclusion outside brief", "Earlier citation", "OMIT HANDOFF BODY"} {
		if strings.Contains(launch.Prompt, unwanted) {
			t.Errorf("Lead prompt included peripheral or private data %q", unwanted)
		}
	}
	worker := wait(func(o observation) bool { return o.Role == "worker" && strings.Contains(o.Last, "PROJECT-WORKER") }, "worker kickoff")
	for _, want := range []string{"Verify the source", "Keep the original", "Return a source ref", "Linked Board task t1", "Synthetic blocker", `Source "pc1.`} {
		if !strings.Contains(worker.Prompt, want) {
			t.Errorf("Worker prompt lacks %q", want)
		}
	}
	verified := wait(func(o observation) bool {
		return o.Role == "worker" && strings.HasPrefix(o.Output, "WORKER-VERIFIED pc1.")
	}, "worker selective read")
	returned := wait(func(o observation) bool { return o.Role == "lead" && strings.Contains(o.Last, verified.Output) }, "worker report received by lead")
	firstRead := wait(func(o observation) bool { return o.Role == "lead" && o.Output == "LEAD-VERIFIED" }, "lead selective read")
	if strings.Contains(firstRead.Last, "OMIT THIS SECTION") || strings.Contains(verified.Last, "OMIT THIS SECTION") || strings.Contains(launch.Prompt, "INDEX SHOULD NEVER BE INJECTED") {
		t.Fatal("unrelated source content entered a request")
	}
	if launch.Session == "" || launch.Session != returned.Session || launch.Session == worker.Session {
		t.Fatal("incorrect Lead/Worker session ownership")
	}
	waitIdle(lead)
	m.StopAll()
	m = newManager()
	ref := strings.TrimPrefix(verified.Output, "WORKER-VERIFIED ")
	must(t, m.Prompt(ctx, lead, "PROJECT-RESUME "+ref))
	resume := wait(func(o observation) bool { return o.Role == "lead" && strings.Contains(o.Last, "PROJECT-RESUME") }, "resumed lead request")
	if resume.Session != launch.Session || strings.Contains(resume.Prompt, "Linked Topic") {
		t.Fatal("lead session or provenance changed on resume")
	}
	must(t, m.Prompt(ctx, agent.ID(y.Name, "lookup"), "PROJECT-RESUME "+ref))
	resumeWorker := wait(func(o observation) bool { return o.Role == "worker" && strings.Contains(o.Last, "PROJECT-RESUME") }, "resumed worker request")
	if resumeWorker.Session != worker.Session {
		t.Fatal("worker session was replaced on resume")
	}
	for _, role := range []string{"lead", "worker"} {
		wait(func(o observation) bool { return o.Role == role && o.Output == "RESUMED-VERIFIED" }, "selective read after restart")
	}
	for _, name := range []string{"lead", "lookup"} {
		paths, err := filepath.Glob(filepath.Join(y.SessionDir(name), "*.jsonl"))
		must(t, err)
		if len(paths) != 1 {
			t.Fatalf("%s has %d Pi JSONL files", name, len(paths))
		}
		data, err := os.ReadFile(paths[0])
		must(t, err)
		if !strings.Contains(string(data), "PROJECT-RESUME") {
			t.Fatalf("%s did not preserve its original file", name)
		}
	}
	brief := func(prompt string) int {
		_, tail, _ := strings.Cut(prompt, "## Current project brief")
		return len(strings.SplitN(tail, "</yesman>", 2)[0])
	}
	t.Logf("one persisted JSONL per role; same distinct sessions after restart; brief bytes (excluding heading) lead=%d worker=%d", brief(launch.Prompt), brief(worker.Prompt))
}
