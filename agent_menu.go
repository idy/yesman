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
// doneHidden says whether the page hides the yesman's done workers, which a
// lead's menu shows or hides.
func (s *service) AgentMenu(ctx context.Context, id string, x, y int, doneHidden bool) (string, error) {
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
	removeID, removeLabel := "remove", tr("Remove Worker…")
	var done []*mygo.MenuItem
	if info.Role == agent.RoleLead {
		removeID, removeLabel = "archive", tr("Archive yesman…")
		finished := 0
		for _, i := range s.app.agents.List() {
			if i.Yesman == info.Yesman && !i.FinishedAt.IsZero() {
				finished++
			}
		}
		if doneHidden {
			done = append(done, item("show-done", tr("Show Done Workers"), false), mygo.Separator())
		} else {
			done = append(done, item("hide-done", tr("Hide Done Workers"), finished == 0), mygo.Separator())
		}
	}
	menu := mygo.NewMenu(append(done, []*mygo.MenuItem{
		item("open", tr("Open Conversation"), false),
		item("workspace", tr("View Workspace"), false),
		mygo.Separator(),
		item("reveal", tr("Reveal Workspace"), false),
		item("knowledge", tr("Reveal Knowledge"), false),
		item("logs", tr("Reveal Session Logs"), false),
		mygo.Separator(),
		item("abort", tr("Stop Current Run"), !working),
		item("sleep", tr("Put to Sleep"), !running),
		mygo.Separator(),
		item(removeID, removeLabel, false),
	}...))
	menu.PopupAt(win, x, y)
	return selected, nil
}
