package main

import (
	"context"
	"errors"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/transfer"
)

// The page's own context menu: what yesman shows for a right-click, a
// Control-click on macOS or the menu key wherever the page has no menu of
// its own (the sidebar's rows and the file tree have theirs). The page
// prevents the contextmenu event and asks for this menu (web/contextmenu.js),
// so the web view's menu, with Look Up, Translate, Search with Google,
// Share, Speech and Services on macOS, never shows; showWindow prevents
// any that still reaches the web view.

// PageMenuTarget is what the menu opened on, as the page saw it. It holds no
// text: a selection stays in the page, which copies it with CopyValue only
// once Copy or Cut is chosen, and never out of a secret.
type PageMenuTarget struct {
	Link      bool `json:"link"`      // a link the page opens: Open Link and Copy Link
	Editable  bool `json:"editable"`  // a text field, the editor or other editable content
	ReadOnly  bool `json:"readOnly"`  // ...that cannot be changed: no Cut or Paste
	Secret    bool `json:"secret"`    // a password or a secret's value: nothing is copied out of it
	Selection bool `json:"selection"` // text is selected, which Copy copies
	Content   bool `json:"content"`   // the field has text for Select All
}

// PageMenuChoice is the item chosen, "" when the menu was dismissed, and for
// Paste the clipboard's text, read only once Paste is chosen.
type PageMenuChoice struct {
	Action string `json:"action"`
	Text   string `json:"text,omitempty"`
}

// pageMenuItem is an item of the page's menu; one without an ID is a
// separator.
type pageMenuItem struct {
	id, label, accel string
	disabled         bool
}

// pageMenuItems lists the page's menu for what it opened on: a link's
// items, then Cut, Copy, Paste and Select All in a field, or Copy, enabled
// for a selection, elsewhere.
func pageMenuItems(t PageMenuTarget) []pageMenuItem {
	var items []pageMenuItem
	if t.Link {
		items = append(items,
			pageMenuItem{id: "open-link", label: tr("Open Link")},
			pageMenuItem{id: "copy-link", label: tr("Copy Link")},
			pageMenuItem{})
	}
	noCopy := !t.Selection || t.Secret
	copyItem := pageMenuItem{id: "copy", label: tr("Copy"), accel: "CmdOrCtrl+C", disabled: noCopy}
	if !t.Editable {
		return append(items, copyItem)
	}
	return append(items,
		pageMenuItem{id: "cut", label: tr("Cut"), accel: "CmdOrCtrl+X", disabled: noCopy || t.ReadOnly},
		copyItem,
		pageMenuItem{id: "paste", label: tr("Paste"), accel: "CmdOrCtrl+V", disabled: t.ReadOnly},
		pageMenuItem{},
		pageMenuItem{id: "select-all", label: tr("Select All"), accel: "CmdOrCtrl+A", disabled: !t.Content})
}

// clipboardText reads the clipboard's text for Paste: "" when it holds none.
var clipboardText = func() (string, error) {
	b, err := mygo.Clipboard.ReadFormat(transfer.Text)
	if errors.Is(err, transfer.ErrFormat) {
		return "", nil
	}
	return string(b), err
}

// pageMenuChoice answers the item chosen, reading the clipboard only for
// Paste.
func pageMenuChoice(action string, read func() (string, error)) (PageMenuChoice, error) {
	if action != "paste" {
		return PageMenuChoice{Action: action}, nil
	}
	text, err := read()
	if err != nil {
		return PageMenuChoice{}, err
	}
	return PageMenuChoice{Action: action, Text: text}, nil
}

// PageMenu opens the page's menu at x, y of the caller's window for what it
// opened on, and returns the item chosen, which the page carries out.
func (s *service) PageMenu(ctx context.Context, x, y int, target PageMenuTarget) (PageMenuChoice, error) {
	action, err := popup(ctx, x, y, func(item func(string, string, ...func(*mygo.MenuItem)) *mygo.MenuItem) []*mygo.MenuItem {
		var items []*mygo.MenuItem
		for _, it := range pageMenuItems(target) {
			if it.id == "" {
				items = append(items, mygo.Separator())
				continue
			}
			items = append(items, item(it.id, it.label, disabled(it.disabled), accel(it.accel)))
		}
		return items
	})
	if err != nil {
		return PageMenuChoice{}, err
	}
	return pageMenuChoice(action, clipboardText)
}
