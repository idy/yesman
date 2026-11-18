package schedule

import (
	"time"

	"github.com/idy/yesman/internal/yesmen"
)

// Gen is the scheduled tasks of one generation of a workspace, as a window
// showed them: since is when that generation was hired. Whatever it reads
// or changes is refused with ErrReplaced once the workspace of that name is
// another generation, dismissed and hired again, checked with the workspace
// held, before anything is read, changed or sent. A window that showed the
// old generation therefore never changes, runs or shows the new one's
// plans, even those reusing its plan IDs and revisions.
type Gen struct {
	s     *Service
	since time.Time
}

// At returns the scheduled tasks of the workspace generation hired at
// since, which must not be zero.
func (s *Service) At(since time.Time) Gen { return Gen{s: s, since: since} }

// hiredAt checks that y, held, is the generation since; zero since
// is not checked, for callers that hold no earlier view of it.
func hiredAt(y yesmen.Yesman, since time.Time) error {
	if !since.IsZero() && !y.HiredAt.Equal(since) {
		return ErrReplaced
	}
	return nil
}

// guard is hiredAt as an Admit, for changes of the plans.
func (g Gen) guard() Admit {
	return func(y yesmen.Yesman) (func(), error) {
		if g.since.IsZero() {
			return nil, ErrReplaced
		}
		return nil, hiredAt(y, g.since)
	}
}

func (g Gen) checked() error {
	if g.since.IsZero() {
		return ErrReplaced
	}
	return nil
}

func (g Gen) View(name string) (View, error) {
	if err := g.checked(); err != nil {
		return View{}, err
	}
	return g.s.view(name, g.since)
}

func (g Gen) Summarize(name string) (Summary, error) {
	if err := g.checked(); err != nil {
		return Summary{}, err
	}
	return g.s.summarize(name, g.since)
}

func (g Gen) Save(name string, in PlanInput) (Plan, error) {
	return g.s.save(name, in, "user", g.guard())
}

func (g Gen) SetEnabled(name, id string, enabled bool, revision int) (Plan, error) {
	return g.s.setEnabled(name, g.guard(), id, enabled, revision)
}

func (g Gen) Archive(name, id string, revision int) error {
	return g.s.archive(name, g.guard(), id, revision)
}

func (g Gen) RunNow(name, id, requestID, from string) (Run, error) {
	if err := g.checked(); err != nil {
		return Run{}, err
	}
	return g.s.runNow(name, g.since, id, requestID, from)
}

func (g Gen) Retry(name, runID string) (Run, error) {
	if err := g.checked(); err != nil {
		return Run{}, err
	}
	return g.s.retry(name, g.since, runID)
}

func (g Gen) Check(name, runID string) (Run, error) {
	if err := g.checked(); err != nil {
		return Run{}, err
	}
	return g.s.check(name, g.since, runID)
}
