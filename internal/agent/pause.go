package agent

import (
	"context"
	"errors"
	"fmt"
	"log"
	"slices"
	"strings"
	"time"

	"github.com/idy/yesman/internal/yesmen"
)

// What the user stopped stays stopped. Their Stop or Abort of a lead pauses
// it: from then on yesman's own messages to it (worker reports, failures,
// Needs you notices, task notices) start nothing and are kept, until the
// user writes to it.
//
// The pause is set in memory, under mu, first: it holds at once, and a
// message is admitted, under mu again, as it is written to pi (see admit),
// so a pause either comes first and the message waits, or comes after and
// the Stop or Abort that follows ends what began. Then the process is
// stopped, and only then is the pause written to the workspace, so that
// slow storage never delays the Stop.
//
// Every setting by the user is numbered (epoch). A write commits only the
// current setting, deciding so under the yesman's continuation lock, the
// one order all writes of the record take: an older setting written late
// never undoes a newer one. A write that fails leaves the setting in
// memory, tried again later, and is reported, never taken as saved; until
// it lands, a restart reads the setting written before.

// errLeadPaused refuses yesman's own message to a lead the user stopped.
var errLeadPaused = errors.New("the user stopped the lead: it hears of this when they write to it")

// deferredLimit bounds the messages kept for a paused lead.
const deferredLimit = 64

// pauseUnsaved starts the Detail shown on a lead whose Stop could not be
// written to its workspace.
const pauseUnsaved = "Stopped, but this could not be saved: yesman keeps it until it quits"

// leadPause is a yesman's lead pause as this Manager knows it; guarded by mu.
type leadPause struct {
	hiredAt time.Time
	// known: read from the workspace or set by the user here. A record
	// that cannot be read leaves it unknown, which counts as paused.
	known, paused bool
	at            time.Time
	// epoch numbers the user's settings here; only the current one is
	// written.
	epoch uint64
	// set by the user here, and not yet written (persisted false).
	local, persisted bool
	deferred         []func()
}

// pauseOf returns the pause kept here of hire y, or nil; under mu. One is
// kept per workspace, of the hire that owns it (see ownPause): a call about
// another hire, as a late callback of an archived workspace makes, finds
// nothing, and changes nothing. Hire times identify a hire but are not
// ordered: a clock set back gives a later hire an earlier time.
func (m *Manager) pauseOf(y yesmen.Yesman) *leadPause {
	if lp := m.leadPauses[y.Name]; lp != nil && lp.hiredAt.Equal(y.HiredAt) {
		return lp
	}
	return nil
}

// ownPause makes sure the pause kept of y's workspace is hire y's, and
// reports whether it is. Another hire's record is replaced only once y is
// known to be the hire the workspace has now: at once, without I/O, when
// this Manager runs y's lead (see liveHireLocked), else once the workspace
// confirms it under its lifecycle lease (which Archive waits for). A stale
// y is refused and changes nothing.
func (m *Manager) ownPause(y yesmen.Yesman) bool {
	m.mu.Lock()
	if m.ownPauseLocked(y) {
		m.mu.Unlock()
		return true
	}
	m.mu.Unlock()
	err := m.cfg.Yesmen.Use(y.Name, func(cur yesmen.Yesman) error {
		if !cur.HiredAt.Equal(y.HiredAt) {
			return yesmen.ErrRehired
		}
		m.mu.Lock()
		if lp := m.leadPauses[y.Name]; lp == nil || !lp.hiredAt.Equal(y.HiredAt) {
			m.leadPauses[y.Name] = &leadPause{hiredAt: y.HiredAt}
		}
		m.mu.Unlock()
		return nil
	})
	return err == nil
}

// ownPauseLocked is ownPause without I/O, under mu: it reports false when
// only the workspace can say whether y is its hire.
func (m *Manager) ownPauseLocked(y yesmen.Yesman) bool {
	if m.leadPauses == nil {
		m.leadPauses = map[string]*leadPause{}
	}
	lp := m.leadPauses[y.Name]
	if lp == nil || !lp.hiredAt.Equal(y.HiredAt) && m.liveHireLocked(y) {
		m.leadPauses[y.Name] = &leadPause{hiredAt: y.HiredAt}
		return true
	}
	return lp.hiredAt.Equal(y.HiredAt)
}

