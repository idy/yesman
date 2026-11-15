package main

import (
	"errors"
	"strings"
	"testing"
)

// menuShape writes items as "id" for an enabled item, "-id" for a disabled
// one and "|" for a separator.
func menuShape(items []pageMenuItem) string {
	parts := make([]string, len(items))
	for i, it := range items {
		switch {
		case it.id == "":
			parts[i] = "|"
		case it.disabled:
			parts[i] = "-" + it.id
		default:
			parts[i] = it.id
		}
	}
	return strings.Join(parts, " ")
}

func TestPageMenuItems(t *testing.T) {
	for _, c := range []struct {
		name   string
		target PageMenuTarget
		want   string
	}{
		{"empty space", PageMenuTarget{}, "-copy"},
		{"selected text", PageMenuTarget{Selection: true}, "copy"},
		{"link", PageMenuTarget{Link: true}, "open-link copy-link | -copy"},
		{"selected link text", PageMenuTarget{Link: true, Selection: true}, "open-link copy-link | copy"},
		{"empty field", PageMenuTarget{Editable: true}, "-cut -copy paste | -select-all"},
		{"field with a caret", PageMenuTarget{Editable: true, Content: true}, "-cut -copy paste | select-all"},
		{"field with a selection", PageMenuTarget{Editable: true, Content: true, Selection: true}, "cut copy paste | select-all"},
		{"read-only field", PageMenuTarget{Editable: true, ReadOnly: true, Content: true, Selection: true}, "-cut copy -paste | select-all"},
		{"password", PageMenuTarget{Editable: true, Secret: true, Content: true, Selection: true}, "-cut -copy paste | select-all"},
		{"read-only password", PageMenuTarget{Editable: true, Secret: true, ReadOnly: true, Content: true, Selection: true}, "-cut -copy -paste | select-all"},
	} {
		if got := menuShape(pageMenuItems(c.target)); got != c.want {
			t.Errorf("%s: menu %q, want %q", c.name, got, c.want)
		}
	}
	// Tests run without an app, so tr says English; labels and keys are
	// those of the platforms' Edit menus.
	for _, it := range pageMenuItems(PageMenuTarget{Link: true, Editable: true}) {
		if it.id != "" && it.label == "" {
			t.Errorf("%s has no label", it.id)
		}
		if it.id == "copy" && (it.label != "Copy" || it.accel != "CmdOrCtrl+C") {
			t.Errorf("copy item = %+v", it)
		}
	}
	for _, msg := range []string{"Open Link", "Copy Link", "Cut", "Copy", "Paste", "Select All"} {
		if trIn("zh-Hans", msg) == msg {
			t.Errorf("%q has no Chinese translation", msg)
		}
	}
}

func TestPageMenuChoiceReadsClipboardOnlyForPaste(t *testing.T) {
	reads := 0
	read := func() (string, error) { reads++; return "line one\nline two", nil }
	for _, action := range []string{"", "copy", "cut", "select-all", "open-link", "copy-link"} {
		got, err := pageMenuChoice(action, read)
		if err != nil || got != (PageMenuChoice{Action: action}) {
			t.Errorf("%q: %+v, %v", action, got, err)
		}
	}
	if reads != 0 {
		t.Fatalf("the clipboard was read %d times without Paste", reads)
	}
	got, err := pageMenuChoice("paste", read)
	if err != nil || got != (PageMenuChoice{Action: "paste", Text: "line one\nline two"}) || reads != 1 {
		t.Fatalf("paste: %+v, %v, %d reads", got, err, reads)
	}
	failed := errors.New("clipboard unavailable")
	if got, err := pageMenuChoice("paste", func() (string, error) { return "", failed }); !errors.Is(err, failed) || got.Text != "" {
		t.Fatalf("failed read: %+v, %v", got, err)
	}
}
