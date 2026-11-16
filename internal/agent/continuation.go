package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"slices"
	"strings"
	"time"

	"github.com/idy/yesman/internal/yesmen"
)

// Continuation sees that the work a lead took on goes on. A run that ends,
// a worker's report or a process closed as finished does not finish a
// task, so whenever something changes (the lead settles, a worker's report
// was delivered, a worker failed, the board changed, the app started) yesman
// checks each open task on its own: is some worker carrying it, or does it
// wait for something that has not happened yet? It tells the lead, in one
// short notice, of the tasks for which neither holds. A busy worker on one
// task never hides another task nobody carries.
//
// What a task waits for is the board's structured Wait, never its note.
// yesman tells the lead; it never changes a task, starts a worker or lifts a
// wait. The user's pauses hold across restarts: a task paused on the board,
// a worker the user stopped and a lead the user stopped are never woken by
// yesman. A legacy blocked task without a wait is only listed for the lead
// to check, alongside a notice sent for something else.
//
// What the lead was told is kept in the workspace (yesmen.Continuation),
// recorded before a notice is sent and marked delivered only once pi
// accepted it, so that neither a restart nor a repeated event repeats a
// notice about an unchanged task, and the notices stay within a budget a
// restart does not reset.

const (
	// noticeLines bounds the tasks one notice lists, per section.
	noticeLines = 20
	// noticesPerState bounds the notices about one task while its status,
	// owner and wait stay the same, delivered or not.
	noticesPerState = 3
	// noticeBudget bounds the notices to one lead per noticeWindow.
	noticeBudget = 6
	// noticeHeader starts every notice.
	noticeHeader = "[yesman] These tasks need a next step"
)

var (
	noticeWindow = time.Hour
	// noticeRetries are the waits before trying again a notice pi did not
	// accept; the per-task bound applies to them as well.
	noticeRetries = []time.Duration{30 * time.Second, 2 * time.Minute}
)

// continuation is one workspace's evaluation, run by one goroutine at a
// time; guarded by the manager's mu.
type continuation struct {
	running, again bool
	retries        int
	timer          *time.Timer
}

// TasksChanged tells the manager that a yesman's board changed.
func (m *Manager) TasksChanged(yesman string) { go m.continueTasks(yesman) }

// Resume checks every yesman's open work once the app starts: a trigger lost
// with the app's last run, such as a worker that was working when it quit,
// is not lost. It starts no process unless a lead has something new to be
// told and the user did not pause it.
func (m *Manager) Resume() {
	ys, err := m.cfg.Yesmen.List()
	if err != nil {
		log.Printf("yesman: resume: %v", err)
		return
	}
	for _, y := range ys {
		go m.continueTasks(y.Name)
	}
}

// continueTasks evaluates a yesman's tasks, once at a time: a call while one
// runs makes it run again when it ends.
func (m *Manager) continueTasks(yesman string) {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	if m.continuations == nil {
		m.continuations = map[string]*continuation{}
	}
	c := m.continuations[yesman]
	if c == nil {
		c = &continuation{}
		m.continuations[yesman] = c
	}
	if c.running {
		c.again = true
		m.mu.Unlock()
		return
	}
	c.running = true
	m.mu.Unlock()
	for {
		m.evaluate(yesman, c)
		m.mu.Lock()
		if !c.again || m.closed {
			c.running, c.again = false, false
			m.mu.Unlock()
			return
		}
		c.again = false
		m.mu.Unlock()
	}
}

// later evaluates a yesman's tasks again after d, with one timer at most.
func (m *Manager) later(yesman string, c *continuation, d time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed || c.timer != nil {
		return
	}
	c.timer = time.AfterFunc(d, func() {
		m.mu.Lock()
		c.timer = nil
		m.mu.Unlock()
		m.continueTasks(yesman)
	})
}

// How a lead can be told: after its current state, or by starting it.
const (
	leadBusy = iota
	leadIdle
	leadAsleep
)

