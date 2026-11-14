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
	p, err := startProc(spec{Path: m.cfg.Pi, Args: []string{"--mode", "rpc", "--no-session"}, Dir: dir, Env: env},
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
	Model         Model  `json:"model"`
	ThinkingLevel string `json:"thinkingLevel"`
	IsStreaming   bool   `json:"isStreaming"`
	IsCompacting  bool   `json:"isCompacting"`
}

func (p *proc) modelState(ctx context.Context) (modelState, error) {
	r, err := p.call(ctx, map[string]any{"type": "get_state"})
	if err != nil {
		return modelState{}, err
	}
	var state modelState
	err = json.Unmarshal(r.Data, &state)
	return state, err
}

// syncModel reads back the effective selection: changing models may also clamp
// the effort. Its caller serializes configuration with the agent's modelMu.
func (m *Manager) syncModel(ctx context.Context, a *agent, p *proc) (Info, error) {
	state, err := p.modelState(ctx)
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
	a.modelMu.Lock()
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

// SetModel selects a provider and model ID from pi's available catalog.
func (m *Manager) SetModel(ctx context.Context, id, provider, model string) (Info, error) {
	return m.configure(ctx, id, map[string]any{"type": "set_model", "provider": provider, "modelId": model})
}

// SetEffort selects one of the current model's supported thinking levels.
func (m *Manager) SetEffort(ctx context.Context, id, level string) (Info, error) {
	return m.configure(ctx, id, map[string]any{"type": "set_thinking_level", "level": level})
}
