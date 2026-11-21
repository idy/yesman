package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"time"

	"github.com/idy/yesman/internal/crew"
	"github.com/idy/yesman/internal/yesmen"
)

// Model is a choice from pi's authenticated catalog. Keep provider headers and
// other connection settings out of the page's model list.
type Model struct {
	ID            string `json:"id"`
	Provider      string `json:"provider"`
	Name          string `json:"name"`
	ContextWindow int    `json:"contextWindow,omitempty"`
	// Levels are the thinking levels it takes, in a catalog.
	Levels []string `json:"levels,omitempty"`
}

// catalogModel is a model as pi's get_available_models lists it, with what
// decides the thinking levels it takes.
type catalogModel struct {
	Model
	Reasoning        bool                       `json:"reasoning"`
	ThinkingLevelMap map[string]json.RawMessage `json:"thinkingLevelMap,omitempty"`
}

// decodeCatalog reads get_available_models' answer.
func decodeCatalog(data json.RawMessage) ([]Model, error) {
	var d struct {
		Models []catalogModel `json:"models"`
	}
	if err := json.Unmarshal(data, &d); err != nil {
		return nil, err
	}
	out := make([]Model, 0, len(d.Models))
	for _, m := range d.Models {
		m.Model.Levels = crew.SupportedLevels(m.Reasoning, m.ThinkingLevelMap)
		out = append(out, m.Model)
	}
	return out, nil
}

// Choice is a model to run as, at a thinking level; an empty Effort leaves
// pi's.
type Choice struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Effort   string `json:"effort,omitempty"`
}

// IsZero reports whether it chooses nothing, leaving pi's default.
func (c Choice) IsZero() bool { return c.Provider == "" || c.Model == "" }

// String is the choice's crew key, provider/model:effort.
func (c Choice) String() string { return crew.MemberKey(c.Provider, c.Model, c.Effort) }

// ParseChoice reads a crew key back into a Choice.
func ParseChoice(key string) (Choice, bool) {
	provider, model, effort, ok := crew.ParseKey(key)
	return Choice{Provider: provider, Model: model, Effort: effort}, ok
}

// Catalog starts pi on its own in dir, with no session and without
// yesman's extension, to read its authenticated catalog of models. It
// loads the user's own extensions, which may add providers.
func (m *Manager) Catalog(ctx context.Context, dir string) ([]Model, error) {
	env := append(os.Environ(), m.cfg.Env...)
	path, args := m.cfg.Pi, []string{"--mode", "rpc", "--no-session"}
	if m.cfg.Command != nil {
		var prefix []string
		var err error
		path, prefix, err = m.cfg.Command(ctx)
		if err != nil {
			return nil, err
		}
		args = append(prefix, args...)
	}
	p, err := startProc(spec{Path: path, Args: args, Dir: dir, Env: env},
		func(*proc, string, json.RawMessage) {})
	if err != nil {
		return nil, err
	}
	defer p.stop(2 * time.Second)
	r, err := p.call(ctx, map[string]any{"type": "get_available_models"})
	if err != nil {
		if msg := p.stderr.String(); msg != "" {
			return nil, fmt.Errorf("%v: %s", err, lastLines(msg, 6))
		}
		return nil, err
	}
	return decodeCatalog(r.Data)
}

// applyChoice selects a model and effort on a process no one else uses
// yet. pi clamps an effort the model does not take.
func applyChoice(ctx context.Context, p *proc, c Choice) error {
	if _, err := p.call(ctx, map[string]any{"type": "set_model", "provider": c.Provider, "modelId": c.Model}); err != nil {
		return err
	}
	if c.Effort == "" {
		return nil
	}
	_, err := p.call(ctx, map[string]any{"type": "set_thinking_level", "level": c.Effort})
	return err
}

// WorkerRoute resolves a preference against the new worker's verified catalog.
// A slot refused by pi is omitted on the next resolution before any task is sent.
type WorkerRoute func(context.Context, []Model) (Choice, error)

type workerSetup struct {
	since time.Time
	route WorkerRoute
}