// liveHireLocked reports, under mu, whether this Manager runs the lead of
// hire y: an agent started for that hire under its lifecycle lease and not
// retiring. StopYesman marks it retiring, under mu, before Archive moves the
// hire away, and another hire exists only after that: so it proves y is the
// hire the workspace has now, without waiting for storage. Hire times are
// compared for identity only.
func (m *Manager) liveHireLocked(y yesmen.Yesman) bool {
	a := m.agents[ID(y.Name, yesmen.LeadName)]
	return a != nil && !a.retiring && !a.workspaceSince.IsZero() && a.workspaceSince.Equal(y.HiredAt)
}

// leadPausedLocked reports whether yesman's own messages may not reach the
// lead of this hire now, under mu. Unknown counts as paused.
func (m *Manager) leadPausedLocked(y yesmen.Yesman) bool {
	lp := m.pauseOf(y)
	return lp == nil || !lp.known || lp.paused
}

// loadPause makes sure the pause of y's lead is known, reading it from the
// workspace, and writes a setting made here that was not written yet. It
// does its I/O outside mu, and reports whether the lead is paused.
func (m *Manager) loadPause(y yesmen.Yesman) bool {
	if !m.ownPause(y) {
		return true // not the workspace's hire any more: nothing to start
	}
	m.mu.Lock()
	var known, unsaved, paused bool
	var epoch uint64
	var at time.Time
	if lp := m.pauseOf(y); lp != nil {
		known, unsaved, epoch, paused, at = lp.known, lp.local && !lp.persisted, lp.epoch, lp.paused, lp.at
	}
	m.mu.Unlock()
	if unsaved {
		_ = m.persistPause(y, epoch, paused, at)
	}
	if known {
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.leadPausedLocked(y)
	}
	c, err := m.cfg.Yesmen.Continuation(y.Name)
	m.mu.Lock()
	defer m.mu.Unlock()
	if lp := m.pauseOf(y); lp != nil && lp.known {
		return lp.paused // the user acted meanwhile, which wins
	}
	if err != nil {
		log.Printf("yesman: read the pause of %s's lead: %v", y.Name, err)
		return true // unknown: paused, and read again next time
	}
	if !c.HiredAt.Equal(y.HiredAt) {
		return true // the workspace was hired again: not this hire's record
	}
	lp := m.pauseOf(y)
	if lp == nil {
		return true // owned by another hire meanwhile
	}
	lp.known, lp.paused, lp.at, lp.persisted = true, !c.LeadPausedAt.IsZero(), c.LeadPausedAt, true
	return lp.paused
}

// persistPause writes setting epoch of the pause, paused since at or not,
// if it is still the current one when the record is written. It reports a
// failure to write the current setting.
func (m *Manager) persistPause(y yesmen.Yesman, epoch uint64, paused bool, at time.Time) error {
	if m.reviewBeforePersistPause != nil {
		m.reviewBeforePersistPause(paused) // tests: the write is slow to begin
	}
	stale := false
	err := m.cfg.Yesmen.Use(y.Name, func(cur yesmen.Yesman) error {
		if !cur.HiredAt.Equal(y.HiredAt) {
			return yesmen.ErrRehired
		}
		_, err := m.cfg.Yesmen.UpdateContinuation(y.Name, y.HiredAt, func(c *yesmen.Continuation) bool {
			// Under the record's lock: a newer setting writes after this.
			m.mu.Lock()
			stale = !m.currentSettingLocked(y, epoch)
			m.mu.Unlock()
			if stale {
				return false
			}
			want := time.Time{}
			if paused {
				want = at
			}
			if c.LeadPausedAt.Equal(want) {
				return false
			}
			c.LeadPausedAt = want
			return true
		})
		return err
	})
	m.mu.Lock()
	if stale || !m.currentSettingLocked(y, epoch) {
		m.mu.Unlock()
		return nil // set again meanwhile, or hired again: not this setting's to write
	}
	lp := m.pauseOf(y)
	if err != nil {
		m.mu.Unlock()
		log.Printf("yesman: record the pause of %s's lead (kept in memory, tried again): %v", y.Name, err)
		return err
	}
	lp.persisted = true
	lead := m.agents[ID(y.Name, yesmen.LeadName)]
	cleared := lead != nil && strings.HasPrefix(lead.info.Detail, pauseUnsaved)
	if cleared {
		lead.info.Detail, lead.info.UpdatedAt = "", time.Now()
	}
	m.mu.Unlock()
	if cleared {
		m.changed()
	}
	return nil
}

