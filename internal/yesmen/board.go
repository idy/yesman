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
}

// Open reports whether the task still needs work.
func (t Task) Open() bool { return t.Status != TaskDone && t.Status != TaskDropped }

// Board is a yesman's task list, in the order its agents keep it.
type Board struct {
	Tasks []Task `json:"tasks"`
	// Next numbers the next task added, so that IDs are never reused.
	Next int `json:"next"`
}

// TaskEdit adds a task, or changes the fields it sets of the task ID names.
type TaskEdit struct {
	ID     string  `json:"id,omitempty"`
	Title  *string `json:"title,omitempty"`
	Status *string `json:"status,omitempty"`
	Owner  *string `json:"owner,omitempty"`
	Detail *string `json:"detail,omitempty"`
	Note   *string `json:"note,omitempty"`
}

// BoardChange is one change to a board, applied whole or not at all.
type BoardChange struct {
	Add    []TaskEdit `json:"add,omitempty"`
	Update []TaskEdit `json:"update,omitempty"`
	Remove []string   `json:"remove,omitempty"`
	// Order lists task IDs in the order to show them; tasks it leaves out
	// follow, in their current order.
	Order []string `json:"order,omitempty"`
}

// Empty reports whether the change changes nothing.
func (c BoardChange) Empty() bool {
	return len(c.Add) == 0 && len(c.Update) == 0 && len(c.Remove) == 0 && len(c.Order) == 0
}

const (
	maxTasks     = 500
	maxTitleLen  = 200
	maxNoteLen   = 4000
	maxDetailLen = 8000
)

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
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := s.board(name)
	if err != nil {
		return Board{}, err
	}
	if err := b.apply(c, time.Now().UTC()); err != nil {
		return Board{}, err
	}
	y, _ := s.Get(name)
	if err := writeJSON(y.BoardFile(), b); err != nil {
		return Board{}, err
	}
	return b, nil
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

	for _, e := range c.Update {
		i := work.index(e.ID)
		if i < 0 {
			return fmt.Errorf("no task %q", e.ID)
		}
		t := work.Tasks[i]
		if err := edit(&t, e); err != nil {
			return fmt.Errorf("task %s: %w", t.ID, err)
		}
		t.UpdatedAt = now
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
	return nil
}
