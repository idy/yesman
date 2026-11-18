package schedule

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/idy/yesman/internal/yesmen"
)

// View is what Tasks shows of a workspace.
type View struct {
	Yesman string `json:"yesman"`
	// Owner: this app runs the tasks. Otherwise another yesman app does,
	// and they are read-only here.
	Owner bool `json:"owner"`
	// Error says why the tasks file cannot be read; it is left unchanged.
	Error string `json:"error,omitempty"`
	// Notice says why part of the history cannot be kept as it should
	// (what pruning removed cannot be read): tasks still run, nothing is
	// pruned, and Run now waits until it can be read.
	Notice string     `json:"notice,omitempty"`
	Now    time.Time  `json:"now"`
	Plans  []PlanView `json:"plans"`
	Limits Limits     `json:"limits"`
}

// Limits are the bounds of a plan, for the form.
type Limits struct {
	Title        int `json:"title"`
	Instructions int `json:"instructions"`
	Plans        int `json:"plans"`
	MinEvery     int `json:"minEvery"`
	MaxEvery     int `json:"maxEvery"`
	History      int `json:"history"`
	OnTime       int `json:"onTimeMinutes"`
}

// PlanView is a plan with where it stands.
type PlanView struct {
	Plan
	// State: on, off, finished (a once plan whose time came), invalid or
	// archived.
	State string `json:"state"`
	// Next is when it is next due; empty when it is off or has no more.
	Next    *time.Time `json:"next,omitempty"`
	Problem string     `json:"problem,omitempty"`
	// Sending: a run of it is on its way to the lead now.
	Sending bool `json:"sending"`
	// Open is the run of it whose board task is still open, if any.
	Open string `json:"open,omitempty"`
	// Runs are its newest records, at most maxRunsPerPlan; More counts the
	// older ones kept and not listed. Attention counts, of them all, the
	// runs that need the user: missed without a catch-up, failed, unknown.
	Runs      []RunView `json:"runs"`
	More      int       `json:"more,omitempty"`
	Attention int       `json:"attention,omitempty"`
}

// RunView is a run with its board task as it is now.
type RunView struct {
	Run
	// Status is what the user sees: sending, sent, queued, failed, unknown,
	// missed, skipped, damaged, or its board task's status (todo, doing,
	// blocked, done, dropped) or removed.
	Status string `json:"status"`
	// Task is its board task's title and status, while the board has it.
	Task *TaskRef `json:"task,omitempty"`
	// What the user can do with it.
	CanRetry    bool `json:"canRetry,omitempty"`
	CanCheck    bool `json:"canCheck,omitempty"`
	CanRunAgain bool `json:"canRunAgain,omitempty"`
	CanCatchUp  bool `json:"canCatchUp,omitempty"`
}

// TaskRef is a board task as a run shows it.
type TaskRef struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Status string `json:"status"`
	Owner  string `json:"owner,omitempty"`
}

// View returns a workspace's tasks.
func (s *Service) View(name string) (View, error) {
	return s.view(name, time.Time{})
}

func (s *Service) view(name string, since time.Time) (View, error) {
	// The workspace is held while its state is found and read: its
	// generation, checked here, stays the current one until then, so the
	// state found is the current one's and no other's is replaced.
	y, release, err := s.cfg.Yesmen.BeginUse(name)
	if err != nil {
		return View{}, err
	}
	defer release()
	if err := hiredAt(y, since); err != nil {
		return View{}, err
	}
	board, boardErr := s.cfg.Yesmen.Board(name)
	tasks := boardTasks(board, boardErr)
	owner := s.Owner()
	sp := s.spaceFor(y)
	now := s.now()
	v := View{Yesman: name, Owner: owner, Now: now, Plans: []PlanView{}, Limits: Limits{
		Title: MaxTitle, Instructions: MaxInstructions, Plans: MaxActivePlans,
		MinEvery: MinEvery, MaxEvery: MaxEvery, History: keepRuns, OnTime: int(OnTime / time.Minute),
	}}
	sp.mu.Lock()
	defer sp.mu.Unlock()
	if !owner {
		// Another app writes them: read them again each time.
		sp.loaded, sp.runs = false, nil
	}
	s.loadLocked(sp, y)
	if sp.err != nil {
		v.Error = sp.err.Error()
	}
	if sp.prunedErr != nil {
		v.Notice = sp.prunedErr.Error()
	}
	for _, p := range sp.plans.Plans {
		v.Plans = append(v.Plans, s.planView(sp, p, tasks, boardErr, now))
	}
	// Plans in the order they were made; archived ones after the rest.
	slices.SortStableFunc(v.Plans, func(a, b PlanView) int {
		if a.Archived() != b.Archived() {
			if a.Archived() {
				return 1
			}
			return -1
		}
		return planNumber(a.ID) - planNumber(b.ID)
	})
	return v, nil
}