// prepareWorker runs under modelMu, immediately before the first prompt is
// registered on p. It never configures a worker resumed by name later.
func (m *Manager) prepareWorker(ctx context.Context, setup *workerSetup, a *agent, p *proc) error {
	m.mu.Lock()
	initial := Choice{Provider: a.info.Provider, Model: a.info.Model, Effort: a.info.Effort}
	current := a.info.Since.Equal(setup.since) && a.proc == p && !a.retiring && !a.stopping
	alreadyBusy := a.inbox != 0 || a.prompts != 0
	m.mu.Unlock()
	if !current {
		return errStopped
	}
	if alreadyBusy {
		return errWorkerClaimed
	}
	models := []Model{}
	if r, err := p.call(ctx, map[string]any{"type": "get_available_models"}); err == nil {
		if live, err := decodeCatalog(r.Data); err == nil {
			models = live
		}
	}
	// A provider can refuse a slot its catalog advertised. Remove just that
	// tuple from this worker's snapshot and re-read the saved pools. Bound
	// retries even if settings keep changing. No model turn has started yet.
	var lastErr error
	for range 2*crew.MaxPoolMembers + 1 {
		as, err := setup.route(ctx, models)
		if err != nil {
			return err
		}
		if as.IsZero() {
			if lastErr == nil || initial.IsZero() {
				return lastErr
			}
			as = initial // undo any partial selection before inheriting pi's default
		}
		if err = applyChoice(ctx, p, as); err == nil {
			info, err := m.syncModel(ctx, a, p)
			if err != nil {
				return err
			}
			if info.Provider == as.Provider && info.Model == as.Model && (as.Effort == "" || info.Effort == as.Effort) {
				return nil
			}
			lastErr = fmt.Errorf("pi could not select %s", as)
		} else {
			lastErr = fmt.Errorf("run as %s: %w", as, err)
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		i := slices.IndexFunc(models, func(m Model) bool {
			return m.Provider == as.Provider && m.ID == as.Model && slices.Contains(m.Levels, as.Effort)
		})
		if i < 0 {
			break
		} // the final lead fallback could not run either
		models[i].Levels = slices.DeleteFunc(models[i].Levels, func(level string) bool { return level == as.Effort })
	}
	return lastErr
}

var errWorkerClaimed = errors.New("the new worker already received another message")

// CallerChoice reads the lead process's own model. Extensions can switch
// models mid-turn, before the normal post-turn sync reaches the app.
func (m *Manager) CallerChoice(ctx context.Context, token string) (Choice, error) {
	m.mu.Lock()
	a := m.agents[m.tokens[token]]
	if a == nil || a.token != token || a.tokenProc == nil || a.proc != a.tokenProc || a.stopping || a.retiring {
		m.mu.Unlock()
		return Choice{}, ErrRetired
	}
	p := a.proc
	m.mu.Unlock()
	state, err := p.modelState(ctx)
	if err != nil {
		return Choice{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if a.token != token || a.proc != p || a.stopping || a.retiring {
		return Choice{}, ErrRetired
	}
	return Choice{Provider: state.Model.Provider, Model: state.Model.ID, Effort: state.ThinkingLevel}, nil
}

// RunAs selects a model and, when given, an effort for an idle agent.
func (m *Manager) RunAs(ctx context.Context, id string, c Choice) (Info, error) {
	info, err := m.SetModel(ctx, id, c.Provider, c.Model)
	if err != nil || c.Effort == "" {
		return info, err
	}
	return m.SetEffort(ctx, id, c.Effort)
}

// ModelOptions includes the agent's current selection and the models it can use.
type ModelOptions struct {
	Models []Model `json:"models"`
	Agent  Info    `json:"agent"`
}

type modelState struct {
	Model         Model        `json:"model"`
	ThinkingLevel string       `json:"thinkingLevel"`
	IsStreaming   bool         `json:"isStreaming"`
	IsCompacting  bool         `json:"isCompacting"`
	SessionID     string       `json:"sessionId"`
	PendingQueue  pendingQueue `json:"pendingQueue"`
}

// startupState fixes the process's provenance mode on its ordered reader,
// before the startup response is published or a later event is observed. The
// mode is immutable thereafter; model/session refreshes cannot race readers.
func (p *proc) startupState(ctx context.Context) (modelState, error) {
	return p.modelState(ctx, func(s modelState) { p.origins.native = s.PendingQueue.supportsPromotion() })
}

func (p *proc) modelState(ctx context.Context, at ...func(modelState)) (modelState, error) {
	var state modelState
	var decodeErr error
	_, err := p.callObserved(ctx, map[string]any{"type": "get_state"}, func(r response) {
		if !r.Success {
			return
		}
		decodeErr = json.Unmarshal(r.Data, &state)
		if decodeErr == nil && len(at) > 0 {
			at[0](state)
		}
	})
	if err != nil {
		return modelState{}, err
	}
	return state, decodeErr
}

// syncModel reads back the effective selection: changing models may also clamp
// the effort. Its caller serializes configuration with the agent's modelMu.
func (m *Manager) syncModel(ctx context.Context, a *agent, p *proc) (Info, error) {
	state, err := p.modelState(ctx, func(s modelState) { m.deliverySessionChanged(a, p, s.SessionID); m.syncPending(a, p, s.PendingQueue) })
	if err != nil {
		return Info{}, err
	}
	r, levelsErr := p.call(ctx, map[string]any{"type": "get_available_thinking_levels"})
	var data struct {
		Levels []string `json:"levels"`
	}
	if levelsErr == nil {
		levelsErr = json.Unmarshal(r.Data, &data)
	}
	m.mu.Lock()
	if a.proc != p {
		m.mu.Unlock()
		return Info{}, errStopped
	}
	a.info.Model, a.info.Provider, a.info.Effort = state.Model.ID, state.Model.Provider, state.ThinkingLevel
	a.info.EffortLevels, a.info.UpdatedAt = data.Levels, time.Now()
	m.mu.Unlock()
	m.changed()
	info, err := m.Get(a.info.ID)
	if err != nil {
		return Info{}, err
	}
	return info, levelsErr
}

// Models starts an agent if needed, and reads its model catalog and selection.
func (m *Manager) Models(ctx context.Context, id string) (ModelOptions, error) {
	a, p, err := m.ensure(id)
	if err != nil {
		return ModelOptions{}, err
	}
	return m.modelsOn(ctx, a, p)
}

func (m *Manager) modelsOn(ctx context.Context, a *agent, p *proc) (ModelOptions, error) {
	a.modelMu.Lock()
	defer a.modelMu.Unlock()
	r, err := p.call(ctx, map[string]any{"type": "get_available_models"})
	if err != nil {
		return ModelOptions{}, err
	}
	out := ModelOptions{Models: []Model{}}
	if out.Models, err = decodeCatalog(r.Data); err != nil {
		return out, err
	}
	out.Agent, err = m.syncModel(ctx, a, p)
	return out, err
}

// configure changes one setting while idle. Dependent RPC commands must finish
// in order, since pi itself dispatches commands concurrently.
func (m *Manager) configure(ctx context.Context, id string, cmd map[string]any) (Info, error) {
	a, p, err := m.ensure(id)
	if err != nil {
		return Info{}, err
	}
	return m.configureOn(ctx, a, p, cmd, nil)
}

func (m *Manager) configureOn(ctx context.Context, a *agent, p *proc, cmd map[string]any, expected *Choice) (Info, error) {
	if err := lockWithin(ctx, &a.modelMu); err != nil {
		return Info{}, err
	}
	defer a.modelMu.Unlock()
	if a.prompts != 0 {
		return Info{}, errors.New("wait for the current run to finish before changing its model or effort")
	}
	state, err := p.modelState(ctx)
	if err != nil {
		return Info{}, err
	}
	if state.IsStreaming || state.IsCompacting {
		return Info{}, errors.New("wait for the current run to finish before changing its model or effort")
	}
	if expected != nil && *expected != (Choice{Provider: state.Model.Provider, Model: state.Model.ID, Effort: state.ThinkingLevel}) {
		return Info{}, errors.New("the lead’s selection changed. Reopen its model controls")
	}
	if cmd["type"] == "set_thinking_level" {
		// pi clamps unsupported values; refuse them instead of silently selecting
		// something the user did not choose.
		info, err := m.syncModel(ctx, a, p)
		if err != nil {
			return Info{}, err
		}
		if !slices.Contains(info.EffortLevels, cmd["level"].(string)) {
			return Info{}, errors.New("this model does not support that effort")
		}
	}
	if _, err := p.call(ctx, cmd); err != nil {
		return Info{}, err
	}
	return m.syncModel(ctx, a, p)
}

// LeadModels uses the composer's configuration path, binding a Settings
// opening to the exact workspace and the selection the user saw. An empty
// change just reads; each edit changes one control.
func (m *Manager) LeadModels(ctx context.Context, workspace string, since time.Time, expected, change Choice) (ModelOptions, error) {
	if since.IsZero() {
		return ModelOptions{}, ErrRetired
	}
	a, p, err := m.ensureScopedCtx(ctx, ID(workspace, yesmen.LeadName), nil, "", &since, false, false)
	if err != nil {
		return ModelOptions{}, err
	}
	var cmd map[string]any
	switch {
	case change == (Choice{}):
		return m.modelsOn(ctx, a, p)
	case change.Effort == "" && !change.IsZero():
		cmd = map[string]any{"type": "set_model", "provider": change.Provider, "modelId": change.Model}
	case change.Effort != "" && change.Provider == "" && change.Model == "":
		cmd = map[string]any{"type": "set_thinking_level", "level": change.Effort}
	default:
		return ModelOptions{}, errors.New("choose one model or one effort")
	}
	info, err := m.configureOn(ctx, a, p, cmd, &expected)
	return ModelOptions{Agent: info}, err
}

// SetModel selects a provider and model ID from pi's available catalog.
func (m *Manager) SetModel(ctx context.Context, id, provider, model string) (Info, error) {
	return m.configure(ctx, id, map[string]any{"type": "set_model", "provider": provider, "modelId": model})
}

// SetEffort selects one of the current model's supported thinking levels.
func (m *Manager) SetEffort(ctx context.Context, id, level string) (Info, error) {
	return m.configure(ctx, id, map[string]any{"type": "set_thinking_level", "level": level})
}
