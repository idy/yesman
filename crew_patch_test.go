package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/egoist/mygo"
	"github.com/idy/yesman/internal/agent"
	"github.com/idy/yesman/internal/crew"
	"github.com/idy/yesman/internal/server"
)

func allInDefault(members []crew.Member) crew.Routing {
	return crew.Routing{Members: members, Pools: crew.Pools{crew.DefaultLabel: {Members: memberKeys(members)}}}
}

const syntheticPatch = crew.PatchHeader + "magpie,auto/claude-opus-5-5,max,0,,,,\nmagpie,local/tiny,off,,123.4,,,0\n"

func TestScorePatchServicePersistenceAndRollback(t *testing.T) {
	a, _ := crewApp(t)
	s := &service{app: a}
	ctx := context.Background()
	source := filepath.Join(t.TempDir(), "input.csv")
	write := func(text string) {
		t.Helper()
		if err := os.WriteFile(source, []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(syntheticPatch)
	if err := a.importScorePatch(source); err != nil {
		t.Fatal(err)
	}
	for _, o := range []crew.Option{{Provider: "magpie", Model: "auto/claude-opus-5-5", Effort: "max"}, {Provider: "magpie", Model: "local/tiny", Effort: "off"}} {
		if _, err := s.AddCrewOption(ctx, o.Provider, o.Model, o.Effort, nil); err != nil {
			t.Fatal(err)
		}
	}
	v := s.Crew(ctx)
	if len(v.Members) != 2 || v.Members[0].Score.Intelligence != 0 || !v.Members[1].Score.IntelligenceMissing {
		t.Fatalf("view: %+v", v)
	}
	if !reflect.DeepEqual(a.workerRouting(ctx, nil).Members, v.Members) {
		t.Fatal("Lead and Models disagree")
	}
	text := server.CrewContext(allInDefault(a.workerRouting(ctx, nil).Members), agent.Info{})
	for _, want := range []string{"intelligence 0 (user CSV)", "inherited AA", "intelligence unavailable", "speed 123.4 tok/s (user CSV)", "time 0 min per task (user CSV)"} {
		if !strings.Contains(text, want) {
			t.Fatalf("context lacks %q: %s", want, text)
		}
	}
	saved, _ := os.ReadFile(a.scorePatchPath())
	if string(saved) != syntheticPatch {
		t.Fatal("persisted bytes changed")
	}
	for _, bad := range []string{syntheticPatch + "magpie,local/tiny,off,9,,,,\n", crew.PatchHeader + "r,m,off,,no,,,\n"} {
		write(bad)
		if err := a.importScorePatch(source); err == nil {
			t.Fatal("invalid import accepted")
		}
		got, _ := os.ReadFile(a.scorePatchPath())
		original, _ := os.ReadFile(source)
		if string(got) != string(saved) || string(original) != bad || !reflect.DeepEqual(s.Crew(ctx).Members, v.Members) {
			t.Fatal("invalid import changed last-good/source")
		}
	}
	if err := a.importScorePatch(a.scorePatchPath()); err == nil {
		t.Fatal("rewrote selected original")
	}
	disabled, err := s.SetScorePatchEnabled(ctx, false)
	if err != nil || disabled.Patch.Enabled || disabled.Members[0].Source != crew.SourceAA || disabled.Members[1].Score != nil {
		t.Fatalf("disable: %+v %v", disabled, err)
	}
	restarted := &app{home: a.home, catalog: a.catalog, settings: loadSettings(a.home.Settings())}
	if got := restarted.crewView(ctx, false); got.Patch.Enabled || got.Patch.Rows != 2 || got.Members[0].Source != crew.SourceAA {
		t.Fatalf("restart disabled: %+v", got)
	}
	enabled, err := (&service{app: restarted}).SetScorePatchEnabled(ctx, true)
	if err != nil || !reflect.DeepEqual(enabled.Members, v.Members) {
		t.Fatalf("restart enabled: %+v %v", enabled, err)
	}
	write(crew.PatchHeader)
	if err := a.importScorePatch(source); err != nil {
		t.Fatal(err)
	}
	if got := s.Crew(ctx); got.Patch.Rows != 0 {
		t.Fatal("header-only replacement did not clear patch")
	}
	// Invalid startup files are left intact and reported, not silently installed.
	if err := os.WriteFile(a.scorePatchPath(), []byte("bad header\n"), 0600); err != nil {
		t.Fatal(err)
	}
	broken := &app{home: a.home, catalog: a.catalog, settings: a.settings}
	if got := broken.crewView(ctx, false); got.Patch.Error == "" || got.Patch.Rows != 0 {
		t.Fatalf("invalid startup: %+v", got)
	}
}

func TestScorePatchSettingsAndSourceProvenance(t *testing.T) {
	current := Settings{ScorePatchDisabled: true}
	for _, mode := range []importMode{importMerge, importReplace} {
		if !(config{}).settings(current, mode).ScorePatchDisabled {
			t.Fatal("general configuration import enabled the independent patch")
		}
	}
	zero := 0.0
	snap := crew.Snapshot{Scores: []crew.Score{{Name: "Plain", Base: "Plain", Intelligence: 20, MinutesPerTask: &zero}}}
	c := crew.Config{Options: []crew.Option{{Provider: "r", Model: "plain", Effort: "high"}}}
	patch, err := crew.ParsePatch([]byte(crew.PatchHeader + "r,plain,high,,0,,,\n"))
	if err != nil {
		t.Fatal(err)
	}
	text := server.CrewContext(allInDefault(crew.Resolve(c, nil, snap, patch)), agent.Info{})
	for _, want := range []string{"time 0 min per task (inherited AA) (source record; actual time unverified)", "AA effort not specified by source", "speed 0 tok/s (user CSV)"} {
		if !strings.Contains(text, want) {
			t.Fatalf("lost provenance %q: %s", want, text)
		}
	}
	patch, _ = crew.ParsePatch([]byte(crew.PatchHeader + "r,plain,high,,,,,0\n"))
	text = server.CrewContext(allInDefault(crew.Resolve(c, nil, snap, patch)), agent.Info{})
	if strings.Contains(text, "actual time unverified") || !strings.Contains(text, "time 0 min per task (user CSV)") {
		t.Fatalf("misattributed explicit zero: %s", text)
	}
	snap.Scores[0].Effort = "high"
	text = server.CrewContext(allInDefault(crew.Resolve(c, nil, snap, patch)), agent.Info{})
	if !strings.Contains(text, "AA source effort: high") {
		t.Fatalf("lost named effort: %s", text)
	}
}

func TestScorePatchDialogs(t *testing.T) {
	a, _ := crewApp(t)
	s := &service{app: a}
	ctx := context.Background()
	dir := t.TempDir()
	source := filepath.Join(dir, "import.csv")
	destination := filepath.Join(dir, "export.csv")
	if err := os.WriteFile(source, []byte(syntheticPatch), 0600); err != nil {
		t.Fatal(err)
	}
	oldOpen, oldSave := configOpenDialog, configSaveDialog
	t.Cleanup(func() { configOpenDialog, configSaveDialog = oldOpen, oldSave })
	configOpenDialog = func(mygo.OpenDialogOptions) ([]string, error) { return []string{source}, nil }
	configSaveDialog = func(mygo.SaveDialogOptions) (string, error) { return destination, nil }
	if _, err := s.ImportScorePatch(ctx); err != nil {
		t.Fatal(err)
	}
	for _, template := range []bool{true, false} {
		if _, err := s.ExportScorePatch(template); err != nil {
			t.Fatal(err)
		}
		got, _ := os.ReadFile(destination)
		want := syntheticPatch
		if template {
			want = crew.PatchHeader
		}
		if string(got) != want {
			t.Fatalf("export: %q", got)
		}
	}
	configSaveDialog = func(mygo.SaveDialogOptions) (string, error) { return a.scorePatchPath(), nil }
	if _, err := s.ExportScorePatch(true); err == nil {
		t.Fatal("template export replaced active patch")
	}
	if data, _ := os.ReadFile(a.scorePatchPath()); string(data) != syntheticPatch {
		t.Fatal("export changed active bytes")
	}
	configOpenDialog = func(mygo.OpenDialogOptions) ([]string, error) { return nil, nil }
	if got, err := s.ImportScorePatch(ctx); err != nil || got.Patch.Rows != 2 {
		t.Fatalf("cancel: %+v %v", got, err)
	}
	configSaveDialog = func(mygo.SaveDialogOptions) (string, error) { return "", nil }
	if path, err := s.ExportScorePatch(true); err != nil || path != "" {
		t.Fatalf("cancel export: %s %v", path, err)
	}
	// A save failure leaves both the current parsed state and original source.
	if err := os.Remove(a.scorePatchPath()); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(a.scorePatchPath(), 0700); err != nil {
		t.Fatal(err)
	}
	if err := a.importScorePatch(source); err == nil {
		t.Fatal("accepted invalid destination")
	}
	patch, status := a.effectiveScorePatch()
	if len(patch) != 2 || status.Rows != 2 {
		t.Fatal("save failure replaced memory")
	}
}

// Production-page fixture: real service/resolver/storage and synthetic catalog.
// Only MyGo dialog transport is substituted; it never starts the live app/pi.
func TestScorePatchBrowserBridge(t *testing.T) {
	if os.Getenv("YESMAN_TEST_SCORE_PATCH_BROWSER") != "1" {
		t.Skip("run scripts/score-patch-browser.mjs")
	}
	a, _ := crewApp(t)
	s := &service{app: a}
	ctx := context.Background()
	if _, err := s.AddCrewOption(ctx, "magpie", "auto/claude-opus-5-5", "max", nil); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	source := filepath.Join(dir, "import.csv")
	destination := filepath.Join(dir, "export.csv")
	oldOpen, oldSave := configOpenDialog, configSaveDialog
	t.Cleanup(func() { configOpenDialog, configSaveDialog = oldOpen, oldSave })
	configOpenDialog = func(mygo.OpenDialogOptions) ([]string, error) { return []string{source}, nil }
	configSaveDialog = func(mygo.SaveDialogOptions) (string, error) { return destination, nil }
	mux := http.NewServeMux()
	mux.HandleFunc("POST /fixture/call", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string            `json:"method"`
			Args   []json.RawMessage `json:"args"`
		}
		err := json.NewDecoder(r.Body).Decode(&req)
		var value any
		arg := func(i int, dst any) {
			if err == nil {
				if i >= len(req.Args) {
					err = fmt.Errorf("missing argument")
				} else {
					err = json.Unmarshal(req.Args[i], dst)
				}
			}
		}
		if err == nil {
			switch req.Method {
			case "Crew":
				value = s.Crew(r.Context())
			case "RefreshCrew":
				value = s.RefreshCrew(r.Context())
			case "FixtureGPT62":
				configureGPT62PatchCrew(t, a)
				value = s.Crew(r.Context())
			case "FixturePatch":
				var data []byte
				data, err = os.ReadFile(a.scorePatchPath())
				value = string(data)
			case "FixtureCSV":
				var text string
				arg(0, &text)
				if err == nil {
					err = os.WriteFile(source, []byte(text), 0600)
				}
			case "FixtureRestart":
				a = &app{home: a.home, catalog: a.catalog, settings: loadSettings(a.home.Settings())}
				s = &service{app: a}
				value = s.Crew(r.Context())
			case "FixtureContext":
				value = server.CrewContext(allInDefault(a.workerRouting(r.Context(), nil).Members), agent.Info{})
			case "SetWorkerPool":
				var id, revision string
				var p crew.Pool
				arg(0, &id)
				arg(1, &revision)
				arg(2, &p)
				if err == nil {
					value, err = s.SetWorkerPool(r.Context(), id, revision, p)
				}
			case "FixtureExport":
				var data []byte
				data, err = os.ReadFile(destination)
				value = string(data)
			case "ImportScorePatch":
				value, err = s.ImportScorePatch(r.Context())
			case "ExportScorePatch":
				var template bool
				arg(0, &template)
				if err == nil {
					value, err = s.ExportScorePatch(template)
				}
			case "SetScorePatchEnabled":
				var enabled bool
				arg(0, &enabled)
				if err == nil {
					value, err = s.SetScorePatchEnabled(r.Context(), enabled)
				}
			case "AddCrewOption":
				var provider, model, effort string
				var score *crew.Score
				arg(0, &provider)
				arg(1, &model)
				arg(2, &effort)
				arg(3, &score)
				if err == nil {
					value, err = s.AddCrewOption(r.Context(), provider, model, effort, score)
				}
			case "SetCrewScore":
				var key string
				var score crew.Score
				arg(0, &key)
				arg(1, &score)
				if err == nil {
					value, err = s.SetCrewScore(r.Context(), key, score)
				}
			case "RemoveCrewOption":
				var key string
				arg(0, &key)
				if err == nil {
					value, err = s.RemoveCrewOption(r.Context(), key)
				}
			case "SetYesmanModel":
				var key string
				arg(0, &key)
				if err == nil {
					value, err = s.SetYesmanModel(r.Context(), key)
				}
			default:
				err = fmt.Errorf("unsupported fixture method %s", req.Method)
			}
		}
		response := map[string]any{"value": value}
		if err != nil {
			response["error"] = err.Error()
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(response)
	})
	web, err := fs.Sub(webFS, "web")
	if err != nil {
		t.Fatal(err)
	}
	mux.Handle("/", http.FileServerFS(web))
	backend := httptest.NewServer(mux)
	defer backend.Close()
	fmt.Println("SCORE_PATCH_BROWSER_URL=" + backend.URL)
	_, _ = io.Copy(io.Discard, os.Stdin)
}