// currentSettingLocked reports, under mu, whether setting epoch of the
// pause of hire y is still the current one. It never makes a hire's pause.
func (m *Manager) currentSettingLocked(y yesmen.Yesman, epoch uint64) bool {
	lp := m.pauseOf(y)
	return lp != nil && lp.epoch == epoch
}

// setPause sets the user's pause of y's lead in memory, and returns what
// was kept for the lead when it lifts the pause, and the write of the
// setting, which the caller does once the process is dealt with. y is the
// hire the caller just read from the workspace; a hire the workspace no
// longer has sets nothing.
func (m *Manager) setPause(y yesmen.Yesman, paused bool) ([]func(), func() error) {
	if !m.ownPause(y) {
		return nil, func() error { return nil }
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.setPauseLocked(y, paused)
}

// setPauseLocked is setPause once the pause kept is hire y's, under mu.
func (m *Manager) setPauseLocked(y yesmen.Yesman, paused bool) ([]func(), func() error) {
	lp := m.pauseOf(y)
	if lp == nil {
		return nil, func() error { return nil } // owned by another hire meanwhile
	}
	if lp.known && !lp.paused && !paused && (!lp.local || lp.persisted) {
		epoch := lp.epoch
		// Not paused here: write only if the workspace says otherwise.
		return nil, func() error {
			if m.loadPauseRecord(y) {
				return m.persistPause(y, epoch, false, time.Time{})
			}
			return nil
		}
	}
	stamp := time.Time{}
	if paused {
		stamp = time.Now().UTC()
	}
	lp.epoch++
	epoch := lp.epoch
	lp.known, lp.paused, lp.local, lp.persisted, lp.at = true, paused, true, false, stamp
	var deferred []func()
	if !paused {
		deferred, lp.deferred = lp.deferred, nil
	}
	return deferred, func() error { return m.persistPause(y, epoch, paused, stamp) }
}

// setLeadPause sets the user's pause of lead id, of yesman yesman, as
// setPause does. The hire is the running lead's when this Manager runs one
// (see liveHireLocked): then the setting holds at once, in the same
// section of mu that finds the hire, and no storage is read before it, so
// a workspace that cannot be read loses nothing the user did; only writing
// it can fail, which is kept and reported. With no lead running here the
// workspace names the hire; if it cannot be read, nothing is set, and the
// write reports why.
func (m *Manager) setLeadPause(id, yesman string, paused bool) ([]func(), func() error) {
	m.mu.Lock()
	if a := m.agents[id]; a != nil && !a.retiring && !a.workspaceSince.IsZero() {
		y := yesmen.Yesman{Name: yesman, HiredAt: a.workspaceSince}
		defer m.mu.Unlock()
		if !m.ownPauseLocked(y) {
			return nil, func() error { return nil } // cannot be: y is the live hire
		}
		return m.setPauseLocked(y, paused)
	}
	m.mu.Unlock()
	y, err := m.cfg.Yesmen.Get(yesman)
	if err != nil {
		log.Printf("yesman: find the hire of %s to record the user's setting of its lead: %v", yesman, err)
		return nil, func() error { return err }
	}
	return m.setPause(y, paused)
}

// loadPauseRecord reports whether the workspace records a pause.
func (m *Manager) loadPauseRecord(y yesmen.Yesman) bool {
	c, err := m.cfg.Yesmen.Continuation(y.Name)
	return err != nil || !c.LeadPausedAt.IsZero()
}

// deferForLead keeps a message of yesman's for the lead of hire since, to
// be sent once the user lifts its pause. It reports whether it was kept:
// a pause lifted meanwhile sends it now, which the caller does.
func (m *Manager) deferForLead(yesman string, since time.Time, send func()) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	lp := m.pauseOf(yesmen.Yesman{Name: yesman, HiredAt: since})
	if lp == nil || lp.known && !lp.paused {
		return false
	}
	if len(lp.deferred) >= deferredLimit {
		log.Printf("yesman: %s's lead is paused with %d messages kept: the oldest is dropped", yesman, deferredLimit)
		lp.deferred = slices.Delete(lp.deferred, 0, 1)
	}
	lp.deferred = append(lp.deferred, send)
	return true
}