// leadStateLocked says whether yesman can tell the lead now, under mu. A
// lead that works, waits, starts, has a message on its way or a worker's
// report coming is busy: its next settle evaluates again. Other workers
// working does not matter.
func (m *Manager) leadStateLocked(yesman string) (*agent, *proc, uint64, int) {
	if m.closed {
		return nil, nil, 0, leadBusy
	}
	for _, w := range m.agents {
		if w.info.Yesman == yesman && w.info.Role == RoleWorker && w.reports > 0 {
			return nil, nil, 0, leadBusy
		}
	}
	lead := m.agents[ID(yesman, yesmen.LeadName)]
	if lead == nil {
		return nil, nil, 0, leadAsleep
	}
	if lead.retiring || lead.removing || lead.starting != nil || lead.prompts != 0 || lead.recalls != 0 {
		return lead, nil, 0, leadBusy
	}
	p := lead.proc
	if p == nil {
		if lead.info.Status == Stopped || lead.info.Status == Failed {
			return lead, nil, 0, leadAsleep
		}
		return lead, nil, 0, leadBusy
	}
	select {
	case <-p.done:
		return lead, nil, 0, leadBusy // watch is about to record how it ended
	default:
	}
	if lead.stopping || lead.info.Status != Idle || lead.inbox != lead.settledInbox {
		return lead, p, 0, leadBusy
	}
	return lead, p, lead.inbox, leadIdle
}

// ownerRun is what yesman knows of a task's owner now.
type ownerRun struct {
	since  time.Time // the generation running
	active bool
	status Status
}

// notice is one task a notice lists.
type notice struct {
	task           yesmen.Task
	why            string
	sig, progress  string
	verify         bool // a legacy wait, only listed for the lead to check
	count          int
	previousNotice yesmen.TaskNotice
	noticed        bool // previousNotice is one: the task had a record
}

