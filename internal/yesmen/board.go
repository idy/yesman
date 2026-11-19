package yesmen

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Task statuses on a board.
const (
	TaskTodo    = "todo"
	TaskDoing   = "doing"
	TaskBlocked = "blocked"
	TaskDone    = "done"
	TaskDropped = "dropped"
)

// TaskStatuses lists the statuses a task can have.
var TaskStatuses = []string{TaskTodo, TaskDoing, TaskBlocked, TaskDone, TaskDropped}

// Task is one item of a yesman's board: a piece of the project's work, which
// a worker may own. It is the plan, kept by the agents; what an agent is
// doing right now is its status, not its task's.
type Task struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Status string `json:"status"`
	// Owner names the agent the task is handed to, usually a worker.
	Owner string `json:"owner,omitempty"`
	// Detail says what the task is, in Markdown.
	Detail string `json:"detail,omitempty"`
	// Note is where it stands: why it is blocked, or its result, with links
	// to its branch or pull request.
	Note      string    `json:"note,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
	// Schedule links a task that tracks one run of a scheduled task to it.
	// Only the scheduler sets it; board changes keep it as it is.
	Schedule *TaskSchedule `json:"schedule,omitempty"`
	// DoneAt is when the task was last marked done, and DoneOwner the agent
	// that owned it then, for counting tasks done by model. Both are kept
	// while it stays done and cleared when it is reopened or dropped. Tasks
	// done before yesman recorded this have neither.
	DoneAt    time.Time `json:"doneAt,omitzero"`
	DoneOwner string    `json:"doneOwner,omitempty"`
	// Delivered marks this task as the final step of a confirmed delivery.
	// It survives a status edit or restart so that delivery cannot replay.
	Delivered bool `json:"delivered,omitempty"`
	// OwnerSince is when the worker it was handed to started: a worker of
	// the same name hired later is not its owner. Zero means whichever
	// worker has that name now, as for a task handed to a worker before it
	// started, or before yesman recorded this; removing that worker sets it.
	OwnerSince time.Time `json:"ownerSince,omitzero"`
	// Wait says what a blocked task waits for, so that yesman can tell its
	// lead when it can go on. Only blocked tasks have one; a blocked task
	// without one, such as one blocked before yesman recorded waits, is
	// left blocked.
	Wait *Wait `json:"wait,omitempty"`
}

// What a blocked task can wait for.
const (
	WaitTasks    = "tasks"    // other tasks being done
	WaitUser     = "user"     // an open Needs you request
	WaitExternal = "external" // something outside yesman, such as CI or a third party
	WaitPaused   = "paused"   // the user paused it; only the lead sets or lifts it
)

// WaitKinds lists what a blocked task can wait for.
var WaitKinds = []string{WaitTasks, WaitUser, WaitExternal, WaitPaused}

// Wait is what a blocked task waits for and when it can go on.
type Wait struct {
	Kind string `json:"kind"`
	// Tasks are the tasks that must be done first, for kind tasks.
	Tasks []string `json:"tasks,omitempty"`
	// Handoff is the Needs you request it waits on, for kind user, besides
	// any request linked to the task.
	Handoff string `json:"handoff,omitempty"`
	// Reason says what it waits for and what lets it go on.
	Reason string    `json:"reason,omitempty"`
	Since  time.Time `json:"since"`
}

// Paused reports whether the user paused the task.
func (t Task) Paused() bool { return t.Wait != nil && t.Wait.Kind == WaitPaused }

// WaitEdit sets what a task waits for; an empty kind clears it.
type WaitEdit struct {
	Kind    string   `json:"kind"`
	Tasks   []string `json:"tasks,omitempty"`
	Handoff string   `json:"handoff,omitempty"`
	Reason  string   `json:"reason,omitempty"`
}

// TaskSchedule names the scheduled task (plan) and run a board task tracks.
type TaskSchedule struct {
	Plan string `json:"plan"`
	Run  string `json:"run"`
}

// Open reports whether the task still needs work.
func (t Task) Open() bool { return t.Status != TaskDone && t.Status != TaskDropped }

// Board is a yesman's task list, in the order its agents keep it.
type Board struct {
	Tasks []Task `json:"tasks"`
	// Next numbers the next task added, so that IDs are never reused.
	Next int `json:"next"`
	// ownerSet lists the tasks a change handed to an owner, for the Store
	// to bind to the owner's incarnation.
	ownerSet []string
}

// TaskEdit adds a task, or changes the fields it sets of the task ID names.
type TaskEdit struct {
	ID     string  `json:"id,omitempty"`
	Title  *string `json:"title,omitempty"`
	Status *string `json:"status,omitempty"`
	Owner  *string `json:"owner,omitempty"`
	Detail *string `json:"detail,omitempty"`
	Note   *string `json:"note,omitempty"`
	// Wait sets what the task waits for, which makes it blocked.
	Wait *WaitEdit `json:"wait,omitempty"`
}

// BoardChange is one change to a board, applied whole or not at all.
type BoardChange struct {
	Add    []TaskEdit `json:"add,omitempty"`
	Update []TaskEdit `json:"update,omitempty"`
	Remove []string   `json:"remove,omitempty"`
	// Order lists task IDs in the order to show them; tasks it leaves out
	// follow, in their current order.
	Order []string `json:"order,omitempty"`
	// Delivery confirms the work behind one existing final task. Only the
	// lead may confirm it, in the change that marks that task done.
	Delivery *DeliveryConfirmation `json:"delivery,omitempty"`
}

type DeliveryConfirmation struct {
	Task    string   `json:"task"`
	Related []string `json:"related,omitempty"`
}

// Empty reports whether the change changes nothing.
func (c BoardChange) Empty() bool {
	return len(c.Add) == 0 && len(c.Update) == 0 && len(c.Remove) == 0 && len(c.Order) == 0 && c.Delivery == nil
}

const (
	maxTasks     = 500
	maxTitleLen  = 200
	maxNoteLen   = 4000
	maxDetailLen = 8000
	maxWaitTasks = 20
	maxReasonLen = 1000
)

// ErrPauseByLead refuses a worker's change to a task the user paused, or a
// pause by a worker: only the lead, which hears the user, sets or lifts it.
var ErrPauseByLead = errors.New("only the lead pauses a task or lifts a pause, when the user asks")

// BoardFile is where a yesman's board is kept.
func (y Yesman) BoardFile() string { return filepath.Join(y.MetaDir(), "board.json") }

// Board returns a yesman's board.
func (s *Store) Board(name string) (Board, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.board(name)
}

func (s *Store) board(name string) (Board, error) {
	y, err := s.Get(name)
	if err != nil {
		return Board{}, err
	}
	var b Board
	if err := readJSON(y.BoardFile(), &b); err != nil && !errors.Is(err, os.ErrNotExist) {
		return Board{}, err
	}
	if b.Tasks == nil {
		b.Tasks = []Task{}
	}
	return b, nil
}

// ChangeBoard applies a change to a yesman's board and returns the board.
// It changes nothing when any part of the change is invalid.
func (s *Store) ChangeBoard(name string, c BoardChange) (Board, error) {
	return s.ChangeBoardBy(name, c, true)
}

// ChangeBoardBy is ChangeBoard for the lead, or for a worker, which cannot
// pause a task, lift a pause or change a paused task.
func (s *Store) ChangeBoardBy(name string, c BoardChange, lead bool) (Board, error) {
	if c.Delivery != nil {
		return Board{}, errors.New("a delivery needs the lead's commit admission")
	}
	return s.changeBoardAndForget(name, c, lead, nil, nil)
}

// ConfirmBoardDelivery changes the Board and records a lead's confirmation.
// The caller holds Use for y. Commit admits the final rename and only then
// announces the delivery, while both the Board and handoffs are held.
func (s *Store) ConfirmBoardDelivery(y Yesman, c BoardChange, commit func(publish func() error) error) (Board, error) {
	if c.Delivery == nil || commit == nil {
		return Board{}, errors.New("a delivery needs a confirmation and commit admission")
	}
	return s.changeBoardAndForget(y.Name, c, true, &y, commit)
}

func (s *Store) changeBoardAndForget(name string, c BoardChange, lead bool, scope *Yesman, commit func(func() error) error) (Board, error) {
	b, before, err := s.changeBoard(name, c, lead, scope, commit)
	if err != nil {
		return Board{}, err
	}
	// After the Store's lock: the record's own I/O holds up only this
	// yesman. A notice recorded meanwhile was refused by the revision this
	// change counted before it wrote the board.
	if y, err := s.Get(name); err == nil {
		_ = s.forgetNotices(y, before, b.Tasks) // best effort, after the board committed
	}
	return b, nil
}

func (s *Store) changeBoard(name string, c BoardChange, lead bool, scope *Yesman, commit func(func() error) error) (Board, []Task, error) {
	var handoffs Handoffs
	var file *handoffsFile
	if scope != nil {
		// AddHandoff takes the same order when it validates a task link.
		// Do the initial bounded Needs You read outside the Store mutex.
		lock := s.handoffLock(name)
		lock.Lock()
		defer lock.Unlock()
		var err error
		file, handoffs, err = openHandoffs(s.root, *scope, &s.metaMu)
		if err != nil {
			return Board{}, nil, fmt.Errorf("cannot confirm delivery without reading Needs You: %w", err)
		}
		defer file.close()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := s.board(name)
	if err != nil {
		return Board{}, nil, err
	}
	before := slices.Clone(b.Tasks)
	if err := b.apply(c, time.Now().UTC()); err != nil {
		return Board{}, nil, err
	}
	if !lead && pauseChanged(before, b.Tasks) {
		return Board{}, nil, ErrPauseByLead
	}
	if err := b.checkWaits(before); err != nil {
		return Board{}, nil, err
	}
	if c.Delivery != nil {
		if err := b.confirmDelivery(*c.Delivery, before, handoffs); err != nil {
			return Board{}, nil, err
		}
	}
	if len(b.ownerSet) > 0 {
		// Bind each task handed over to the worker of that name now.
		ws, err := s.workers(name)
		if err != nil {
			return Board{}, nil, err
		}
		for _, id := range b.ownerSet {
			i := b.index(id)
			if i < 0 {
				continue
			}
			t := &b.Tasks[i]
			t.OwnerSince = time.Time{}
			if j := slices.IndexFunc(ws, func(w Worker) bool { return w.Name == t.Owner }); j >= 0 && t.Owner != "" {
				t.OwnerSince = ws[j].StartedAt
			}
		}
		b.ownerSet = nil
	}
	if scope != nil {
		if err := s.writeDelivery(*scope, b, file, commit); err != nil {
			return Board{}, nil, err
		}
		return b, before, nil
	}
	y, err := s.Get(name)
	if err != nil {
		return Board{}, nil, err
	}
	s.changing(name)
	if err := writeJSON(y.BoardFile(), b); err != nil {
		return Board{}, nil, err
	}
	return b, before, nil
}

// forgetNotices forgets what the lead was told about tasks that moved: whose
// status, owner or wait changed, that were removed, or that wait for a task
// whose status changed. Such a task, back in the state it was told about,
// is told about again.
func (s *Store) forgetNotices(y Yesman, before, after []Task) error {
	old := map[string]Task{}
	for _, t := range before {
		old[t.ID] = t
	}
	now := map[string]bool{}
	status := map[string]bool{} // tasks whose status changed
	moved := map[string]bool{}
	for _, t := range after {
		now[t.ID] = true
		o, ok := old[t.ID]
		if !ok {
			continue
		}
		if o.Status != t.Status {
			status[t.ID] = true
		}
		if o.Status != t.Status || o.Owner != t.Owner || waitKey(o.Wait) != waitKey(t.Wait) {
			moved[t.ID] = true
		}
	}
	for id := range old {
		if !now[id] {
			moved[id], status[id] = true, true
		}
	}
	for _, t := range after {
		if t.Wait != nil && slices.ContainsFunc(t.Wait.Tasks, func(id string) bool { return status[id] }) {
			moved[t.ID] = true
		}
	}
	if len(moved) == 0 {
		return nil
	}
	_, err := s.UpdateContinuation(y.Name, y.HiredAt, func(c *Continuation) bool {
		changed := false
		for id := range moved {
			if _, ok := c.Tasks[id]; ok {
				delete(c.Tasks, id)
				changed = true
			}
		}
		return changed
	})
	return err
}

func waitKey(w *Wait) string {
	if w == nil {
		return ""
	}
	return w.Kind + ":" + strings.Join(w.Tasks, ",") + ":" + w.Handoff
}

// pauseChanged reports whether a paused task was changed or removed, or a
// task was paused.
func pauseChanged(before, after []Task) bool {
	old := map[string]Task{}
	for _, t := range before {
		old[t.ID] = t
	}
	seen := map[string]bool{}
	for _, t := range after {
		seen[t.ID] = true
		o, ok := old[t.ID]
		if t.Paused() && (!ok || !o.Paused()) {
			return true
		}
		if ok && o.Paused() && (!t.Paused() || t.Status != o.Status || t.Owner != o.Owner) {
			return true
		}
	}
	for _, o := range before {
		if o.Paused() && !seen[o.ID] {
			return true
		}
	}
	return false
}

// checkWaits checks that the tasks a new wait names exist, so that a typo
// never leaves a task waiting for ever. A task waited on that is removed
// later is the lead's to settle; yesman tells it.
func (b *Board) checkWaits(before []Task) error {
	old := map[string]*Wait{}
	for _, t := range before {
		old[t.ID] = t.Wait
	}
	for _, t := range b.Tasks {
		if t.Wait == nil || t.Wait.Kind != WaitTasks || old[t.ID] == t.Wait {
			continue
		}
		for _, id := range t.Wait.Tasks {
			if id == t.ID {
				return fmt.Errorf("task %s cannot wait for itself", t.ID)
			}
			if b.index(id) < 0 {
				return fmt.Errorf("task %s waits for %s, which is not on the board", t.ID, id)
			}
		}
	}
	return nil
}

// AddScheduledTask adds the board task that tracks a run of a scheduled
// task, or returns the one added for that run before, so that a run retried
// or recovered after a crash never gets a second task. added reports whether
// this call added it.
func (s *Store) AddScheduledTask(name string, link TaskSchedule, title, detail string) (task Task, added bool, err error) {
	if link.Plan == "" || link.Run == "" {
		return Task{}, false, errors.New("a scheduled task's board task needs its plan and run")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := s.board(name)
	if err != nil {
		return Task{}, false, err
	}
	for _, t := range b.Tasks {
		if t.Schedule != nil && t.Schedule.Run == link.Run {
			return t, false, nil
		}
	}
	title = clipBoardText(strings.Join(strings.Fields(title), " "), maxTitleLen)
	detail = clipBoardText(detail, maxDetailLen)
	if err := b.apply(BoardChange{Add: []TaskEdit{{Title: &title, Detail: &detail}}}, time.Now().UTC()); err != nil {
		return Task{}, false, err
	}
	i := len(b.Tasks) - 1
	b.Tasks[i].Schedule = &TaskSchedule{Plan: link.Plan, Run: link.Run}
	y, err := s.Get(name)
	if err != nil {
		return Task{}, false, err
	}
	s.changing(name) // counted before it is written, as every board change (see Revision)
	if err := writeJSON(y.BoardFile(), b); err != nil {
		return Task{}, false, err
	}
	return b.Tasks[i], true, nil
}

// UpdateScheduledTask changes the board task of a scheduled run when fn
// reports a change, and returns it. A task no longer on the board is left
// alone; found reports whether it is there.
func (s *Store) UpdateScheduledTask(name, run string, fn func(*Task) bool) (task Task, changed bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := s.board(name)
	if err != nil {
		return Task{}, false, err
	}
	for i, t := range b.Tasks {
		if t.Schedule == nil || t.Schedule.Run != run {
			continue
		}
		if !fn(&t) {
			return b.Tasks[i], false, nil
		}
		t.Note = clipBoardText(t.Note, maxNoteLen)
		if !slices.Contains(TaskStatuses, t.Status) {
			return b.Tasks[i], false, fmt.Errorf("status %q is not one of %s", t.Status, strings.Join(TaskStatuses, ", "))
		}
		t.Schedule = b.Tasks[i].Schedule
		now := time.Now().UTC()
		markDone(&t, b.Tasks[i].Status == TaskDone, now)
		t.UpdatedAt = now
		b.Tasks[i] = t
		y, err := s.Get(name)
		if err != nil {
			return Task{}, false, err
		}
		s.changing(name)
		if err := writeJSON(y.BoardFile(), b); err != nil {
			return Task{}, false, err
		}
		return t, true, nil
	}
	return Task{}, false, nil
}

func clipBoardText(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

// TaskID reads the ID of a task as agents may write it: t3, T3, #3 or 3.
func TaskID(s string) string {
	s = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(s), "#"))
	if _, err := strconv.Atoi(s); err == nil {
		return "t" + s
	}
	return s
}

func (b *Board) index(id string) int {
	id = TaskID(id)
	return slices.IndexFunc(b.Tasks, func(t Task) bool { return t.ID == id })
}

func (b *Board) apply(c BoardChange, now time.Time) error {
	tasks := slices.Clone(b.Tasks)
	next := b.Next
	work := Board{Tasks: tasks, Next: next}
	owned := func(e TaskEdit, id string) {
		if e.Owner != nil {
			work.ownerSet = append(work.ownerSet, id)
		}
	}

	for _, e := range c.Update {
		i := work.index(e.ID)
		if i < 0 {
			return fmt.Errorf("no task %q", e.ID)
		}
		t := work.Tasks[i]
		wasDone := t.Status == TaskDone
		if err := edit(&t, e); err != nil {
			return fmt.Errorf("task %s: %w", t.ID, err)
		}
		t.UpdatedAt = now
		markDone(&t, wasDone, now)
		owned(e, t.ID)
		work.Tasks[i] = t
	}
	for _, id := range c.Remove {
		i := work.index(id)
		if i < 0 {
			return fmt.Errorf("no task %q", id)
		}
		work.Tasks = slices.Delete(work.Tasks, i, i+1)
	}
	for _, e := range c.Add {
		if e.ID != "" {
			return fmt.Errorf("a new task gets its ID from the board; to change task %s, update it", e.ID)
		}
		if e.Title == nil {
			return errors.New("a new task needs a title")
		}
		work.Next++
		t := Task{ID: "t" + strconv.Itoa(work.Next), Status: TaskTodo, CreatedAt: now, UpdatedAt: now}
		if err := edit(&t, e); err != nil {
			return fmt.Errorf("new task %q: %w", *e.Title, err)
		}
		markDone(&t, false, now)
		owned(e, t.ID)
		work.Tasks = append(work.Tasks, t)
	}
	if len(work.Tasks) > maxTasks {
		return fmt.Errorf("a board holds at most %d tasks: remove finished ones", maxTasks)
	}
	if len(c.Order) > 0 {
		ordered := make([]Task, 0, len(work.Tasks))
		seen := map[string]bool{}
		for _, id := range c.Order {
			i := work.index(id)
			if i < 0 {
				return fmt.Errorf("no task %q to order", id)
			}
			if seen[work.Tasks[i].ID] {
				continue
			}
			seen[work.Tasks[i].ID] = true
			ordered = append(ordered, work.Tasks[i])
		}
		for _, t := range work.Tasks {
			if !seen[t.ID] {
				ordered = append(ordered, t)
			}
		}
		work.Tasks = ordered
	}
	*b = work
	return nil
}

// markDone records when a task became done and who owned it then, and
// forgets both once it is not done. A task that stays done keeps them,
// even when its owner changes; one done before they were recorded is not
// given a time it was not done at.
func markDone(t *Task, wasDone bool, now time.Time) {
	switch {
	case t.Status != TaskDone:
		t.DoneAt, t.DoneOwner = time.Time{}, ""
	case !wasDone:
		t.DoneAt, t.DoneOwner = now, t.Owner
	}
}

func edit(t *Task, e TaskEdit) error {
	if e.Title != nil {
		title := strings.Join(strings.Fields(*e.Title), " ")
		if title == "" {
			return errors.New("the title is empty")
		}
		if utf8.RuneCountInString(title) > maxTitleLen {
			return fmt.Errorf("the title is longer than %d characters: put the rest in detail", maxTitleLen)
		}
		t.Title = title
	}
	if e.Status != nil {
		status := strings.ToLower(strings.TrimSpace(*e.Status))
		if !slices.Contains(TaskStatuses, status) {
			return fmt.Errorf("status %q is not one of %s", *e.Status, strings.Join(TaskStatuses, ", "))
		}
		t.Status = status
	}
	if e.Owner != nil {
		owner := strings.TrimSpace(*e.Owner)
		if owner != "" {
			if err := ValidName(owner); err != nil {
				return fmt.Errorf("owner: %w", err)
			}
		}
		t.Owner = owner
	}
	if e.Detail != nil {
		if utf8.RuneCountInString(*e.Detail) > maxDetailLen {
			return fmt.Errorf("the detail is longer than %d characters", maxDetailLen)
		}
		t.Detail = strings.TrimSpace(*e.Detail)
	}
	if e.Note != nil {
		if utf8.RuneCountInString(*e.Note) > maxNoteLen {
			return fmt.Errorf("the note is longer than %d characters", maxNoteLen)
		}
		t.Note = strings.TrimSpace(*e.Note)
	}
	if e.Wait != nil {
		w, err := newWait(*e.Wait, t.Wait)
		if err != nil {
			return fmt.Errorf("wait: %w", err)
		}
		t.Wait = w
		if w != nil {
			if e.Status != nil && t.Status != TaskBlocked {
				return fmt.Errorf("a task that waits is blocked, not %s", t.Status)
			}
			t.Status = TaskBlocked
		}
	}
	if t.Status != TaskBlocked {
		t.Wait = nil // going on, or finished, it no longer waits
	}
	return nil
}

// newWait checks a wait as an agent gives it. An unchanged wait keeps when
// it began.
func newWait(e WaitEdit, old *Wait) (*Wait, error) {
	kind := strings.ToLower(strings.TrimSpace(e.Kind))
	if kind == "" {
		return nil, nil
	}
	if !slices.Contains(WaitKinds, kind) {
		return nil, fmt.Errorf("kind %q is not one of %s", e.Kind, strings.Join(WaitKinds, ", "))
	}
	w := &Wait{Kind: kind, Reason: strings.TrimSpace(e.Reason), Since: time.Now().UTC()}
	if utf8.RuneCountInString(w.Reason) > maxReasonLen {
		return nil, fmt.Errorf("the reason is longer than %d characters", maxReasonLen)
	}
	switch kind {
	case WaitTasks:
		for _, id := range e.Tasks {
			if id = TaskID(id); id != "" && !slices.Contains(w.Tasks, id) {
				w.Tasks = append(w.Tasks, id)
			}
		}
		if len(w.Tasks) == 0 {
			return nil, errors.New("name the tasks it waits for")
		}
		if len(w.Tasks) > maxWaitTasks {
			return nil, fmt.Errorf("it can wait for at most %d tasks", maxWaitTasks)
		}
	case WaitUser:
		w.Handoff = strings.ToLower(strings.TrimSpace(e.Handoff))
	case WaitExternal, WaitPaused:
		if w.Reason == "" {
			return nil, fmt.Errorf("say in reason what it waits for and what lets it go on")
		}
	}
	if kind != WaitTasks && len(e.Tasks) > 0 {
		return nil, fmt.Errorf("tasks are for kind %s", WaitTasks)
	}
	if old != nil && old.Kind == w.Kind && slices.Equal(old.Tasks, w.Tasks) && old.Handoff == w.Handoff {
		w.Since = old.Since
	}
	return w, nil
}