// automatic reports whether a message to agent id is one of yesman's own to
// a lead, which the user's pause holds.
func automatic(id string, origin Origin) bool {
	_, name, err := SplitID(id)
	return err == nil && name == yesmen.LeadName && origin.Source == OriginYesman
}

// pauseByUser records that the user stopped an agent or its run, so that
// yesman does not start it again by itself, across restarts too. The pause
// holds in memory at once, without waiting for storage; the returned write,
// which the caller does after stopping the agent, saves it.
func (m *Manager) pauseByUser(id string) func() error {
	none := func() error { return nil }
	yesman, name, err := SplitID(id)
	if err != nil {
		return none
	}
	if name != yesmen.LeadName {
		return m.pauseWorker(id, yesman, name)
	}
	_, persist := m.setLeadPause(id, yesman, true)
	return persist
}

// workerPause is the user's setting of the pause of one worker incarnation
// (since) of one hire of a workspace, as this Manager knows it; guarded by
// mu. A Stop pauses it; being given work lifts the pause. Either is set
// here first and written to the workspace (Worker.PausedAt) after: a
// setting not written yet is kept, holds here, and is tried again. Every
// setting is numbered (epoch, from m.pauseEpoch), and a write commits only
// the current one, deciding so under the lock workers.json is written
// under: an older setting written late never undoes a newer one.
type workerPause struct {
	hiredAt, since, at time.Time
	paused             bool
	epoch              uint64
	persisted          bool
	failure            string // why a lifted pause was not written yet
}

// resumeUnsaved starts the Detail shown on a worker given work whose pause
// could not be cleared in its workspace.
const resumeUnsaved = "Given work, but its Stop could not be cleared in the workspace: yesman tries again"

// workerResume names the setting that lifted a worker's pause when it was
// given work.
type workerResume struct {
	id             string
	hiredAt, since time.Time
	epoch          uint64
}

// setWorkerPauseLocked makes the current setting of worker id's pause and
// returns its epoch; under mu.
func (m *Manager) setWorkerPauseLocked(id string, hiredAt, since, at time.Time, paused bool) uint64 {
	if m.workerPauses == nil {
		m.workerPauses = map[string]*workerPause{}
	}
	m.pauseEpoch++
	m.workerPauses[id] = &workerPause{hiredAt: hiredAt, since: since, at: at, paused: paused, epoch: m.pauseEpoch}
	return m.pauseEpoch
}

// workerSettingCurrent reports whether setting epoch of the pause of worker
// id, of hire hiredAt and started at since, is still the current one.
func (m *Manager) workerSettingCurrent(id string, hiredAt, since time.Time, epoch uint64) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	wp := m.workerPauses[id]
	return wp != nil && wp.hiredAt.Equal(hiredAt) && wp.since.Equal(since) && wp.epoch == epoch
}

// resumeWorkerLocked lifts the user's pause of worker a, which is given
// work, and returns the setting for the write that records it (see
// reopenWorkerInScope). Under mu.
func (m *Manager) resumeWorkerLocked(a *agent) workerResume {
	r := workerResume{id: a.info.ID, hiredAt: a.workspaceSince, since: a.info.Since}
	r.epoch = m.setWorkerPauseLocked(r.id, r.hiredAt, r.since, time.Time{}, false)
	return r
}

