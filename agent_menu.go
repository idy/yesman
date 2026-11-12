package main

import (
	"context"
	"errors"

	"github.com/egoist/mygo"
	"github.com/idy/yesman/internal/agent"
)

// AgentMenu opens a native context menu for a sidebar row. Return the
// selected action to the page so it reuses the existing interactions and
// confirmation dialogs without doing blocking work on the UI thread.
func (s *service) AgentMenu(ctx context.Context, id string, x, y int) (string, error) {
	win := mygo.CallerWindow(ctx)
	if win == nil {
		return "", errors.New("the agent menu needs a window")
	}
	info, err := s.app.agents.Get(id)
	if err != nil {
		return "", err
	}
	selected := ""
	item := func(id, label string, disabled bool) *mygo.MenuItem {
		return &mygo.MenuItem{
			ID: id, Label: label, Disabled: disabled,
			Click: func(*mygo.MenuItem, *mygo.Window) { selected = id },
		}
	}
	working := info.Status == agent.Working || info.Status == agent.Waiting
	running := working || info.Status == agent.Idle || info.Status == agent.Starting
	removeID, removeLabel := "remove", "Remove Worker…"
	if info.Role == agent.RoleLead {
		removeID, removeLabel = "archive", "Archive yesman…"
	}
	menu := mygo.NewMenu([]*mygo.MenuItem{
		item("open", "Open Conversation", false),
		item("workspace", "View Workspace", false),
		mygo.Separator(),
		item("reveal", "Reveal Workspace", false),
		item("knowledge", "Reveal Knowledge", false),
		item("logs", "Reveal Session Logs", false),
		mygo.Separator(),
		item("abort", "Stop Current Run", !working),
		item("sleep", "Put to Sleep", !running),
		mygo.Separator(),
		item(removeID, removeLabel, false),
	})
	menu.PopupAt(win, x, y)
	return selected, nil
}
