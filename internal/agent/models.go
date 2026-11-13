package agent

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"
)

// Model is a choice from pi's authenticated catalog. Keep provider headers and
// other connection settings out of the page's model list.
type Model struct {
	ID            string `json:"id"`
	Provider      string `json:"provider"`
	Name          string `json:"name"`
	ContextWindow int    `json:"contextWindow,omitempty"`
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
	if err := json.Unmarshal(r.Data, &out); err != nil {
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