// resumedWorker settles the setting r after its write, which err says
// failed or not. Written, nothing needs it any more; not written, it is
// kept, shown and tried again (see persistWorkerPauses).
func (m *Manager) resumedWorker(r workerResume, err error) {
	if errors.Is(err, errStopped) || errors.Is(err, yesmen.ErrRehired) {
		err = nil // not this hire's or this agent's to write any more
	}
	m.mu.Lock()
	wp := m.workerPauses[r.id]
	if wp == nil || !wp.hiredAt.Equal(r.hiredAt) || !wp.since.Equal(r.since) || wp.epoch != r.epoch {
		m.mu.Unlock()
		return // set again meanwhile
	}
	shown := wp.failure != ""
	if err == nil {
		delete(m.workerPauses, r.id)
	} else {
		wp.failure = err.Error()
		shown = true
	}
	m.mu.Unlock()
	if shown {
		m.changed()
	}
}

// pauseWorker sets the user's pause of worker id in memory and returns its
// write. The incarnation is the running agent's, read without I/O; only a
// worker that is not loaded here is looked up in the workspace.
func (m *Manager) pauseWorker(id, yesman, name string) func() error {
	at := time.Now().UTC()
	m.mu.Lock()
	a := m.agents[id]
	var hiredAt, since time.Time
	if a != nil && !a.retiring && !a.workspaceSince.IsZero() && !a.info.Since.IsZero() {
		hiredAt, since = a.workspaceSince, a.info.Since
	}
	m.mu.Unlock()
	if hiredAt.IsZero() {
		y, err := m.cfg.Yesmen.Get(yesman)
		if err != nil {
			return func() error { return err }
		}
		ws, err := m.cfg.Yesmen.Workers(yesman)
		if err != nil {
			log.Printf("yesman: record that the user stopped %s: %v", id, err)
			return func() error { return err }
		}
		i := slices.IndexFunc(ws, func(w yesmen.Worker) bool { return w.Name == name })
		if i < 0 {
			return func() error { return nil }
		}
		hiredAt, since = y.HiredAt, ws[i].StartedAt
	}
	m.mu.Lock()
	epoch := m.setWorkerPauseLocked(id, hiredAt, since, at, true)
	m.mu.Unlock()
	return func() error { return m.persistWorkerPause(id, yesman, name, hiredAt, since, at, epoch) }
}

// persistWorkerPause writes setting epoch of a worker's pause, if it is
// still the current one when workers.json is written: it is checked under
// the lock giving the worker work writes under too, so a pause the worker
// was given work after is never written. It reports a failure to write it.
func (m *Manager) persistWorkerPause(id, yesman, name string, hiredAt, since, at time.Time, epoch uint64) error {
	current := func() bool { return m.workerSettingCurrent(id, hiredAt, since, epoch) }
	err := m.cfg.Yesmen.Use(yesman, func(y yesmen.Yesman) error {
		if !y.HiredAt.Equal(hiredAt) {
			return yesmen.ErrRehired
		}
		return m.cfg.Yesmen.PauseWorkerIf(yesman, name, since, at, current)
	})
	if errors.Is(err, yesmen.ErrRehired) {
		err = nil // not this hire's to write any more
	}
	m.mu.Lock()
	wp := m.workerPauses[id]
	if wp == nil || !wp.hiredAt.Equal(hiredAt) || !wp.since.Equal(since) || wp.epoch != epoch {
		m.mu.Unlock()
		return nil // given work or paused again meanwhile
	}
	if err != nil {
		m.mu.Unlock()
		log.Printf("yesman: record that the user stopped %s (kept in memory, tried again): %v", id, err)
		return err
	}
	wp.persisted = true
	a := m.agents[id]
	cleared := a != nil && strings.HasPrefix(a.info.Detail, pauseUnsaved)
	if cleared {
		a.info.Detail, a.info.UpdatedAt = "", time.Now()
	}
	m.mu.Unlock()
	if cleared {
		m.changed()
	}
	return nil
}

