package main

import (
	"context"
	"errors"
	"log"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/idy/yesman/internal/agent"
	"github.com/idy/yesman/internal/crew"
)

// The crew are the models workers run as, which the user lists in
// Settings › yesman: options, each one of pi's models at one effort. A new
// yesman's lead runs as the option chosen there; the lead runs each worker
// as an option, weighing their scores, or as its own model. The scores are
// Artificial Analysis's, found by the model's name, else the user's own.

// catalog keeps pi's catalog of models, which pi reads from its
// configuration and extensions when it starts: yesman starts pi on its own
// to read it once it is first needed, and again when asked.
type catalog struct {
	read func(context.Context) ([]agent.Model, error)

	mu      sync.Mutex
	models  []crew.Model
	at      time.Time     // when models were read; zero until a read succeeds
	err     error         // why the last read failed
	loading chan struct{} // closed when the read under way ends
}

// catalogTimeout bounds one read: pi starting and loading its extensions.
const catalogTimeout = time.Minute

func newCatalog(read func(context.Context) ([]agent.Model, error)) *catalog {
	return &catalog{read: read}
}

// get returns the catalog, reading it when it has not been read or when
// refresh asks. A read goes on when ctx ends, for the next get. The models
// are nil until a read succeeds.
func (c *catalog) get(ctx context.Context, refresh bool) ([]crew.Model, time.Time, error) {
	c.mu.Lock()
	if !refresh && !c.at.IsZero() {
		defer c.mu.Unlock()
		return c.models, c.at, nil
	}
	if c.loading == nil {
		c.loading = make(chan struct{})
		go c.load(c.loading)
	}
	wait := c.loading
	c.mu.Unlock()
	select {
	case <-wait:
	case <-ctx.Done():
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.models, c.at, ctx.Err()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.models, c.at, c.err
}

// peek returns the models last read, without reading them: nil until a
// read succeeds.
func (c *catalog) peek() []crew.Model {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.models
}

func (c *catalog) load(done chan struct{}) {
	ctx, cancel := context.WithTimeout(context.Background(), catalogTimeout)
	defer cancel()
	found, err := c.read(ctx)
	c.mu.Lock()
	defer c.mu.Unlock()
	if err == nil {
		c.models, c.at = crewModels(found), time.Now()
	} else {
		log.Print("pi's models: ", err)
	}
	c.err = err
	c.loading = nil
	close(done)
}

// crewModels are pi's models as the crew sees them.
func crewModels(models []agent.Model) []crew.Model {
	out := make([]crew.Model, 0, len(models))
	for _, m := range models {
		out = append(out, crew.Model{Provider: m.Provider, ID: m.ID, Name: m.Name, Levels: m.Levels})
	}
	return out
}

// crewConfig is a copy of the crew settings.
func (a *app) crewConfig() crew.Config {
	a.setmu.Lock()
	defer a.setmu.Unlock()
	return a.settings.Crew.Clone()
}

// crew is the crew, with what pi's catalog says of it.
func (a *app) crew(ctx context.Context) ([]crew.Member, error) {
	models, _, err := a.catalog.get(ctx, false)
	patch, _ := a.effectiveScorePatch()
	return crew.Resolve(a.crewConfig(), models, crew.Embedded(), patch), err
}

// workerRouting reads the current settings and score patch. The lead sees the
// Settings catalog. At dispatch, live is the newly started worker's catalog.
func (a *app) workerRouting(ctx context.Context, live []agent.Model) crew.Routing {
	var models []crew.Model
	if live == nil {
		ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		// Resolve keeps the saved tuples and their scores when a cold probe
		// has not succeeded. A successful empty catalog verifies absence.
		models, _, _ = a.catalog.get(ctx, false)
	} else {
		models = crewModels(live)
	}
	c := a.crewConfig()
	patch, _ := a.effectiveScorePatch()
	return crew.Routing{Pools: c.Pools, Members: crew.Resolve(c, models, crew.Embedded(), patch), CatalogUnknown: models == nil}
}

// yesmanModel is the option a new yesman's lead runs as.
func (a *app) yesmanModel(ctx context.Context) (string, error) {
	key := a.crewConfig().Yesman
	members, err := a.crew(ctx)
	if crew.Usable(members, key) {
		return key, nil
	}
	return "", errors.Join(errors.New(tr("Choose the model new yesmen run as in Settings › yesman.")), err)
}

// CrewView is what Settings › yesman shows.
type CrewView struct {
	Patch ScorePatchView `json:"patch"`
	Pools []PoolView     `json:"pools"`
	// Date is the day of the Artificial Analysis scores, and Source where
	// they come from.
	Date   string `json:"date"`
	Source string `json:"source"`
	// Top is the highest intelligence Artificial Analysis scored, for the
	// user to place their own estimates on its scale.
	Top float64 `json:"top"`
	// Models are pi's models, to add options of.
	Models []CrewModel `json:"models"`
	// Members are the crew's options, in the user's order.
	Members []crew.Member `json:"members"`
	// Yesman is the key of the option new yesmen run as, "" for none.
	Yesman string `json:"yesman"`
	// ReadAt is when pi's catalog was read, and Error why it could not be.
	ReadAt time.Time `json:"readAt,omitzero"`
	Error  string    `json:"error,omitempty"`
	// CatalogUnknown keeps a failed first probe distinct from a verified empty catalog.
	CatalogUnknown bool `json:"catalogUnknown,omitempty"`
}

// PoolView pairs the fixed label and its editable settings with a revision
// for an optimistic save. Two windows editing different pools can both save.
type PoolView struct {
	crew.Label
	crew.Pool
	Revision string `json:"revision"`
}

// CrewModel is a model of pi's catalog.
type CrewModel struct {
	crew.Model
	Key string `json:"key"`
	// Scored are the levels with AA or user CSV scores. A level without
	// either asks for the user's existing estimate.
	Scored []string `json:"scored"`
}

func (a *app) crewView(ctx context.Context, refresh bool) CrewView {
	snap := crew.Embedded()
	v := CrewView{Date: snap.Date, Source: crew.Source, Models: []CrewModel{}}
	for _, s := range snap.Scores {
		v.Top = max(v.Top, s.Intelligence)
	}
	models, at, err := a.catalog.get(ctx, refresh)
	if err != nil {
		v.Error = err.Error()
	}
	c := a.crewConfig()
	v.ReadAt, v.Yesman = at, c.Yesman
	for _, label := range crew.Labels {
		p := c.Pools[label.ID]
		v.Pools = append(v.Pools, PoolView{Label: label, Pool: p, Revision: p.Revision()})
	}
	patch, status := a.effectiveScorePatch()
	v.Patch = status
	v.CatalogUnknown = models == nil
	v.Members = crew.Resolve(c, models, snap, patch)
	for _, m := range models {
		cm := CrewModel{Model: m, Key: m.Key(), Scored: []string{}}
		for _, l := range m.Levels {
			resolved := crew.Resolve(crew.Config{Options: []crew.Option{{Provider: m.Provider, Model: m.ID, Effort: l}}}, []crew.Model{m}, snap, patch)
			if resolved[0].Score != nil {
				cm.Scored = append(cm.Scored, l)
			}
		}
		v.Models = append(v.Models, cm)
	}
	return v
}

// changeCrew saves a change to the crew settings, unless change fails.
func (a *app) changeCrew(change func(*crew.Config) error) error {
	a.setmu.Lock()
	defer a.setmu.Unlock()
	next := a.settings
	next.Crew = next.Crew.Clone()
	if err := change(&next.Crew); err != nil {
		return err
	}
	return a.putSettingsLocked(next)
}

// ownScore is a copy of the user's score with only what they give.
func ownScore(s crew.Score) (*crew.Score, error) {
	if err := crew.CheckScore(s); err != nil {
		return nil, errors.New(tr(err.Error()))
	}
	return &crew.Score{Intelligence: s.Intelligence, Speed: s.Speed, Coding: s.Coding, CostPerTask: s.CostPerTask, MinutesPerTask: s.MinutesPerTask}, nil
}

// Crew returns the crew, what new yesmen run as, and pi's models.
func (s *service) Crew(ctx context.Context) CrewView { return s.app.crewView(ctx, false) }

// RefreshCrew reads pi's catalog again, as after configuring a model.
func (s *service) RefreshCrew(ctx context.Context) CrewView { return s.app.crewView(ctx, true) }

// AddCrewOption adds one of pi's models at an effort to the crew. score is
// optional estimate when no source scores exist. The first option is what new yesmen run as.
func (s *service) AddCrewOption(ctx context.Context, provider, model, effort string, score *crew.Score) (CrewView, error) {
	models, _, err := s.app.catalog.get(ctx, false)
	i := slices.IndexFunc(models, func(m crew.Model) bool { return m.Provider == provider && m.ID == model })
	if i < 0 {
		return CrewView{}, errors.Join(errors.New(tr("pi has no model %s", provider+"/"+model)), err)
	}
	if !slices.Contains(models[i].Levels, effort) {
		return CrewView{}, errors.New(tr("%s does not take the %s effort", crew.ShortName(models[i].Name, model), effort))
	}
	o := crew.Option{Provider: provider, Model: model, Effort: effort}
	patch, _ := s.app.effectiveScorePatch()
	resolved := crew.Resolve(crew.Config{Options: []crew.Option{o}}, models, crew.Embedded(), patch)
	if resolved[0].Score == nil && score != nil {
		if o.Score, err = ownScore(*score); err != nil {
			return CrewView{}, err
		}
	}
	err = s.app.changeCrew(func(c *crew.Config) error {
		if c.Index(o.Key()) >= 0 {
			return errors.New(tr("%s is in the crew already", o.Key()))
		}
		c.Options = append(c.Options, o)
		if c.Yesman == "" {
			c.Yesman = o.Key()
		}
		return nil
	})
	return s.app.crewView(ctx, false), err
}

// SetCrewScore replaces the user's own score for an option.
func (s *service) SetCrewScore(ctx context.Context, key string, score crew.Score) (CrewView, error) {
	own, err := ownScore(score)
	if err != nil {
		return CrewView{}, err
	}
	err = s.app.changeCrew(func(c *crew.Config) error {
		i := c.Index(key)
		if i < 0 {
			return errors.New(tr("%s is not in the crew", key))
		}
		c.Options[i].Score = own
		return nil
	})
	return s.app.crewView(ctx, false), err
}

// RemoveCrewOption takes an option out of the crew. When new yesmen ran as
// it, they run as the first option left.
func (s *service) RemoveCrewOption(ctx context.Context, key string) (CrewView, error) {
	err := s.app.changeCrew(func(c *crew.Config) error {
		i := c.Index(key)
		if i < 0 {
			return errors.New(tr("%s is not in the crew", key))
		}
		c.Options = slices.Delete(c.Options, i, i+1)
		for id, p := range c.Pools {
			p.Members = slices.DeleteFunc(p.Members, func(k string) bool { return k == key })
			c.Pools[id] = p
		}
		if c.Yesman == key {
			c.Yesman = ""
			if len(c.Options) > 0 {
				c.Yesman = c.Options[0].Key()
			}
		}
		return nil
	})
	return s.app.crewView(ctx, false), err
}

// LeadModels reads or changes one workspace lead's selection from Settings.
func (s *service) LeadModels(ctx context.Context, workspace string, since time.Time, expected, change agent.Choice) (agent.ModelOptions, error) {
	return s.app.agents.LeadModels(ctx, workspace, since, expected, change)
}

// SetWorkerPool edits just one fixed label. Recheck the saved pool and Crew
// under the settings lock so a stale form cannot put a removed member back.
func (s *service) SetWorkerPool(ctx context.Context, label, revision string, p crew.Pool) (CrewView, error) {
	if !slices.ContainsFunc(crew.Labels[:], func(l crew.Label) bool { return l.ID == label }) {
		return CrewView{}, errors.New(tr("Unknown worker label."))
	}
	if err := p.Validate(); err != nil {
		return CrewView{}, errors.New(tr(err.Error()))
	}
	p.Instructions = strings.TrimSpace(p.Instructions)
	err := s.app.changeCrew(func(c *crew.Config) error {
		prev := c.Pools[label]
		if revision != prev.Revision() {
			return errors.New(tr("This pool changed. Reopen it to edit the latest settings."))
		}
		models := s.app.catalog.peek()
		var keys []string
		for _, key := range p.Members {
			i := c.Index(key)
			if i < 0 {
				return errors.New(tr("A selected model was removed. Reopen the pool to update your selection."))
			}
			o := c.Options[i]
			if !slices.Contains(prev.Members, key) && !slices.ContainsFunc(models, func(m crew.Model) bool {
				return m.Provider == o.Provider && m.ID == o.Model && slices.Contains(m.Levels, o.Effort)
			}) {
				return errors.New(tr("A selected model or effort is unavailable. Read pi’s models again."))
			}
			if !slices.Contains(keys, key) {
				keys = append(keys, key)
			}
		}
		p.Members = keys
		if c.Pools == nil {
			c.Pools = crew.Pools{}
		}
		c.Pools[label] = p
		return nil
	})
	if err != nil {
		return CrewView{}, err
	}
	return s.Crew(ctx), nil
}

// SetYesmanModel chooses the option new yesmen's leads run as, by its key.
func (s *service) SetYesmanModel(ctx context.Context, key string) (CrewView, error) {
	members, err := s.app.crew(ctx)
	if !crew.Usable(members, key) {
		return CrewView{}, errors.Join(errors.New(tr("%s is not in the crew", key)), err)
	}
	err = s.app.changeCrew(func(c *crew.Config) error {
		if c.Index(key) < 0 {
			return errors.New(tr("%s is not in the crew", key))
		}
		c.Yesman = key
		return nil
	})
	return s.app.crewView(ctx, false), err
}
