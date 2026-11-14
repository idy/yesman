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
	"github.com/idy/yesman/internal/secrets"
	"github.com/idy/yesman/internal/yesmen"
)

// Server serves the API.
type Server struct {
	Pool   *pool.Pool
	Yesmen *yesmen.Store
	Agents *agent.Manager
	// Changed is told when the pool or a workspace's worktrees change.
	Changed func()
	// BoardChanged is told when a yesman's board changes.
	BoardChanged func(yesman string)
	// AddRepo, when set, clones a GitHub repository into the pool in place
	// of resolving and cloning it here, so the app can show it cloning.
	AddRepo func(ctx context.Context, repo string) (pool.Repo, error)
	// Secrets and Variables, when set, are set in agents' commands.
	Secrets   *secrets.Vault
	Variables *secrets.Variables

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
	mux.HandleFunc("GET /v1/env", s.handle(s.env))
	mux.HandleFunc("GET /v1/env/describe", s.handle(s.describeEnv))
	mux.HandleFunc("POST /v1/workers/answer", s.handle(s.answerWorker))
	mux.HandleFunc("GET /v1/board", s.handle(s.board))
	mux.HandleFunc("POST /v1/board", s.handle(s.changeBoard))
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
		Name  string `json:"name"`
		Title string `json:"title"`
		Task  string `json:"task"`
	}
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	return s.Agents.StartWorker(ctx, c.yesman.Name, req.Name, req.Title, req.Task)
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

