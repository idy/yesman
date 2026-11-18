package schedule

import (
	"errors"
	"testing"
	"time"

	"github.com/idy/yesman/internal/yesmen"
)

// R5-F4: what a window shows and asks for names the workspace generation it
// showed. Once that yesman was dismissed and one hired under its name, an
// old request is refused before anything is read, changed or sent, even for
// a plan of the new yesman with the old one's ID and revision; the new
// generation's own requests go on.
func TestOldGenerationNeverTouchesItsReplacement(t *testing.T) {
	e := newEnv(t, "", utc("2026-11-16T08:59:00Z"))
	old, err := e.store.Get(ws)
	if err != nil {
		t.Fatal(err)
	}
	g := e.svc.At(old.HiredAt)
	p, err := g.Save(ws, PlanInput{Title: "Old", Instructions: "x", Schedule: daily("09:00"), Timezone: "UTC"})
	if err != nil {
		t.Fatal(err)
	}
	if v, err := g.View(ws); err != nil || len(v.Plans) != 1 {
		t.Fatalf("the current generation's view: %+v %v", v.Plans, err)
	}
	if _, err := e.store.Archive(ws, func(y yesmen.Yesman) error {
		e.svc.RetireYesman(y.Name, y.HiredAt)
		return e.agents.StopYesman(y.Name)
	}); err != nil {
		t.Fatal(err)
	}
	e.agents.ForgetYesman(ws, old.HiredAt)
	time.Sleep(10 * time.Millisecond)
	if _, err := e.store.Hire(ws); err != nil {
		t.Fatal(err)
	}
	y, _ := e.store.Get(ws)
	// The new yesman's own plan, with the same ID and revision as the old.
	q, err := e.svc.At(y.HiredAt).Save(ws, PlanInput{Title: "NEW GENERATION PLAN", Instructions: "x", Schedule: daily("09:00"), Timezone: "UTC"})
	if err != nil || q.ID != p.ID || q.Revision != p.Revision || q.Enabled {
		t.Fatalf("the new plan: %+v %v", q, err)
	}
	r, err := e.svc.At(y.HiredAt).RunNow(ws, q.ID, e.requestID(), "")
	if err != nil {
		t.Fatal(err)
	}
	e.svc.Wait()
	prompts := len(e.prompts())
	before := e.plansBytes()

	for name, call := range map[string]func() error{
		"view":    func() error { _, err := g.View(ws); return err },
		"summary": func() error { _, err := g.Summarize(ws); return err },
		"save": func() error {
			_, err := g.Save(ws, PlanInput{ID: p.ID, Revision: p.Revision, Title: "Old edit", Instructions: "x", Schedule: daily("09:00"), Timezone: "UTC", Enabled: true})
			return err
		},
		"create": func() error {
			_, err := g.Save(ws, PlanInput{Title: "Old new", Instructions: "x", Schedule: daily("09:00"), Timezone: "UTC"})
			return err
		},
		"switch":  func() error { _, err := g.SetEnabled(ws, p.ID, true, p.Revision); return err },
		"archive": func() error { return g.Archive(ws, p.ID, p.Revision) },
		"run":     func() error { _, err := g.RunNow(ws, p.ID, e.requestID(), ""); return err },
		"retry":   func() error { _, err := g.Retry(ws, r.ID); return err },
		"check":   func() error { _, err := g.Check(ws, r.ID); return err },
	} {
		if err := call(); !errors.Is(err, ErrReplaced) {
			t.Errorf("%s by the old generation: %v", name, err)
		}
	}
	// A window that names no generation is refused too.
	if _, err := e.svc.At(time.Time{}).RunNow(ws, q.ID, e.requestID(), ""); !errors.Is(err, ErrReplaced) {
		t.Errorf("a run naming no generation: %v", err)
	}
	e.svc.Wait()
	if string(e.plansBytes()) != string(before) {
		t.Fatal("the old generation changed the new one's plans")
	}
	if n := len(e.prompts()); n != prompts {
		t.Fatalf("the old generation sent %d prompts", n-prompts)
	}
	v, err := e.svc.At(y.HiredAt).View(ws)
	if err != nil || len(v.Plans) != 1 || v.Plans[0].Title != "NEW GENERATION PLAN" || v.Plans[0].Enabled || v.Plans[0].Revision != q.Revision {
		t.Fatalf("the new generation's plans: %+v %v", v.Plans, err)
	}
	if on, err := e.svc.At(y.HiredAt).SetEnabled(ws, q.ID, true, q.Revision); err != nil || !on.Enabled {
		t.Fatalf("the new generation's own switch: %+v %v", on, err)
	}
}