func (s *Service) planView(sp *space, p Plan, tasks map[string]yesmen.Task, boardErr error, now time.Time) PlanView {
	pv := PlanView{Plan: p, Problem: sp.problem[p.ID], Runs: []RunView{}}
	records := plansRuns(sp.runs, p.ID)
	loc, zoneErr := LoadZone(p.Timezone)
	spec, specErr := p.Schedule.normalize()
	switch {
	case p.Archived():
		pv.State = "archived"
	case zoneErr != nil:
		pv.State, pv.Problem = "invalid", zoneErr.Error()
	case specErr != nil:
		pv.State, pv.Problem = "invalid", specErr.Error()
	case !p.Enabled:
		pv.State = "off"
	default:
		pv.State = "on"
		if n, ok := spec.next(cursor(p, records), loc, p.ActiveSince); ok {
			at := n.At
			pv.Next = &at
		} else if spec.Kind == KindOnce {
			pv.State = "finished"
		}
	}
	for _, r := range records {
		if r.fired() && r.inFlight() {
			pv.Sending = true
		}
		if pv.Open == "" && open(r.Run, tasks, boardErr) && !r.inFlight() {
			pv.Open = r.ID
		}
	}
	for i, r := range records {
		v := runView(r.Run, p, tasks, boardErr, sp.active[r.ID])
		if v.CanCatchUp || v.Status == StateFailed || v.Status == StateUnknown {
			pv.Attention++
		}
		if i < maxRunsPerPlan {
			pv.Runs = append(pv.Runs, v)
		} else {
			pv.More++
		}
	}
	return pv
}

func runView(r Run, p Plan, tasks map[string]yesmen.Task, boardErr error, active bool) RunView {
	r.BoardSync = nil // the scheduler's own bookkeeping
	v := RunView{Run: r, Status: r.State}
	t, onBoard := taskOf(r, tasks)
	if onBoard {
		v.Task = &TaskRef{ID: t.ID, Title: t.Title, Status: t.Status, Owner: t.Owner}
	}
	switch r.State {
	case StateClaimed, StateBoarded, StateSending:
		v.Status = "sending"
	case StateSent, StateQueued, StateUnknown:
		// Queued while pi reports it in its queue; once the queue moved on,
		// what is known is that pi accepted it.
		if a := r.attempt(); r.State == StateQueued && a != nil && a.State != "queued" {
			v.Status = StateSent
		}
		switch {
		case onBoard && t.Status != yesmen.TaskTodo:
			// The lead took it up: its board task says where it stands.
			v.Status = t.Status
		case !onBoard && boardErr == nil && r.Board != "":
			v.Status = "removed"
			if r.Outcome != nil && r.Outcome.Status != "removed" && r.Outcome.Status != yesmen.TaskTodo {
				v.Status = r.Outcome.Status // how it stood when it was removed
			}
		}
	}
	live := !p.Archived()
	v.CanRetry = live && !active && canRetry(r)
	v.CanCheck = !active && r.attempt() != nil && (r.State == StateUnknown || r.State == StateSent || r.State == StateQueued)
	v.CanRunAgain = live && (r.State == StateUnknown || r.State == StateFailed && !canRetry(r)) && r.fired()
	v.CanCatchUp = live && r.Kind == RunMissed && r.CaughtUp == ""
	return v
}

// Summary is what the info panel shows of a workspace's tasks.
type Summary struct {
	Active int        `json:"active"` // on
	Total  int        `json:"total"`  // not archived
	Next   *time.Time `json:"next,omitempty"`
	// Attention counts runs that need the user: missed without a catch-up,
	// failed, unknown.
	Attention int  `json:"attention"`
	Owner     bool `json:"owner"`
	Error     bool `json:"error,omitempty"`
}

