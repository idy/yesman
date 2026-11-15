package main

import (
	"context"
	"encoding/base64"
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
	Images    bool `json:"images"`    // the field takes pasted images, as the composer does
}

// PageMenuChoice is the item chosen, "" when the menu was dismissed, and for
// Paste what the clipboard holds, read only once Paste is chosen: its text,
// and its images, in the clipboard's order, for a field that takes them. The
// page hands both to the field's own paste handling, as ⌘V does.
type PageMenuChoice struct {
	Action string          `json:"action"`
	Text   string          `json:"text,omitempty"`
	Images []PageMenuImage `json:"images,omitempty"`
}

// PageMenuImage is an image on the clipboard, as the page gets one from a
// paste: PNG (MyGo gives TIFF as PNG), base64.
type PageMenuImage struct {
	MimeType string `json:"mimeType"`
	Data     string `json:"data"`
}

// Limits on pasted images, as the composer takes them (web/images.js), so a
// clipboard of large images is refused here, not carried to the page.
const (
	pasteMaxImages     = 4
	pasteMaxImageBytes = 5 << 20
	pasteMaxTotalBytes = 10 << 20
)

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

// clipboardRead reads the clipboard's representations of formats.
var clipboardRead = func(formats ...transfer.Format) (transfer.Data, error) {
	return mygo.Clipboard.Read(formats...)
}

// pasteContents reads the clipboard for Paste: its text, "" when it holds
// none, and, when images is set, each item's PNG in the clipboard's order.
// Only the formats asked for are read.
func pasteContents(read func(...transfer.Format) (transfer.Data, error), images bool) (string, []PageMenuImage, error) {
	formats := []transfer.Format{transfer.Text}
	if images {
		formats = append(formats, transfer.PNG)
	}
	d, err := read(formats...)
	if err != nil {
		return "", nil, err
	}
	text, err := d.Read(transfer.Text)
	if err != nil && !errors.Is(err, transfer.ErrFormat) {
		return "", nil, err
	}
	if !images {
		return string(text), nil, nil
	}
	var out []PageMenuImage
	total := 0
	for _, item := range d.Items() {
		b, err := item.Read(transfer.PNG)
		if errors.Is(err, transfer.ErrFormat) {
			continue
		}
		if err != nil {
			return "", nil, err
		}
		if len(out) == pasteMaxImages {
			return "", nil, errors.New(tr("Attach at most 4 images."))
		}
		if len(b) == 0 {
			return "", nil, errors.New(tr("This image is empty."))
		}
		if len(b) > pasteMaxImageBytes {
			return "", nil, errors.New(tr("Each image must be 5 MB or smaller."))
		}
		if total += len(b); total > pasteMaxTotalBytes {
			return "", nil, errors.New(tr("Images must be 10 MB or smaller in total."))
		}
		out = append(out, PageMenuImage{MimeType: string(transfer.PNG), Data: base64.StdEncoding.EncodeToString(b)})
	}
	return string(text), out, nil
}

// pageMenuChoice answers the item chosen, reading the clipboard only for
// Paste, and its images only for a field that takes them.
func pageMenuChoice(action string, target PageMenuTarget, read func(...transfer.Format) (transfer.Data, error)) (PageMenuChoice, error) {
	if action != "paste" {
		return PageMenuChoice{Action: action}, nil
	}
	text, images, err := pasteContents(read, target.Images && !target.Secret)
	if err != nil {
		return PageMenuChoice{}, err
	}
	return PageMenuChoice{Action: action, Text: text, Images: images}, nil
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
	return pageMenuChoice(action, target, clipboardRead)
}
