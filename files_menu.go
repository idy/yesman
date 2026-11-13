package main

import (
	"context"
	"errors"
	"path/filepath"
	"runtime"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/transfer"
)

// popup shows a native menu at x, y of the caller's window and returns the
// ID of the item chosen, or "" when it was dismissed.
func popup(ctx context.Context, x, y int, items func(item func(id, label string, extra ...func(*mygo.MenuItem)) *mygo.MenuItem) []*mygo.MenuItem) (string, error) {
	win := mygo.CallerWindow(ctx)
	if win == nil {
		return "", errors.New("the menu needs a window")
	}
	selected := ""
	item := func(id, label string, extra ...func(*mygo.MenuItem)) *mygo.MenuItem {
		m := &mygo.MenuItem{ID: id, Label: label, Click: func(*mygo.MenuItem, *mygo.Window) { selected = id }}
		for _, f := range extra {
			f(m)
		}
		return m
	}
	mygo.NewMenu(items(item)).PopupAt(win, x, y)
	return selected, nil
}

func checked(on bool) func(*mygo.MenuItem) {
	return func(m *mygo.MenuItem) { m.Type, m.Checked = mygo.MenuItemCheckbox, on }
}

func accel(a string) func(*mygo.MenuItem) { return func(m *mygo.MenuItem) { m.Accelerator = a } }

func disabled(off bool) func(*mygo.MenuItem) { return func(m *mygo.MenuItem) { m.Disabled = off } }

// revealLabel names showing a file in the system's file manager.
func revealLabel() string {
	switch runtime.GOOS {
	case "darwin":
		return "Reveal in Finder"
	case "windows":
		return "Show in Explorer"
	}
	return "Show in Files"
}

// FilesMenu opens the files pane's menu. tree, wrap, autoSave and folders
// give the pane's state: the tree shown, long lines wrapped, edits saved as
// they are made, folders expanded.
func (s *service) FilesMenu(ctx context.Context, x, y int, tree, wrap, autoSave, folders bool) (string, error) {
	return popup(ctx, x, y, func(item func(string, string, ...func(*mygo.MenuItem)) *mygo.MenuItem) []*mygo.MenuItem {
		return []*mygo.MenuItem{
			item("find", "Go to File…", accel("CmdOrCtrl+P")),
			mygo.Separator(),
			item("tree", "Show File Tree", checked(tree), accel("CmdOrCtrl+Shift+Y")),
			item("collapse", "Collapse All Folders", disabled(!folders)),
			mygo.Separator(),
			item("autosave", "Auto Save", checked(autoSave)),
			item("wrap", "Word Wrap", checked(wrap)),
			mygo.Separator(),
			item("knowledge", "Open Knowledge Notes"),
			item("checkout", "Check Out a Repository…"),
			item("reveal", revealLabel()),
			mygo.Separator(),
			item("dismiss", "Dismiss yesman…"),
		}
	})
}

// FileMenu opens the menu of a file or directory in the tree. It reveals
// and copies paths itself, and returns "open" for the page to open it.
func (s *service) FileMenu(ctx context.Context, yesman, rel string, dir bool, x, y int) (string, error) {
	ys, err := s.app.yesmen.Get(yesman)
	if err != nil {
		return "", err
	}
	rel = cleanRel(rel)
	abs := filepath.Join(ys.Path, filepath.FromSlash(rel))
	choice, err := popup(ctx, x, y, func(item func(string, string, ...func(*mygo.MenuItem)) *mygo.MenuItem) []*mygo.MenuItem {
		var items []*mygo.MenuItem
		if !dir {
			items = append(items, item("open", "Open"), mygo.Separator())
		}
		return append(items,
			item("reveal", revealLabel()),
			mygo.Separator(),
			item("copy", "Copy Path"),
			item("copy-rel", "Copy Relative Path"),
		)
	})
	if err != nil {
		return "", err
	}
	switch choice {
	case "reveal":
		mygo.Shell.ShowItemInFolder(abs)
	case "copy":
		return "", mygo.Clipboard.Write(transfer.TextData(abs))
	case "copy-rel":
		return "", mygo.Clipboard.Write(transfer.TextData(rel))
	}
	return choice, nil
}
