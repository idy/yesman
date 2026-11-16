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

// pauseOf returns the pause of a hire of a yesman, under mu.
func (m *Manager) pauseOf(y yesmen.Yesman) *leadPause {
	if m.leadPauses == nil {
		m.leadPauses = map[string]*leadPause{}
	}
	lp := m.leadPauses[y.Name]
	if lp == nil || !lp.hiredAt.Equal(y.HiredAt) {
		lp = &leadPause{hiredAt: y.HiredAt}
		m.leadPauses[y.Name] = lp
	}
	return lp
}

// leadPausedLocked reports whether yesman's own messages may not reach the
// lead of this hire now, under mu. Unknown counts as paused.
func (m *Manager) leadPausedLocked(y yesmen.Yesman) bool {
	lp := m.pauseOf(y)
	return !lp.known || lp.paused
}

// loadPause makes sure the pause of y's lead is known, reading it from the
// workspace, and writes a setting made here that was not written yet. It
// does its I/O outside mu, and reports whether the lead is paused.
func (m *Manager) loadPause(y yesmen.Yesman) bool {
	m.mu.Lock()
	lp := m.pauseOf(y)
	known, unsaved, epoch, paused, at := lp.known, lp.local && !lp.persisted, lp.epoch, lp.paused, lp.at
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
	lp = m.pauseOf(y)
	if lp.known {
		return lp.paused // the user acted meanwhile, which wins
	}
	if err != nil {
		log.Printf("yesman: read the pause of %s's lead: %v", y.Name, err)
		return true // unknown: paused, and read again next time
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
	lp := m.leadPauses[y.Name]
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
	lp := m.leadPauses[y.Name]
	return lp != nil && lp.hiredAt.Equal(y.HiredAt) && lp.epoch == epoch
}

// setPause sets the user's pause of y's lead in memory, and returns what
// was kept for the lead when it lifts the pause, and the write of the
// setting, which the caller does once the process is dealt with.
func (m *Manager) setPause(y yesmen.Yesman, paused bool) ([]func(), func() error) {
	at := time.Now().UTC()
	m.mu.Lock()
	lp := m.pauseOf(y)
	if lp.known && !lp.paused && !paused && (!lp.local || lp.persisted) {
		epoch := lp.epoch
		m.mu.Unlock()
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
		stamp = at
	}
	lp.epoch++
	epoch := lp.epoch
	lp.known, lp.paused, lp.local, lp.persisted, lp.at = true, paused, true, false, stamp
	var deferred []func()
	if !paused {
		deferred, lp.deferred = lp.deferred, nil
	}
	m.mu.Unlock()
	return deferred, func() error { return m.persistPause(y, epoch, paused, stamp) }
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
	lp := m.leadPauses[yesman]
	if lp == nil || !lp.hiredAt.Equal(since) || lp.known && !lp.paused {
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
// yesman does not start it again by itself, across restarts too. A lead's
// pause holds in memory at once; the returned write, which the caller does
// after stopping it, saves it. A worker's is written before.
func (m *Manager) pauseByUser(id string) func() error {
	none := func() error { return nil }
	yesman, name, err := SplitID(id)
	if err != nil {
		return none
	}
	y, err := m.cfg.Yesmen.Get(yesman)
	if err != nil {
		return none
	}
	if name == yesmen.LeadName {
		_, persist := m.setPause(y, true)
		return persist
	}
	now := time.Now().UTC()
	err = m.cfg.Yesmen.Use(yesman, func(y yesmen.Yesman) error {
		ws, err := m.cfg.Yesmen.Workers(yesman)
		if err != nil {
			return err
		}
		i := slices.IndexFunc(ws, func(w yesmen.Worker) bool { return w.Name == name })
		if i < 0 {
			return nil
		}
		return m.cfg.Yesmen.PauseWorker(yesman, name, ws[i].StartedAt, now)
	})
	if err != nil {
		log.Printf("yesman: record that the user stopped %s: %v", id, err)
	}
	return none
}

// unsavedPause shows on a lead that its user's Stop was not saved.
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
	y, err := m.cfg.Yesmen.Get(yesman)
	if err != nil {
		return func() {}
	}
	deferred, persist := m.setPause(y, false)
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