// persistWorkerResume writes setting r, which lifted a worker's pause, if
// it is still the current one when workers.json is written.
func (m *Manager) persistWorkerResume(r workerResume, yesman, name string) {
	current := func() bool { return m.workerSettingCurrent(r.id, r.hiredAt, r.since, r.epoch) }
	err := m.cfg.Yesmen.Use(yesman, func(y yesmen.Yesman) error {
		if !y.HiredAt.Equal(r.hiredAt) {
			return yesmen.ErrRehired
		}
		return m.cfg.Yesmen.ResumeWorkerIf(yesman, name, r.since, current)
	})
	if err != nil && !errors.Is(err, yesmen.ErrRehired) {
		log.Printf("yesman: record that %s was given work (kept in memory, tried again): %v", r.id, err)
	}
	m.resumedWorker(r, err)
}

// persistWorkerPauses writes the settings of the pauses of a hire's workers
// that were not written yet; continuation calls it before it reads the
// workers. Another hire's are left to it (see ForgetYesman).
func (m *Manager) persistWorkerPauses(y yesmen.Yesman) {
	type pending struct {
		name   string
		r      workerResume
		at     time.Time
		paused bool
	}
	var todo []pending
	m.mu.Lock()
	for id, wp := range m.workerPauses {
		yesman, name, err := SplitID(id)
		if err != nil || yesman != y.Name || !wp.hiredAt.Equal(y.HiredAt) || wp.persisted {
			continue
		}
		todo = append(todo, pending{name, workerResume{id, wp.hiredAt, wp.since, wp.epoch}, wp.at, wp.paused})
	}
	m.mu.Unlock()
	for _, p := range todo {
		if p.paused {
			_ = m.persistWorkerPause(p.r.id, y.Name, p.name, p.r.hiredAt, p.r.since, p.at, p.r.epoch)
		} else {
			m.persistWorkerResume(p.r, y.Name, p.name)
		}
	}
}

// unsavedPause shows on an agent that its user's Stop was not saved.
func (m *Manager) unsavedPause(id string, err error) {
	m.mu.Lock()
	a := m.agents[id]
	if a != nil {
		a.info.Detail, a.info.UpdatedAt = pauseUnsaved+" ("+err.Error()+")", time.Now()
	}
	m.mu.Unlock()
	if a != nil {
		m.changed()
	}
}

// resumeByUser lifts the user's pause of a lead when they write to it. It
// returns a function that sends what was kept for the lead, which the
// caller runs once the user's own message is on its way.
func (m *Manager) resumeByUser(id string) func() {
	yesman, name, err := SplitID(id)
	if err != nil || name != yesmen.LeadName {
		return func() {}
	}
	deferred, persist := m.setLeadPause(id, yesman, false)
	// A failure is logged and tried again; until it lands, a restart still
	// reads the lead as paused, which waits for the user.
	_ = persist()
	return func() {
		for _, send := range deferred {
			go send()
		}
		if len(deferred) > 0 {
			go m.continueTasks(yesman)
		}
	}
}

// StopByUser is Stop for the user: the agent stays stopped, across
// restarts, until it is given work again.
func (m *Manager) StopByUser(id string) {
	persist := m.pauseByUser(id)
	m.Stop(id)
	if err := persist(); err != nil {
		m.unsavedPause(id, err)
	}
}

// AbortByUser is Abort for the user, who stopped the run: yesman does not
// start the agent again by itself. A pause that could not be saved is
// reported with the Abort.
func (m *Manager) AbortByUser(ctx context.Context, id string) error {
	persist := m.pauseByUser(id)
	err := m.Abort(ctx, id)
	if perr := persist(); perr != nil {
		m.unsavedPause(id, perr)
		err = errors.Join(err, fmt.Errorf("the stop could not be saved, and holds only until yesman quits: %w", perr))
	}
	return err
}