// evaluate tells the lead of the tasks that need a next step, if it can be
// told now and has something new to be told.
func (m *Manager) evaluate(yesman string, c *continuation) {
	y, err := m.cfg.Yesmen.Get(yesman)
	if err != nil {
		return
	}
	// The revision is read before what it covers: if it is the same when the
	// notice is recorded, nothing read here was overtaken (see recordAttempt).
	rev := m.cfg.Yesmen.Revision(yesman)
	paused := m.loadPause(y)
	record, err := m.cfg.Yesmen.Continuation(yesman)
	if err != nil {
		log.Printf("yesman: continuation of %s: %v", yesman, err)
		return
	}
	board, err := m.cfg.Yesmen.Board(yesman)
	if err != nil {
		return
	}
	workers, err := m.cfg.Yesmen.Workers(yesman)
	if err != nil {
		return
	}
	handoffs, err := m.cfg.Yesmen.Handoffs(yesman)
	unreadable := err != nil
	if unreadable {
		// Unknown is not cleared: its waits stay blocked, to be checked.
		log.Printf("yesman: Needs you of %s: %v", yesman, err)
		handoffs = yesmen.Handoffs{}
	}
	runs := map[string]ownerRun{}
	m.mu.Lock()
	for _, a := range m.agents {
		if a.info.Yesman != yesman || a.info.Role != RoleWorker || a.retiring {
			continue
		}
		runs[a.info.Name] = ownerRun{since: a.info.Since, active: busyLocked(a), status: a.info.Status}
	}
	m.mu.Unlock()

	items := classifyAs(board, workers, runs, handoffs, unreadable)
	// Whatever the lead's state, a task that needs no notice now forgets the
	// one it had, so that it is told again once it needs one; it keeps its
	// count, so that a task going back and forth without progress stays
	// bounded.
	if record = m.forgetSettled(y, record, items); paused {
		return // the user stopped the lead: nothing starts it but the user
	}
	m.mu.Lock()
	_, _, _, state := m.leadStateLocked(yesman)
	m.mu.Unlock()
	if state == leadBusy {
		return
	}
	var listed []notice
	actionable := 0
	for _, n := range items {
		prev, seen := record.Tasks[n.task.ID]
		if seen && prev.Sig == n.sig && prev.Delivered {
			continue // told already, and nothing changed since
		}
		n.count = 1
		if seen && prev.Progress == n.progress {
			n.count = prev.Count + 1
		}
		if n.count > noticesPerState {
			continue // told enough while it did not move: the lead decides
		}
		n.previousNotice, n.noticed = prev, seen
		listed = append(listed, n)
		if !n.verify {
			actionable++
		}
	}
	if actionable == 0 {
		return // nothing new; legacy waits only go along with a notice
	}
	now := time.Now().UTC()
	recent := recentAttempts(record.Attempts, now)
	if len(recent) >= noticeBudget {
		m.later(yesman, c, recent[0].Add(noticeWindow).Sub(now)+time.Second)
		return
	}
	listed = limitNotices(listed)
	text := noticeText(listed)
	if m.beforeNotice != nil {
		m.beforeNotice() // tests: something changes after the board was read
	}
	// Reserve the lead, recheck it and record the attempt before sending,
	// so that a restart never forgets a notice that may have gone. The
	// user's Stop sets its pause under mu, so a notice either is reserved
	// before it (and the Stop ends what it started) or sees it here; no
	// lock is held while the notice is sent.
	m.mu.Lock()
	lead, p, inbox, again := m.leadStateLocked(yesman)
	stale := m.leadPausedLocked(y) || m.carriedLocked(yesman, listed)
	m.mu.Unlock()
	if stale {
		m.reevaluate(c)
		return
	}
	if again != state {
		return // it changed meanwhile; its own event evaluates again
	}
	if state == leadIdle {
		lead.modelMu.Lock()
		m.mu.Lock()
		l, p2, inbox2, s := m.leadStateLocked(yesman)
		if s != leadIdle || l != lead || p2 != p || inbox2 != inbox || m.leadPausedLocked(y) || m.carriedLocked(yesman, listed) {
			m.mu.Unlock()
			lead.modelMu.Unlock()
			m.reevaluate(c)
			return
		}
		// Not inbox: a notice pi refuses must not leave the lead looking
		// busy until its next run, which would hold off the retry.
		lead.prompts++
		m.mu.Unlock()
		lead.modelMu.Unlock()
	}
	release := func() {
		if state != leadIdle {
			return
		}
		lead.modelMu.Lock()
		m.mu.Lock()
		lead.prompts--
		m.mu.Unlock()
		lead.modelMu.Unlock()
	}
	if err := m.recordAttempt(y, listed, now, false, board, rev); err != nil {
		release()
		if errors.Is(err, errOvertaken) {
			m.reevaluate(c) // what was read changed meanwhile: read it again
		} else {
			log.Printf("yesman: record continuation of %s: %v", yesman, err)
		}
		return
	}
	if m.reviewBeforeTaskSend != nil {
		m.reviewBeforeTaskSend() // tests: the user acts before the notice is sent
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	origin := Origin{Source: OriginYesman, Kind: "tasks"}
	// Admitted only as it is written to pi: nothing read was changed since,
	// no worker took up a task it lists, and the user did not pause the
	// lead, whether it was idle, asleep or started for this.
	current := func() error {
		if m.cfg.Yesmen.Revision(yesman) != rev || m.carriedLocked(yesman, listed) {
			return errOvertaken
		}
		return nil
	}
	if state == leadIdle {
		err = m.sendPromptDelivery(ctx, lead, p, text, "followUp", "", origin, m.admitAuto(y, current))
	} else {
		since := y.HiredAt
		err = m.promptAdmitted(ctx, ID(yesman, yesmen.LeadName), text, "followUp", "", nil, &since, origin, current)
	}
	if errors.Is(err, errLeadPaused) || errors.Is(err, errOvertaken) {
		// Never written: it was not a notice, and costs nothing.
		m.unrecordAttempt(y, listed, now)
		if errors.Is(err, errOvertaken) {
			m.reevaluate(c)
		}
		return // paused: the lead hears when the user writes to it
	}
	if err != nil {
		log.Printf("yesman: tell the lead of %s about its tasks: %v", yesman, err)
		m.mu.Lock()
		retry := -1
		if c.retries < len(noticeRetries) {
			retry = c.retries
			c.retries++
		}
		m.mu.Unlock()
		if retry >= 0 {
			m.later(yesman, c, noticeRetries[retry])
		}
		return
	}
	m.mu.Lock()
	c.retries = 0
	m.mu.Unlock()
	if err := m.recordAttempt(y, listed, now, true, board, 0); err != nil {
		log.Printf("yesman: record continuation of %s: %v", yesman, err)
	}
}

// errOvertaken refuses to record a notice read from what changed since.
var errOvertaken = errors.New("the tasks changed while yesman read them")

// recordAttempt records a notice as sent, before it is, or as delivered.
// It forgets tasks no longer on the board, so the record stays bounded.
// Before a notice is sent, the board, workers and handoffs must still be at
// revision rev: a change counts before it is written, so one that was not
// counted yet commits after this record, and the notice told the state
// before it.
func (m *Manager) recordAttempt(y yesmen.Yesman, listed []notice, at time.Time, delivered bool, board yesmen.Board, rev uint64) error {
	return m.cfg.Yesmen.Use(y.Name, func(cur yesmen.Yesman) error {
		if !cur.HiredAt.Equal(y.HiredAt) {
			return yesmen.ErrRehired
		}
		overtaken := false
		_, err := m.cfg.Yesmen.UpdateContinuation(y.Name, y.HiredAt, func(c *yesmen.Continuation) bool {
			if !delivered && m.cfg.Yesmen.Revision(y.Name) != rev {
				overtaken = true
				return false
			}
			if delivered {
				changed := false
				for _, n := range listed {
					if r, ok := c.Tasks[n.task.ID]; ok && r.Sig == n.sig && r.At.Equal(at) && !r.Delivered {
						r.Delivered = true
						c.Tasks[n.task.ID] = r
						changed = true
					}
				}
				return changed
			}
			c.Attempts = append(recentAttempts(c.Attempts, at), at)
			for id := range c.Tasks {
				if !slices.ContainsFunc(board.Tasks, func(t yesmen.Task) bool { return t.ID == id }) {
					delete(c.Tasks, id)
				}
			}
			for _, n := range listed {
				c.Tasks[n.task.ID] = yesmen.TaskNotice{Sig: n.sig, Progress: n.progress, Count: n.count, At: at}
			}
			return true
		})
		if err == nil && overtaken {
			err = errOvertaken
		}
		return err
	})
}

// unrecordAttempt takes back an attempt recorded for a notice that was
// never written to pi: the time it took of the budget, and what it
// recorded of each task it listed, unless recorded again since.
func (m *Manager) unrecordAttempt(y yesmen.Yesman, listed []notice, at time.Time) {
	err := m.cfg.Yesmen.Use(y.Name, func(cur yesmen.Yesman) error {
		if !cur.HiredAt.Equal(y.HiredAt) {
			return yesmen.ErrRehired
		}
		_, err := m.cfg.Yesmen.UpdateContinuation(y.Name, y.HiredAt, func(c *yesmen.Continuation) bool {
			changed := false
			if i := slices.IndexFunc(c.Attempts, func(t time.Time) bool { return t.Equal(at) }); i >= 0 {
				c.Attempts = slices.Delete(c.Attempts, i, i+1)
				changed = true
			}
			for _, n := range listed {
				r, ok := c.Tasks[n.task.ID]
				if !ok || r.Sig != n.sig || !r.At.Equal(at) || r.Delivered {
					continue
				}
				if n.noticed {
					c.Tasks[n.task.ID] = n.previousNotice
				} else {
					delete(c.Tasks, n.task.ID)
				}
				changed = true
			}
			return changed
		})
		return err
	})
	if err != nil {
		// Kept as an attempt, it counts against the budget: never more.
		log.Printf("yesman: take back the continuation attempt of %s: %v", y.Name, err)
	}
}

// forgetSettled clears the delivered notice of every task the board still
// has that needs none now, and returns the record as it is then.
func (m *Manager) forgetSettled(y yesmen.Yesman, record yesmen.Continuation, items []notice) yesmen.Continuation {
	needs := map[string]bool{}
	for _, n := range items {
		needs[n.task.ID] = true
	}
	stale := false
	for id, r := range record.Tasks {
		if !needs[id] && r.Sig != "" {
			stale = true
		}
	}
	if !stale {
		return record
	}
	var out yesmen.Continuation
	err := m.cfg.Yesmen.Use(y.Name, func(cur yesmen.Yesman) error {
		if !cur.HiredAt.Equal(y.HiredAt) {
			return yesmen.ErrRehired
		}
		var err error
		out, err = m.cfg.Yesmen.UpdateContinuation(y.Name, y.HiredAt, func(c *yesmen.Continuation) bool {
			changed := false
			for id, r := range c.Tasks {
				if !needs[id] && r.Sig != "" {
					r.Sig, r.Delivered = "", false
					c.Tasks[id] = r
					changed = true
				}
			}
			return changed
		})
		return err
	})
	if err != nil {
		log.Printf("yesman: continuation of %s: %v", y.Name, err)
		return record
	}
	return out
}

// busyLocked reports whether a worker carries its work now: it starts,
// works, waits for an answer, or has a message or its report on its way.
func busyLocked(a *agent) bool {
	switch a.info.Status {
	case Starting, Working, Waiting:
		return true
	}
	return a.starting != nil || a.prompts > 0 || a.reports > 0 || a.recalls > 0
}

// carriedLocked reports whether a worker took up a task the notice lists
// as nobody's since it was read.
func (m *Manager) carriedLocked(yesman string, listed []notice) bool {
	for _, n := range listed {
		if n.verify || n.task.Owner == "" || n.task.Owner == yesmen.LeadName || n.task.Status == yesmen.TaskBlocked {
			continue
		}
		a := m.agents[ID(yesman, n.task.Owner)]
		if a != nil && !a.retiring && busyLocked(a) && (n.task.OwnerSince.IsZero() || n.task.OwnerSince.Equal(a.info.Since)) {
			return true
		}
	}
	return false
}

// reevaluate has the running evaluation of c read everything again, once it
// ends: what it read was overtaken.
func (m *Manager) reevaluate(c *continuation) {
	m.mu.Lock()
	c.again = true
	m.mu.Unlock()
}

func recentAttempts(all []time.Time, now time.Time) []time.Time {
	var out []time.Time
	for _, t := range all {
		if now.Sub(t) < noticeWindow {
			out = append(out, t)
		}
	}
	return out
}

// classify finds the open tasks that need the lead: in board order, those
// nobody carries and blocked ones whose wait is over, then legacy blocked
// tasks without a wait, to check.
func classify(board yesmen.Board, workers []yesmen.Worker, runs map[string]ownerRun, handoffs yesmen.Handoffs) []notice {
	return classifyAs(board, workers, runs, handoffs, false)
}

// classifyAs is classify, where unreadable says the Needs you requests
// could not be read: a task waiting for the user is then only listed to
// check, never taken as free to go on.
func classifyAs(board yesmen.Board, workers []yesmen.Worker, runs map[string]ownerRun, handoffs yesmen.Handoffs, unreadable bool) []notice {
	byID := map[string]yesmen.Task{}
	for _, t := range board.Tasks {
		byID[t.ID] = t
	}
	byName := map[string]yesmen.Worker{}
	for _, w := range workers {
		byName[w.Name] = w
	}
	var out, legacy []notice
	for _, t := range board.Tasks {
		progress := hashOf(t.Status, t.Owner, waitKey(t.Wait))
		switch t.Status {
		case yesmen.TaskTodo, yesmen.TaskDoing:
			why, state, ok := unattended(t, byName, runs)
			if !ok {
				continue
			}
			out = append(out, notice{task: t, why: why, progress: progress, sig: hashOf(progress, state)})
		case yesmen.TaskBlocked:
			if t.Wait == nil {
				legacy = append(legacy, notice{task: t, verify: true, why: "no wait recorded: record what it waits for", progress: progress, sig: hashOf(progress, "legacy")})
				continue
			}
			if unreadable && t.Wait.Kind == yesmen.WaitUser {
				legacy = append(legacy, notice{task: t, verify: true, why: "its Needs you requests could not be read: check them", progress: progress, sig: hashOf(progress, "unreadable")})
				continue
			}
			why, state, ok := waitOver(t, byID, handoffs)
			if !ok {
				continue
			}
			out = append(out, notice{task: t, why: why, progress: progress, sig: hashOf(progress, state)})
		}
	}
	return append(out, legacy...)
}

// unattended says why nobody carries a todo or doing task, if nobody does,
// and the durable state that says so.
func unattended(t yesmen.Task, workers map[string]yesmen.Worker, runs map[string]ownerRun) (string, string, bool) {
	if t.Owner == "" || t.Owner == yesmen.LeadName {
		return "no worker carries it", "lead", true
	}
	w, ok := workers[t.Owner]
	if !ok {
		return fmt.Sprintf("no worker named %s now", t.Owner), "missing", true
	}
	if !t.OwnerSince.IsZero() && !t.OwnerSince.Equal(w.StartedAt) {
		// A worker of the same name hired later: not the one it was given.
		return fmt.Sprintf("its owner %s was replaced by a new worker of that name, which was not given it", t.Owner),
			hashOf("replaced", t.OwnerSince.UTC().Format(time.RFC3339Nano), w.StartedAt.UTC().Format(time.RFC3339Nano)), true
	}
	if !w.PausedAt.IsZero() {
		return "", "", false // the user stopped it: theirs to resume
	}
	if r, ok := runs[t.Owner]; ok && r.active && r.since.Equal(w.StartedAt) {
		return "", "", false
	}
	gen := w.StartedAt.UTC().Format(time.RFC3339Nano)
	switch {
	case !w.FinishedAt.IsZero():
		return fmt.Sprintf("owner %s finished its run and was closed; a report does not finish the task", t.Owner),
			hashOf("finished", gen, w.FinishedAt.UTC().Format(time.RFC3339Nano)), true
	case !w.FailedAt.IsZero():
		return fmt.Sprintf("owner %s failed", t.Owner), hashOf("failed", gen, w.FailedAt.UTC().Format(time.RFC3339Nano)), true
	}
	return fmt.Sprintf("owner %s is not working on it", t.Owner), hashOf("stopped", gen), true
}

// waitOver says why a blocked task's wait is over, if it is, and the state
// that says so. A wait for the user is over only for the lead to check:
// a request closed as done is the user's word, not proof.
func waitOver(t yesmen.Task, tasks map[string]yesmen.Task, handoffs yesmen.Handoffs) (string, string, bool) {
	w := t.Wait
	switch w.Kind {
	case yesmen.WaitTasks:
		var done, gone []string
		for _, id := range w.Tasks {
			d, ok := tasks[id]
			switch {
			case !ok || d.Status == yesmen.TaskDropped:
				gone = append(gone, id)
			case d.Status == yesmen.TaskDone:
				done = append(done, id)
			}
		}
		if len(gone) > 0 {
			return fmt.Sprintf("it waits for %s, dropped or removed", strings.Join(gone, ", ")), hashOf("gone", strings.Join(gone, ",")), true
		}
		if len(done) < len(w.Tasks) {
			return "", "", false
		}
		return fmt.Sprintf("%s, which it waits for, %s done", strings.Join(done, ", "), plural(len(done), "is", "are")), "ready", true
	case yesmen.WaitUser:
		var closed []string
		for _, h := range handoffs.Items {
			if h.Task != t.ID && (w.Handoff == "" || h.ID != w.Handoff) {
				continue
			}
			if h.Open() {
				return "", "", false
			}
			closed = append(closed, fmt.Sprintf("%s %s", h.ID, h.Resolution))
		}
		if len(closed) == 0 {
			return "it waits for the user, but no Needs you request for it is open: register one, or go on", "unregistered", true
		}
		return fmt.Sprintf("it waits for the user, whose requests are closed (%s): check the result yourself; that does not unblock it", strings.Join(closed, ", ")),
			hashOf("closed", strings.Join(closed, ",")), true
	}
	return "", "", false // external or paused: yesman cannot tell when it is over
}

func waitKey(w *yesmen.Wait) string {
	if w == nil {
		return ""
	}
	return w.Kind + ":" + strings.Join(w.Tasks, ",") + ":" + w.Handoff
}

func hashOf(parts ...string) string {
	h := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(h[:8])
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// limitNotices keeps at most noticeLines tasks of each section. The rest
// are not recorded, so the next notice lists them.
func limitNotices(all []notice) []notice {
	var out []notice
	acted, verified := 0, 0
	for _, n := range all {
		if n.verify {
			if verified++; verified > noticeLines {
				continue
			}
		} else if acted++; acted > noticeLines {
			continue
		}
		out = append(out, n)
	}
	return out
}

// noticeText is the notice for the lead: short, one line per task.
func noticeText(listed []notice) string {
	var b strings.Builder
	b.WriteString(noticeHeader + ". A run that ended or a worker's report does not finish a task.\n")
	line := func(n notice) {
		fmt.Fprintf(&b, "- %s [%s] %s", n.task.ID, n.task.Status, oneLine(n.task.Title))
		if n.task.Owner != "" && n.verify {
			fmt.Fprintf(&b, " (owner %s)", n.task.Owner)
		}
		if n.why != "" {
			b.WriteString(": " + n.why)
		}
		if n.count == noticesPerState {
			b.WriteString(" (yesman will not tell you again until its status, owner or wait changes)")
		}
		b.WriteString("\n")
	}
	verify := slices.ContainsFunc(listed, func(n notice) bool { return n.verify })
	for _, n := range listed {
		if !n.verify {
			line(n)
		}
	}
	b.WriteString("\nFor each: start or message a worker for it, or mark it done or dropped, or blocked with the wait it is under (yesman_board wait). Leave what the user paused alone. yesman tells you again only of a task that changed.\n")
	if verify {
		b.WriteString("\nCheck these; yesman leaves them blocked:\n")
		for _, n := range listed {
			if n.verify {
				line(n)
			}
		}
	}
	return b.String()
}

func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > 120 {
		return string(r[:119]) + "…"
	}
	return s
}
