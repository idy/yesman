package main

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"strings"
	"testing"

	"github.com/egoist/mygo/transfer"
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

// fakeClipboard is a controlled clipboard: items with the representations
// given, read as MyGo's Clipboard.Read reads them (transfer.Data.Materialize):
// only the formats asked for, transfer.ErrFormat when it has none of them, and
// a representation that fails fails the read. It records the formats of each
// read.
type fakeClipboard struct {
	items [][]transfer.Representation
	err   error
	reads [][]transfer.Format
}

func (c *fakeClipboard) read(formats ...transfer.Format) (transfer.Data, error) {
	c.reads = append(c.reads, formats)
	if c.err != nil {
		return transfer.Data{}, c.err
	}
	items := make([]transfer.Item, len(c.items))
	for i, reps := range c.items {
		items[i] = transfer.NewItem(reps...)
	}
	return transfer.New(items...).Materialize(formats)
}

var (
	pngA = []byte("\x89PNG\r\n\x1a\nA")
	pngB = []byte("\x89PNG\r\n\x1a\nB")
)

func TestPageMenuChoiceReadsClipboardOnlyForPaste(t *testing.T) {
	clip := &fakeClipboard{items: [][]transfer.Representation{{transfer.Bytes(transfer.Text, []byte("line one\nline two"))}}}
	for _, action := range []string{"", "copy", "cut", "select-all", "open-link", "copy-link"} {
		got, err := pageMenuChoice(action, PageMenuTarget{Editable: true, Images: true}, clip.read)
		if err != nil || got.Action != action || got.Text != "" || got.Images != nil {
			t.Errorf("%q: %+v, %v", action, got, err)
		}
	}
	if len(clip.reads) != 0 {
		t.Fatalf("the clipboard was read %d times without Paste", len(clip.reads))
	}
	got, err := pageMenuChoice("paste", PageMenuTarget{Editable: true}, clip.read)
	if err != nil || got.Text != "line one\nline two" || got.Images != nil || len(clip.reads) != 1 {
		t.Fatalf("paste: %+v, %v, %d reads", got, err, len(clip.reads))
	}
	failed := errors.New("clipboard unavailable")
	if got, err := pageMenuChoice("paste", PageMenuTarget{Editable: true}, (&fakeClipboard{err: failed}).read); !errors.Is(err, failed) || got.Text != "" {
		t.Fatalf("failed read: %+v, %v", got, err)
	}
}

func TestPageMenuPasteImages(t *testing.T) {
	b64 := base64.StdEncoding.EncodeToString
	composer := PageMenuTarget{Editable: true, Content: true, Images: true}
	field := PageMenuTarget{Editable: true, Content: true}
	for _, c := range []struct {
		name   string
		target PageMenuTarget
		items  [][]transfer.Representation
		text   string
		images []PageMenuImage
		reads  []transfer.Format
	}{
		{"image only, composer", composer,
			[][]transfer.Representation{{transfer.Bytes(transfer.PNG, pngA)}},
			"", []PageMenuImage{{"image/png", b64(pngA)}}, []transfer.Format{transfer.Text, transfer.PNG}},
		{"two images and text, in order", composer,
			[][]transfer.Representation{
				{transfer.Bytes(transfer.PNG, pngB)},
				{transfer.Bytes(transfer.Text, []byte("caption"))},
				{transfer.Bytes(transfer.PNG, pngA)},
			},
			"caption", []PageMenuImage{{"image/png", b64(pngB)}, {"image/png", b64(pngA)}}, []transfer.Format{transfer.Text, transfer.PNG}},
		{"text only, composer", composer,
			[][]transfer.Representation{{transfer.Bytes(transfer.Text, []byte("hi"))}},
			"hi", nil, []transfer.Format{transfer.Text, transfer.PNG}},
		// A field that takes no images reads no image: an image-only
		// clipboard pastes nothing, never an empty string over a selection.
		{"image only, other field", field,
			[][]transfer.Representation{{transfer.Bytes(transfer.PNG, pngA)}},
			"", nil, []transfer.Format{transfer.Text}},
		{"image and text, other field", field,
			[][]transfer.Representation{{transfer.Bytes(transfer.PNG, pngA), transfer.Bytes(transfer.Text, []byte("alt"))}},
			"alt", nil, []transfer.Format{transfer.Text}},
		{"secret never reads images", PageMenuTarget{Editable: true, Secret: true, Images: true},
			[][]transfer.Representation{{transfer.Bytes(transfer.PNG, pngA)}},
			"", nil, []transfer.Format{transfer.Text}},
	} {
		clip := &fakeClipboard{items: c.items}
		got, err := pageMenuChoice("paste", c.target, clip.read)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if got.Text != c.text || !slices.Equal(got.Images, c.images) {
			t.Errorf("%s: text %q images %v, want %q %v", c.name, got.Text, got.Images, c.text, c.images)
		}
		if len(clip.reads) != 1 || !slices.Equal(clip.reads[0], c.reads) {
			t.Errorf("%s: reads %v, want one of %v", c.name, clip.reads, c.reads)
		}
	}
}

