// Package server is the local HTTP API that yesman's pi extension calls
// from inside each agent. It listens on loopback; every request carries the
// calling agent's own credential, YESMAN_TOKEN, which the agent manager
// issued when it started that agent and maps back to it.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/idy/yesman/internal/agent"
	"github.com/idy/yesman/internal/github"
	"github.com/idy/yesman/internal/pool"
	"github.com/idy/yesman/internal/yesmen"
)

// Server serves the API.
type Server struct {
	Pool   *pool.Pool
	Yesmen *yesmen.Store
	Agents *agent.Manager
	// Changed is told when the pool or a workspace's worktrees change.
	Changed func()
	// AddRepo, when set, clones a GitHub repository into the pool in place
	// of resolving and cloning it here, so the app can show it cloning.
	AddRepo func(ctx context.Context, repo string) (pool.Repo, error)

	url string
}

// Start listens on a loopback port and serves in the background.
func (s *Server) Start() error {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	s.url = "http://" + ln.Addr().String()
	srv := &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	return nil
}

// URL is the address the API serves at, after Start.
func (s *Server) URL() string { return s.url }

// Handler routes the API.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/context", s.handle(s.context))
	mux.HandleFunc("GET /v1/repos", s.handle(s.repos))
	mux.HandleFunc("POST /v1/repos", s.handle(s.addRepo))
	mux.HandleFunc("POST /v1/checkout", s.handle(s.checkout))
	mux.HandleFunc("POST /v1/worktrees/remove", s.handle(s.removeWorktree))
	mux.HandleFunc("GET /v1/agents", s.handle(s.agents))
	mux.HandleFunc("POST /v1/workers", s.handle(s.startWorker))
	mux.HandleFunc("POST /v1/workers/send", s.handle(s.sendWorker))
	mux.HandleFunc("POST /v1/workers/remove", s.handle(s.removeWorker))
	return mux
}

// caller is the agent a request comes from.
type caller struct {
	agent  agent.Info
	yesman yesmen.Yesman
}

type handlerFunc func(ctx context.Context, c caller, r *http.Request) (any, error)

func (s *Server) handle(fn handlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok {
			writeError(w, http.StatusUnauthorized, errors.New("no agent credential"))
			return
		}
		info, err := s.Agents.Authenticate(token)
		if err != nil {
			writeError(w, http.StatusUnauthorized, err)
			return
		}
		y, err := s.Yesmen.Get(info.Yesman)
		if err != nil {
			writeError(w, http.StatusForbidden, err)
			return
		}
		out, err := fn(r.Context(), caller{agent: info, yesman: y}, r)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if text, ok := out.(string); ok {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			_, _ = w.Write([]byte(text))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	}
}

func writeError(w http.ResponseWriter, code int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}

func decode(r *http.Request, v any) error {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		return fmt.Errorf("bad request body: %w", err)
	}
	return nil
}

func (s *Server) changed() {
	if s.Changed != nil {
		s.Changed()
	}
}

func (s *Server) repos(ctx context.Context, c caller, _ *http.Request) (any, error) {
	snapshot, err := s.Pool.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	return map[string]any{"repos": snapshot.Repos, "worktrees": snapshot.Under(c.yesman.Path)}, nil
}

func (s *Server) addRepo(_ context.Context, _ caller, r *http.Request) (any, error) {
	var req struct {
		Repo string `json:"repo"` // a GitHub repository: owner/repo or its URL
	}
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	// A clone outlives a dropped request, so that a retry finds it done.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	add := s.AddRepo
	if add == nil {
		add = func(ctx context.Context, repo string) (pool.Repo, error) {
			gh, err := github.Resolve(ctx, repo)
			if err != nil {
				return pool.Repo{}, err
			}
			return s.Pool.Add(ctx, gh.URL, gh.Upstream)
		}
	}
	repo, err := add(ctx, req.Repo)
	if err == nil {
		s.changed()
	}
	return repo, err
}

var dirRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._@-]{0,79}$`)

// CheckoutRequest asks for a worktree in the caller's workspace.
type CheckoutRequest struct {
	Repo   string `json:"repo"`
	Branch string `json:"branch,omitempty"`
	Base   string `json:"base,omitempty"`
	Ref    string `json:"ref,omitempty"`
	Dir    string `json:"dir,omitempty"`
}

// Checkout resolves a request's defaults and checks the worktree out.
func Checkout(ctx context.Context, p *pool.Pool, y yesmen.Yesman, req CheckoutRequest) (pool.Worktree, error) {
	repo, err := p.Get(req.Repo)
	if err != nil {
		return pool.Worktree{}, err
	}
	short := repo.Name[strings.LastIndex(repo.Name, "/")+1:]
	dir := req.Dir
	switch {
	case dir != "":
	case req.Ref != "":
		dir = short + "@" + sanitize(req.Ref)
	case req.Branch != "":
		b := req.Branch
		b = b[strings.LastIndex(b, "/")+1:]
		dir = short + "--" + sanitize(b)
	default:
		dir = short
	}
	if !dirRE.MatchString(dir) {
		return pool.Worktree{}, fmt.Errorf("%q is not a usable directory name", dir)
	}
	branch := req.Branch
	if branch == "" && req.Ref == "" {
		branch = "yesman/" + y.Name + "/" + dir
	}
	return p.AddWorktree(ctx, repo, pool.WorktreeOptions{
		Dest:   filepath.Join(y.Path, dir),
		Branch: branch,
		Base:   req.Base,
		Ref:    req.Ref,
	})
}

func sanitize(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	return strings.Trim(b.String(), "-.")
}

func (s *Server) checkout(_ context.Context, c caller, r *http.Request) (any, error) {
	var req CheckoutRequest
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	var wt pool.Worktree
	err := s.Yesmen.Use(c.yesman.Name, func(y yesmen.Yesman) error {
		var err error
		wt, err = Checkout(ctx, s.Pool, y, req)
		return err
	})
	if err == nil {
		s.changed()
	}
	return wt, err
}

func (s *Server) removeWorktree(ctx context.Context, c caller, r *http.Request) (any, error) {
	var req struct {
		Dir   string `json:"dir"`
		Force bool   `json:"force"`
	}
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	if !dirRE.MatchString(req.Dir) {
		return nil, fmt.Errorf("%q is not a worktree directory of this workspace", req.Dir)
	}
	if err := s.Yesmen.Use(c.yesman.Name, func(y yesmen.Yesman) error {
		return s.Pool.RemoveWorktree(ctx, filepath.Join(y.Path, req.Dir), req.Force)
	}); err != nil {
		return nil, err
	}
	s.changed()
	return map[string]bool{"removed": true}, nil
}

func (s *Server) agents(_ context.Context, c caller, _ *http.Request) (any, error) {
	var out []agent.Info
	for _, i := range s.Agents.List() {
		if i.Yesman == c.yesman.Name {
			i.Dialog = nil
			out = append(out, i)
		}
	}
	return out, nil
}

var errLeadOnly = errors.New("only the yesman's lead manages workers")

func (s *Server) startWorker(_ context.Context, c caller, r *http.Request) (any, error) {
	if c.agent.Role != agent.RoleLead {
		return nil, errLeadOnly
	}
	var req struct {
		Name string `json:"name"`
		Task string `json:"task"`
	}
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	return s.Agents.StartWorker(ctx, c.yesman.Name, req.Name, req.Task)
}

func (s *Server) sendWorker(_ context.Context, c caller, r *http.Request) (any, error) {
	if c.agent.Role != agent.RoleLead {
		return nil, errLeadOnly
	}
	var req struct {
		Name    string `json:"name"`
		Message string `json:"message"`
	}
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	if req.Name == yesmen.LeadName {
		return nil, errors.New("that is you")
	}
	id := agent.ID(c.yesman.Name, req.Name)
	if _, err := s.Agents.Get(id); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := s.Agents.Prompt(ctx, id, "[from your lead] "+req.Message); err != nil {
		return nil, err
	}
	return map[string]bool{"sent": true}, nil
}

func (s *Server) removeWorker(_ context.Context, c caller, r *http.Request) (any, error) {
	if c.agent.Role != agent.RoleLead {
		return nil, errLeadOnly
	}
	var req struct {
		Name string `json:"name"`
	}
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	if err := s.Agents.RemoveWorker(c.yesman.Name, req.Name); err != nil {
		return nil, err
	}
	return map[string]bool{"removed": true}, nil
}

func (s *Server) context(ctx context.Context, c caller, _ *http.Request) (any, error) {
	return BuildContext(ctx, s.Pool, s.Agents, c.yesman, c.agent), nil
}

const knowledgeLimit = 8000

// BuildContext writes the system prompt section yesman's extension gives
// an agent before each run.
func BuildContext(ctx context.Context, p *pool.Pool, agents *agent.Manager, y yesmen.Yesman, me agent.Info) string {
	var b strings.Builder
	w := func(format string, args ...any) { fmt.Fprintf(&b, format, args...) }

	if me.Role == agent.RoleLead {
		w("You are the lead of the yesman named %q. You talk with the user, plan, keep this yesman's knowledge, and get the work delivered. ", y.Name)
		w("Hand substantial, separable work to workers with yesman_worker_start: each is its own pi agent in this workspace and reports back to you when it finishes a run. Do small things yourself. ")
		w("Never sleep, poll or read session logs to wait for a worker: end your run, and its report arrives as a new message that starts your next one.\n")
	} else {
		w("You are %q, a worker of the yesman named %q. Your lead started you for one task and receives your final message when you finish a run, so end each run with a short report: what you did, where (worktree, branch, PR) and what is left.\n", me.Name, y.Name)
		if me.Task != "" {
			w("\nYour task, as given:\n%s\n", indent(me.Task))
		}
	}

	w("\n## Workspace\n\n")
	w("Your working directory %s is this yesman's workspace. It is not a git repository: each repository is a worktree in a subdirectory, checked out of yesman's shared repo pool. Run git inside a worktree or with `git -C <dir>`.\n", y.Path)
	w("- Get a worktree with yesman_checkout, never with git clone or git worktree add. Each worktree is on its own branch; ")
	w("give every piece of parallel work (each PR) its own worktree, and never edit a worktree another agent is using.\n")
	w("- Never write to the pool (%s). Commits, pushes and pull requests happen in worktrees as usual.\n", p.Root())
	snapshot, scanErr := p.Snapshot(ctx)
	if wts := snapshot.Under(y.Path); scanErr == nil && len(wts) > 0 {
		w("\nWorktrees here:\n")
		for _, wt := range wts {
			where := "branch " + wt.Branch
			if wt.Detached {
				where = "detached at " + short(wt.Head)
			}
			w("- %s: %s, %s\n", wt.Dir, wt.Repo, where)
		}
	} else {
		w("\nNo worktrees yet.\n")
	}
	if repos := snapshot.Repos; repos != nil || scanErr == nil {
		if len(repos) == 0 {
			w("\nThe repo pool is empty. Add a GitHub repository with yesman_repo_add.\n")
		} else {
			w("\nRepositories in the pool: ")
			for i, r := range repos {
				if i == 40 {
					w(", and %d more (yesman_repos lists them)", len(repos)-40)
					break
				}
				if i > 0 {
					w(", ")
				}
				w("%s", r.Name)
			}
			w(". Add another GitHub repository with yesman_repo_add.\n")
		}
	}

	rel := ".yesman/knowledge"
	w("\n## Knowledge\n\n")
	w("Every agent of this yesman shares %s/. Read a note when it bears on your work. When you learn something durable about this project (a decision, how something is built or released, a pitfall, the user's preference), write it down: one note per file, then add one line for it to %s/INDEX.md. Update a note rather than duplicating it.\n", rel, rel)
	if data, err := os.ReadFile(filepath.Join(y.KnowledgeDir(), "INDEX.md")); err == nil {
		text := strings.TrimSpace(string(data))
		if len(text) > knowledgeLimit {
			text = text[:knowledgeLimit] + "\n… (truncated; read the file for the rest)"
		}
		w("\n%s/INDEX.md:\n\n%s\n", rel, text)
	}

	var team []agent.Info
	for _, i := range agents.List() {
		if i.Yesman == y.Name && i.ID != me.ID {
			team = append(team, i)
		}
	}
	w("\n## Agents\n\n")
	w("Agent session logs are under .yesman/sessions/<name>/ as JSONL; read a worker's to see what it did.\n")
	if len(team) == 0 {
		w("No other agents.\n")
	}
	for _, i := range team {
		w("- %s (%s): %s", i.Name, i.Role, i.Status)
		if i.Task != "" {
			w(", task: %s", oneLine(i.Task, 120))
		}
		w("\n")
	}
	return b.String()
}

func indent(s string) string {
	return "  " + strings.ReplaceAll(strings.TrimSpace(s), "\n", "\n  ")
}

func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > n {
		s = s[:n] + "…"
	}
	return s
}

func short(sha string) string {
	if len(sha) > 10 {
		return sha[:10]
	}
	return sha
}