// answerWorker answers the question a worker waits on, for the lead, which
// answers for its workers before the user does.
func (s *Server) answerWorker(_ context.Context, c caller, r *http.Request) (any, error) {
	if c.agent.Role != agent.RoleLead {
		return nil, errLeadOnly
	}
	var req struct {
		Name string `json:"name"`
		agent.DialogReply
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
	if err := s.Agents.AnswerDialog(id, req.DialogReply); err != nil {
		return nil, err
	}
	return map[string]bool{"answered": true}, nil
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

// board returns the caller's yesman's board. Every agent of the yesman reads
// and keeps it, so that the lead can hand keeping it to a worker.
func (s *Server) board(_ context.Context, c caller, _ *http.Request) (any, error) {
	return s.Yesmen.Board(c.yesman.Name)
}

func (s *Server) changeBoard(_ context.Context, c caller, r *http.Request) (any, error) {
	var change yesmen.BoardChange
	if err := decode(r, &change); err != nil {
		return nil, err
	}
	if change.Empty() {
		return s.Yesmen.Board(c.yesman.Name)
	}
	var b yesmen.Board
	if err := s.Yesmen.Use(c.yesman.Name, func(y yesmen.Yesman) error {
		var err error
		b, err = s.Yesmen.ChangeBoard(y.Name, change)
		return err
	}); err != nil {
		return nil, err
	}
	if s.BoardChanged != nil {
		s.BoardChanged(c.yesman.Name)
	}
	return b, nil
}

func (s *Server) context(ctx context.Context, c caller, _ *http.Request) (any, error) {
	board, _ := s.Yesmen.Board(c.yesman.Name)
	return BuildContext(ctx, s.Pool, s.Agents, c.yesman, c.agent, board) + s.envContext(c.agent.Role == agent.RoleLead), nil
}

const knowledgeLimit = 8000

// BuildContext writes the system prompt section yesman's extension gives
// an agent before each run.
func BuildContext(ctx context.Context, p *pool.Pool, agents *agent.Manager, y yesmen.Yesman, me agent.Info, board yesmen.Board) string {
	var b strings.Builder
	w := func(format string, args ...any) { fmt.Fprintf(&b, format, args...) }

	if me.Role == agent.RoleLead {
		w("You are the lead of the yesman named %q. You talk with the user, plan, keep the board and this yesman's knowledge, and get the work delivered by workers.\n\n", y.Label())
		w("Stay free for the user: never do the work yourself. You have no shell, and the only files you may write are knowledge notes. Read files only to answer a quick question, not to investigate. ")
		w("Hand everything else to workers with yesman_worker_start (changes, commands, tests, research across many files, reviews, releases), each with a title naming its subject, then end your run. Each worker is its own pi agent in this workspace; several can run at once. ")
		w("Keep every run short: answer, delegate, end.\n")
		w("Never sleep, poll or read session logs to wait for a worker: end your run, and its report arrives as a new message that starts your next one. You also hear when a worker's run was stopped by the user or failed.\n")
		w("\n## Seeing it through\n\n")
		w("You answer for this yesman: what you take on, you see through to the end. The user should not have to look after the workers. ")
		w("When a report arrives, check it against the board, mark the task, and start the next step yourself (follow-ups, fixes, reviews) until everything the user asked for is done.\n")
		w("- A worker's question arrives as a [yesman] message: answer it with yesman_worker_answer, or with yesman_worker_send when the worker asked in its report.\n")
		w("- A worker whose process failed: message it with yesman_worker_send to start it again, or start a fresh worker. Tell the user only if it keeps failing.\n")
		w("- A worker's run the user stopped: that was their call. Leave it stopped and mark its task blocked, unless they say otherwise.\n")
		w("- When you end a run with tasks still todo or doing and no worker running, yesman reminds you once.\n")
		w("Decide everything you can. Bring the user only what you cannot decide: what to build, trade-offs only they can weigh, taste, credentials, or anything irreversible they did not ask for. ")
		w("Never hold the rest of the work for these: mark each task that waits on one blocked, with the question in its note, finish everything else, and then put the open decisions to the user together, each with the options and your recommendation.\n")
	} else {
		w("You are %q, a worker of the yesman named %q. Your lead started you for one task and receives your final message when you finish a run, so end each run with a short report: what you did, where (worktree, branch, PR) and what is left.\n", me.Name, y.Label())
		w("Do not stop to ask. When something is unclear, make the reasonable choice, say so in your report, and go on. If you cannot go on without a decision, finish everything that does not depend on it, then end your run with the question in your report: your lead answers it, not the user. Questions your tools ask go to your lead too.\n")
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

	w("\n## Board\n\n")
	w("The board is this yesman's task list, which the user watches beside the conversation. Read and change it with yesman_board. ")
	w("Statuses: todo, doing, blocked (say why in the note), done (put the result and its links, such as the branch or PR, in the note), dropped. ")
	if me.Role == agent.RoleLead {
		w("Add a task for each piece of work the user asks for, and set its owner when you hand it to a worker. Workers keep their own tasks current; when the board needs more than a quick change, such as reconciling it with reports, have a worker do it.\n")
	} else {
		w("Keep the tasks you own (owner %q) current: doing when you start, blocked when you cannot go on, done when you finish. Change other tasks only when your task is to keep the board.\n", me.Name)
	}
	writeBoard(&b, board)

	var team []agent.Info
	for _, i := range agents.List() {
		if i.Yesman == y.Name && i.ID != me.ID {
			team = append(team, i)
		}
	}
	w("\n## Agents\n\n")
	w("Agent session logs are under .yesman/sessions/<name>/ as JSONL; read a worker's to see what it did. The user sees workers by number, a1, a2 and so on, beside their subject, and may name them that way; you address them by name.\n")
	if len(team) == 0 {
		w("No other agents.\n")
	}
	finished := 0
	for _, i := range team {
		if !i.FinishedAt.IsZero() {
			finished++
			continue
		}
		if i.Number > 0 {
			w("- a%d %s (%s): %s", i.Number, i.Name, i.Role, i.Status)
		} else {
			w("- %s (%s): %s", i.Name, i.Role, i.Status)
		}
		if i.Activity != "" {
			w(", now: %s", i.Activity)
		}
		if i.Title != "" {
			w(", on: %s", i.Title)
		}
		w("\n")
	}
	if finished > 0 {
		w("Done, reported and closed (a message starts one again, with its session): ")
		n := 0
		for _, i := range team {
			if !i.FinishedAt.IsZero() {
				if n++; n > 1 {
					w(", ")
				}
				w("a%d %s (%s)", i.Number, i.Name, oneLine(i.Title, 80))
			}
		}
		w(".\n")
	}
	return b.String()
}

const boardLimit = 60

// writeBoard lists a board's open tasks for an agent's context.
func writeBoard(b *strings.Builder, board yesmen.Board) {
	open, closed := 0, 0
	for _, t := range board.Tasks {
		if !t.Open() {
			closed++
			continue
		}
		if open++; open > boardLimit {
			continue
		}
		fmt.Fprintf(b, "- %s [%s] %s", t.ID, t.Status, oneLine(t.Title, 200))
		if t.Owner != "" {
			fmt.Fprintf(b, " (owner %s)", t.Owner)
		}
		if t.Note != "" {
			fmt.Fprintf(b, ": %s", oneLine(t.Note, 160))
		}
		b.WriteString("\n")
	}
	switch {
	case open == 0 && closed == 0:
		b.WriteString("The board is empty.\n")
	case open == 0:
		b.WriteString("No open tasks.\n")
	case open > boardLimit:
		fmt.Fprintf(b, "… and %d more open tasks (yesman_board lists them)\n", open-boardLimit)
	}
	if closed > 0 {
		fmt.Fprintf(b, "%d done or dropped (yesman_board lists them).\n", closed)
	}
}

func indent(s string) string {
	return "  " + strings.ReplaceAll(strings.TrimSpace(s), "\n", "\n  ")
}

func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		s = string(r[:n]) + "…"
	}
	return s
}

func short(sha string) string {
	if len(sha) > 10 {
		return sha[:10]
	}
	return sha
}