// Summarize returns a workspace's Summary.
func (s *Service) Summarize(name string) (Summary, error) {
	return s.summarize(name, time.Time{})
}

func (s *Service) summarize(name string, since time.Time) (Summary, error) {
	v, err := s.view(name, since)
	if err != nil {
		return Summary{}, err
	}
	sum := Summary{Owner: v.Owner, Error: v.Error != ""}
	for _, p := range v.Plans {
		if p.Archived() {
			continue
		}
		sum.Total++
		if p.State == "on" {
			sum.Active++
			if p.Next != nil && (sum.Next == nil || p.Next.Before(*sum.Next)) {
				sum.Next = p.Next
			}
		}
		sum.Attention += p.Attention
	}
	return sum, nil
}

// LeadContext is the section of a lead's context about its scheduled
// tasks: short, and only what it needs to handle their runs.
func (s *Service) LeadContext(name string) string {
	v, err := s.View(name)
	if err != nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n## Scheduled tasks\n\n")
	b.WriteString("The user keeps scheduled tasks in Tasks. When one is due, yesman sends you a [yesman] Scheduled task message naming the board task that tracks that run: handle it like a request from the user, and mark that board task done or blocked, with the outcome in its note, when the work ends. ")
	b.WriteString("Only the user creates, turns on, edits or runs scheduled tasks. Use yesman_schedules to list them; give it a draft only when the user asks you to schedule something: it is saved turned off, for the user to review and turn on in Tasks. Never draft one on your own.\n")
	n := 0
	for _, p := range v.Plans {
		if p.Archived() {
			continue
		}
		if n++; n > 20 {
			fmt.Fprintf(&b, "- and %d more (yesman_schedules lists them)\n", activeCount(plansOfView(v))-20)
			break
		}
		fmt.Fprintf(&b, "- %s %q: %s (%s), %s", p.ID, p.Title, p.Schedule.Describe(), p.Timezone, p.State)
		if p.Next != nil {
			fmt.Fprintf(&b, ", next %s", p.Next.In(zoneOrUTC(p.Timezone)).Format("2006-01-02 15:04 MST"))
		}
		b.WriteString("\n")
	}
	if n == 0 {
		b.WriteString("There are no scheduled tasks.\n")
	}
	return b.String()
}

func plansOfView(v View) []Plan {
	out := make([]Plan, len(v.Plans))
	for i, p := range v.Plans {
		out[i] = p.Plan
	}
	return out
}

// ListText describes the tasks and their recent runs for the lead's tool.
func (s *Service) ListText(name string) (string, error) {
	v, err := s.View(name)
	if err != nil {
		return "", err
	}
	if v.Error != "" {
		return v.Error, nil
	}
	var b strings.Builder
	shown := 0
	for _, p := range v.Plans {
		if p.Archived() {
			continue
		}
		shown++
		fmt.Fprintf(&b, "- %s %q: %s (%s), %s", p.ID, p.Title, p.Schedule.Describe(), p.Timezone, p.State)
		if p.CreatedBy == "lead" {
			b.WriteString(", drafted by you")
		}
		if p.Next != nil {
			fmt.Fprintf(&b, ", next %s", p.Next.In(zoneOrUTC(p.Timezone)).Format("2006-01-02 15:04 MST"))
		}
		fmt.Fprintf(&b, "\n  instructions: %s\n", strings.ReplaceAll(clip(p.Instructions, 400), "\n", "\n  "))
		for i, r := range p.Runs {
			if i == 5 {
				break
			}
			fmt.Fprintf(&b, "  run %s: %s", r.At.In(zoneOrUTC(p.Timezone)).Format("2006-01-02 15:04"), r.Status)
			if r.Board != "" {
				fmt.Fprintf(&b, ", board task %s", r.Board)
			}
			if r.Count > 1 {
				fmt.Fprintf(&b, ", %d occurrences", r.Count)
			}
			b.WriteString("\n")
		}
	}
	if shown == 0 {
		return "There are no scheduled tasks. The user creates them in Tasks.", nil
	}
	return b.String(), nil
}
