package main

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/idy/yesman/internal/usage"
	"github.com/idy/yesman/internal/yesmen"
)

// Settings › Model usage shows what agents actually used, read from their
// pi session logs: for each model at each effort, the agents that used it,
// the board tasks done with it, tokens and measured output speed. It reads
// the logs of the current yesmen only, again only as far as each file grew
// since the last read, without holding the agent manager's lock.

// usageIndex keeps what was read of each yesman's session logs.
type usageIndex struct {
	mu     sync.Mutex
	spaces map[string]*usageSpace // by yesman name
}

// usageSpace is what was read of one hire of a yesman: a workspace
// dismissed and hired again under its name is read anew.
type usageSpace struct {
	hiredAt  time.Time
	mu       sync.Mutex // one read of the workspace at a time
	sessions usage.Sessions
}

func (a *app) usageIndex() *usageIndex {
	a.usageOnce.Do(func() { a.usage = &usageIndex{spaces: map[string]*usageSpace{}} })
	return a.usage
}

// keep forgets the yesmen no longer hired, or hired again.
func (x *usageIndex) keep(ys []yesmen.Yesman) {
	x.mu.Lock()
	defer x.mu.Unlock()
	hired := map[string]time.Time{}
	for _, y := range ys {
		hired[y.Name] = y.HiredAt
	}
	for name, sp := range x.spaces {
		if at, ok := hired[name]; !ok || !at.Equal(sp.hiredAt) {
			delete(x.spaces, name)
		}
	}
}

func (x *usageIndex) space(y yesmen.Yesman) *usageSpace {
	x.mu.Lock()
	defer x.mu.Unlock()
	sp := x.spaces[y.Name]
	if sp == nil || !sp.hiredAt.Equal(y.HiredAt) {
		sp = &usageSpace{hiredAt: y.HiredAt}
		x.spaces[y.Name] = sp
	}
	return sp
}

// read brings a yesman's records up to date, with its workers and board.
func (x *usageIndex) read(ctx context.Context, store *yesmen.Store, y yesmen.Yesman) (usage.Workspace, error) {
	sp := x.space(y)
	sp.mu.Lock()
	defer sp.mu.Unlock()
	w := usage.Workspace{Name: y.Name, HiredAt: y.HiredAt}
	// The logs first: a workspace whose .yesman is not its own is not read.
	var err error
	if w.Agents, w.Stats, err = sp.sessions.Scan(ctx, y.Path); err != nil {
		return w, err
	}
	if w.Workers, err = store.Workers(y.Name); err != nil {
		return w, err
	}
	if w.Board, err = store.Board(y.Name); err != nil {
		return w, err
	}
	// Without the recorded starts, the current workers still tell theirs.
	if w.Starts, err = store.WorkerStarts(y.Name); err != nil {
		w.Stats.Errors = append(w.Stats.Errors, err.Error())
	}
	return w, nil
}

// ModelUsageQuery chooses what Settings › Model usage counts.
type ModelUsageQuery struct {
	// Yesman is a yesman's name, or "" for all of them.
	Yesman string `json:"yesman"`
	// Agents is "workers", "leads" or "all".
	Agents string `json:"agents"`
	// Period is "all", for all the history the logs keep, or "24h", "7d"
	// or "30d".
	Period string `json:"period"`
}

// UsageYesman names a yesman for the page's filter.
type UsageYesman struct {
	Name  string `json:"name"`
	Label string `json:"label"`
}

// ModelUsageView is what Settings › Model usage shows.
type ModelUsageView struct {
	usage.Summary
	Query ModelUsageQuery `json:"query"`
	// Since is when the period starts; zero for all history.
	Since time.Time `json:"since,omitzero"`
	// Names are pi's names of the models, by provider/model, when pi's
	// catalog has been read; Model usage does not start pi to read it.
	Names  map[string]string `json:"names"`
	Yesmen []UsageYesman     `json:"yesmen"`
	ReadAt time.Time         `json:"readAt"`
}

// usagePeriods are the periods the page offers.
var usagePeriods = map[string]time.Duration{"all": 0, "24h": 24 * time.Hour, "7d": 7 * 24 * time.Hour, "30d": 30 * 24 * time.Hour}

// ModelUsage summarizes the model usage of the current yesmen's agents. A
// yesman whose logs cannot be read is left out and named in the coverage.
func (s *service) ModelUsage(ctx context.Context, q ModelUsageQuery) (ModelUsageView, error) {
	return s.app.modelUsage(ctx, q, time.Now())
}

func (a *app) modelUsage(ctx context.Context, q ModelUsageQuery, now time.Time) (ModelUsageView, error) {
	if q.Agents == "" {
		q.Agents = "workers"
	}
	if q.Period == "" {
		q.Period = "all"
	}
	query := usage.Query{Workers: q.Agents != "leads", Leads: q.Agents != "workers"}
	if q.Agents != "workers" && q.Agents != "leads" && q.Agents != "all" {
		return ModelUsageView{}, fmt.Errorf("unknown agents %q", q.Agents)
	}
	period, ok := usagePeriods[q.Period]
	if !ok {
		return ModelUsageView{}, fmt.Errorf("unknown period %q", q.Period)
	}
	view := ModelUsageView{Query: q, Names: map[string]string{}, Yesmen: []UsageYesman{}, ReadAt: now.UTC()}
	if period > 0 {
		query.Since = now.Add(-period).UTC()
		view.Since = query.Since
	}
	ys, err := a.yesmen.List()
	if err != nil {
		return ModelUsageView{}, err
	}
	idx := a.usageIndex()
	idx.keep(ys)
	var spaces []usage.Workspace
	var failed []string
	found := q.Yesman == ""
	for _, y := range ys {
		view.Yesmen = append(view.Yesmen, UsageYesman{Name: y.Name, Label: y.Label()})
		if q.Yesman != "" && y.Name != q.Yesman {
			continue
		}
		found = true
		w, err := idx.read(ctx, a.yesmen, y)
		if ctx.Err() != nil {
			return ModelUsageView{}, ctx.Err()
		}
		if err != nil {
			failed = append(failed, y.Name+": "+err.Error())
			continue
		}
		spaces = append(spaces, w)
	}
	if !found {
		return ModelUsageView{}, fmt.Errorf("no yesman named %s", q.Yesman)
	}
	view.Summary = usage.Summarize(spaces, query)
	view.Coverage.Errors = append(failed, view.Coverage.Errors...)
	for _, m := range a.catalog.peek() {
		view.Names[m.Provider+"/"+m.ID] = m.Name
	}
	return view, nil
}
