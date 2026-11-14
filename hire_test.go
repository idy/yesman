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
	"strings"
	"testing"

	"github.com/idy/yesman/internal/agent"
	"github.com/idy/yesman/internal/pool"
	"github.com/idy/yesman/internal/yesmen"
)

func hireService(t *testing.T) *service {
	t.Helper()
	a, _ := crewApp(t)
	a.agents = agent.New(agent.Config{Yesmen: a.yesmen})
	a.pool = pool.New(a.home.Repos())
	t.Cleanup(a.agents.StopAll)
	s := &service{app: a}
	if _, err := s.AddCrewOption(context.Background(), "magpie", "auto/claude-opus-5-5", "xhigh", nil); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestHireNamedServicePreservesLabelModelAndLegacyContract(t *testing.T) {
	s := hireService(t)
	ctx := context.Background()
	if _, err := s.Hire(ctx, "GizMoth"); err == nil {
		t.Fatal("legacy API accepted a display name as an internal ID")
	}
	y, err := s.HireNamed(ctx, "  GizMoth  ")
	if err != nil || y.Name != "gizmoth" || y.Label() != "GizMoth" || y.Model != "magpie/auto/claude-opus-5-5:xhigh" {
		t.Fatalf("named hire: %+v %v", y, err)
	}
	st, err := s.State(ctx)
	if err != nil || len(st.Yesmen) != 1 || st.Yesmen[0].Yesman != y || len(st.Agents) != 1 || st.Agents[0].ID != "gizmoth/lead" || st.Agents[0].Yesman != y.Name {
		t.Fatalf("initial state: %+v %v", st, err)
	}
	fresh := yesmen.New(s.app.home.Yesmen(), s.app.home.Archive())
	if got, err := fresh.Get(y.Name); err != nil || got != y {
		t.Fatalf("fresh store: %+v %v", got, err)
	}
	if old, err := s.Hire(ctx, "old-flow"); err != nil || old.Name != "old-flow" || old.DisplayName != "" || old.Label() != "old-flow" {
		t.Fatalf("legacy hire: %+v %v", old, err)
	}
	if _, err := s.Hire(ctx, "old-flow"); err == nil {
		t.Fatal("legacy conflict no longer fails")
	}
	if duplicate, err := s.HireNamed(ctx, "GizMoth"); err != nil || duplicate.Name != "gizmoth-2" || duplicate.Label() != y.Label() {
		t.Fatalf("duplicate label: %+v %v", duplicate, err)
	}
}

func TestHireNamedRequiresModelBeforeCreatingWorkspace(t *testing.T) {
	a, _ := crewApp(t)
	s := &service{app: a}
	if _, err := s.HireNamed(context.Background(), "GizMoth"); err == nil || !strings.Contains(err.Error(), "Settings › yesman") {
		t.Fatalf("missing model: %v", err)
	}
	if ys, err := a.yesmen.List(); err != nil || len(ys) != 0 {
		t.Fatalf("refused hire changed disk: %v %v", ys, err)
	}
}

// TestHireBrowserBridge is the optional production-page fixture used by
// scripts/hire-browser.mjs. All workspaces/settings live in t.TempDir; no pi
// process or model is started. Creation and State use the actual service.
func TestHireBrowserBridge(t *testing.T) {
	if os.Getenv("YESMAN_TEST_HIRE_BROWSER") != "1" {
		t.Skip("run through scripts/hire-browser.mjs")
	}
	s := hireService(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /fixture/call", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string   `json:"method"`
			Args   []string `json:"args"`
		}
		err := json.NewDecoder(r.Body).Decode(&req)
		var value any
		if err == nil {
			switch req.Method {
			case "State":
				value, err = s.State(r.Context())
			case "HireNamed", "Hire":
				if len(req.Args) != 1 {
					err = fmt.Errorf("expected one name")
				} else if req.Method == "HireNamed" {
					value, err = s.HireNamed(r.Context(), req.Args[0])
				} else {
					value, err = s.Hire(r.Context(), req.Args[0])
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
		if err := json.NewEncoder(w).Encode(response); err != nil {
			t.Error(err)
		}
	})
	web, err := fs.Sub(webFS, "web")
	if err != nil {
		t.Fatal(err)
	}
	mux.Handle("/", http.FileServerFS(web))
	server := httptest.NewServer(mux)
	defer server.Close()
	fmt.Println("HIRE_BROWSER_URL=" + server.URL)
	// The runner closes stdin after closing the browser, which shuts down the
	// fixture and lets testing remove its isolated data directory normally.
	_, _ = io.Copy(io.Discard, os.Stdin)
}
