package agent

import (
	"context"
	"errors"
	"log"
	"slices"
	"time"

	"github.com/idy/yesman/internal/yesmen"
)

// What the user stopped stays stopped. Their Stop or Abort of a lead pauses
// it: from then on yesman's own messages to it (worker reports, failures,
// Needs you notices, task notices) start nothing and are kept, until the
// user writes to it. The pause is set in memory, under mu, before it is
// written to the workspace, so that it holds at once and even when writing
// it fails; the write is tried again until it lands. A start or a message
// is admitted under mu too, so a pause either comes first, and the message
// waits, or comes after, and the Stop that follows it ends what began. No
// lock is held while a message is sent.

// errLeadPaused refuses yesman's own message to a lead the user stopped.
var errLeadPaused = errors.New("the user stopped the lead: it hears of this when they write to it")

// deferredLimit bounds the messages kept for a paused lead.
const deferredLimit = 64

// leadPause is a yesman's lead pause as this Manager knows it; guarded by mu.
type leadPause struct {
	hiredAt time.Time
	// known: read from the workspace or set by the user here. A record
	// that cannot be read leaves it unknown, which counts as paused.
	known, paused bool
	at            time.Time
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
// workspace, and writes a pause set here that was not written yet. It does
// its I/O outside mu, and reports whether the lead is paused.
func (m *Manager) loadPause(y yesmen.Yesman) bool {
	m.mu.Lock()
	lp := m.pauseOf(y)
	known, local, persisted, paused, at := lp.known, lp.local, lp.persisted, lp.paused, lp.at
	m.mu.Unlock()
	if local && !persisted {
		m.persistPause(y, paused, at)
	}
	if known {
		return paused
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
	lp.known, lp.paused, lp.at = true, !c.LeadPausedAt.IsZero(), c.LeadPausedAt
	return lp.paused
}

// persistPause writes the pause as set here, unless it changed meanwhile.
func (m *Manager) persistPause(y yesmen.Yesman, paused bool, at time.Time) {
	err := m.cfg.Yesmen.Use(y.Name, func(cur yesmen.Yesman) error {
		if !cur.HiredAt.Equal(y.HiredAt) {
			return yesmen.ErrRehired
		}
		_, err := m.cfg.Yesmen.UpdateContinuation(y.Name, y.HiredAt, func(c *yesmen.Continuation) bool {
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
	defer m.mu.Unlock()
	lp := m.pauseOf(y)
	if lp.paused != paused || !lp.at.Equal(at) {
		return // set again meanwhile: that setting writes itself
	}
	if err != nil {
		log.Printf("yesman: record the pause of %s's lead (kept in memory, tried again): %v", y.Name, err)
		return
	}
	lp.persisted = true
}

// setPause sets the user's pause of y's lead, in memory first, then in the
// workspace. It returns what was kept for the lead when it lifts the pause.
func (m *Manager) setPause(y yesmen.Yesman, paused bool) []func() {
	at := time.Now().UTC()
	m.mu.Lock()
	lp := m.pauseOf(y)
	if lp.known && !lp.paused && !paused && (!lp.local || lp.persisted) {
		m.mu.Unlock()
		// Not paused here: write only if the workspace says otherwise.
		if m.loadPauseRecord(y) {
			m.persistPause(y, false, time.Time{})
		}
		return nil
	}
	stamp := time.Time{}
	if paused {
		stamp = at
	}
	lp.known, lp.paused, lp.local, lp.persisted, lp.at = true, paused, true, false, stamp
	var deferred []func()
	if !paused {
		deferred, lp.deferred = lp.deferred, nil
	}
	m.mu.Unlock()
	m.persistPause(y, paused, stamp)
	return deferred
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
// yesman does not start it again by itself, across restarts too.
func (m *Manager) pauseByUser(id string) {
	yesman, name, err := SplitID(id)
	if err != nil {
		return
	}
	y, err := m.cfg.Yesmen.Get(yesman)
	if err != nil {
		return
	}
	if name == yesmen.LeadName {
		m.setPause(y, true)
		return
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
	deferred := m.setPause(y, false)
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
	m.pauseByUser(id)
	m.Stop(id)
}

// AbortByUser is Abort for the user, who stopped the run: yesman does not
// start the agent again by itself.
func (m *Manager) AbortByUser(ctx context.Context, id string) error {
	m.pauseByUser(id)
	return m.Abort(ctx, id)
}