// A clipboard with nothing Paste takes pastes nothing, with no error, so the
// page leaves the field, its selection and caret as they were and shows no
// message; a read that fails still says so.
func TestPageMenuPasteNothingToPaste(t *testing.T) {
	field := PageMenuTarget{Editable: true, Content: true, Selection: true}
	composer := PageMenuTarget{Editable: true, Content: true, Images: true}
	imageOnly := [][]transfer.Representation{{transfer.Bytes(transfer.PNG, pngA)}}
	fileOnly := [][]transfer.Representation{{transfer.Bytes(transfer.FileList, []byte("/tmp/a.png"))}}
	jpegOnly := [][]transfer.Representation{{transfer.Bytes("image/jpeg", []byte{0xff, 0xd8, 0xff})}}
	for _, c := range []struct {
		name   string
		target PageMenuTarget
		items  [][]transfer.Representation
	}{
		{"empty, field", field, nil},
		{"empty, composer", composer, nil},
		{"image only, field", field, imageOnly},
		{"image only, secret", PageMenuTarget{Editable: true, Secret: true, Images: true}, imageOnly},
		{"files only, composer", composer, fileOnly},
		{"JPEG only, composer", composer, jpegOnly},
	} {
		got, err := pageMenuChoice("paste", c.target, (&fakeClipboard{items: c.items}).read)
		if err != nil || got.Action != "paste" || got.Text != "" || got.Images != nil {
			t.Errorf("%s: %+v, %v; want nothing to paste", c.name, got, err)
		}
	}
	// What there is still pastes, past items without it.
	got, err := pageMenuChoice("paste", composer, (&fakeClipboard{items: [][]transfer.Representation{
		fileOnly[0], {transfer.Bytes(transfer.PNG, pngB)}, jpegOnly[0], {transfer.Bytes(transfer.Text, []byte("note"))}, {transfer.Bytes(transfer.PNG, pngA)},
	}}).read)
	b64 := base64.StdEncoding.EncodeToString
	if err != nil || got.Text != "note" || !slices.Equal(got.Images, []PageMenuImage{{"image/png", b64(pngB)}, {"image/png", b64(pngA)}}) {
		t.Errorf("mixed clipboard: %+v, %v", got, err)
	}
	// Failed reads are not "nothing to paste".
	changed := errors.New("clipboard changed while it was read")
	for _, c := range []struct {
		name string
		clip *fakeClipboard
		want error
	}{
		{"read fails", &fakeClipboard{err: changed}, changed},
		{"read refused", &fakeClipboard{err: fmt.Errorf("pasteboard: %w", fs.ErrPermission)}, fs.ErrPermission},
		{"text fails", &fakeClipboard{items: [][]transfer.Representation{{transfer.Lazy(transfer.Text, func() ([]byte, error) { return nil, fs.ErrPermission })}}}, fs.ErrPermission},
		{"image fails", &fakeClipboard{items: [][]transfer.Representation{{transfer.Lazy(transfer.PNG, func() ([]byte, error) { return nil, changed })}}}, changed},
	} {
		got, err := pageMenuChoice("paste", composer, c.clip.read)
		if !errors.Is(err, c.want) || got.Text != "" || got.Images != nil {
			t.Errorf("%s: %+v, %v; want %v", c.name, got, err, c.want)
		}
	}
}

func TestPageMenuPasteImageLimits(t *testing.T) {
	png := func(n int) transfer.Representation { return transfer.Bytes(transfer.PNG, make([]byte, n)) }
	items := func(sizes ...int) (out [][]transfer.Representation) {
		for _, n := range sizes {
			out = append(out, []transfer.Representation{png(n)})
		}
		return out
	}
	composer := PageMenuTarget{Editable: true, Images: true}
	for _, c := range []struct {
		name  string
		items [][]transfer.Representation
		want  string
	}{
		{"five images", items(1, 1, 1, 1, 1), "Attach at most 4 images."},
		{"empty image", items(0), "This image is empty."},
		{"one over 5 MB", items(5<<20 + 1), "Each image must be 5 MB or smaller."},
		{"over 10 MB in all", items(4<<20, 4<<20, 3<<20), "Images must be 10 MB or smaller in total."},
	} {
		got, err := pageMenuChoice("paste", composer, (&fakeClipboard{items: c.items}).read)
		if err == nil || err.Error() != c.want || got.Images != nil {
			t.Errorf("%s: %+v, %v, want %q", c.name, got.Images, err, c.want)
		}
	}
	got, err := pageMenuChoice("paste", composer, (&fakeClipboard{items: items(5<<20, 5<<20)}).read)
	if err != nil || len(got.Images) != 2 {
		t.Errorf("two 5 MB images: %d, %v", len(got.Images), err)
	}
	for _, msg := range []string{"Attach at most 4 images.", "This image is empty.", "Each image must be 5 MB or smaller.", "Images must be 10 MB or smaller in total."} {
		if trIn("zh-Hans", msg) == msg {
			t.Errorf("%q has no Chinese translation", msg)
		}
	}
}
