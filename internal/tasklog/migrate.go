package tasklog

// Migration is an explicit OFFLINE operation. It never runs at startup.
// Every original byte is retained, including arbitrary Markdown and non-UTF8
// files (which are refused before mutation). All output paths are derived here,
// not taken from an untrusted manifest. A marker suppresses bootstrap after a
// crash. The source inode is retained at switch, so late appenders cannot lose
// their text. External writers must still be stopped: no portable filesystem
// primitive makes a compare-and-rename atomic against an uncooperative writer.

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
	"unicode/utf8"
)

const chunkSize = 32 << 10
const maxSource = 64 << 20 // explicit refusal; original remains untouched

type Part struct {
	Path   string `json:"path"`
	Start  int    `json:"start"`
	End    int    `json:"end"`
	SHA256 string `json:"sha256"`
}

type Manifest struct {
	Version        int    `json:"version"`
	SHA256         string `json:"sha256"`
	SourceModTime  string `json:"sourceModTime"`
	Bytes          int    `json:"bytes"`
	Parts          []Part `json:"parts"`
	Catalogs       []Part `json:"catalogs,omitempty"`
	Original       string `json:"original"`
	Retained       string `json:"retained"`
	TOCHash        string `json:"tocSHA256"`
	RestoredSHA256 string `json:"restoredSHA256,omitempty"`
}

type Options struct {
	// Action is dry-run, apply, verify, or rollback.
	Action  string
	Expect  string
	Offline bool
	// Hook is used by isolated tests to inject crashes, appenders and I/O errors.
	Hook func(stage string) error
}

func digest(b []byte) string          { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }
func generation(hash string) string   { return Recovery + "/" + hash }
func original(hash string) string     { return Dir + "/legacy-" + hash + "-original.md" }
func retained(hash string) string     { return generation(hash) + "/source.md" }
func manifestPath(hash string) string { return generation(hash) + "/manifest.json" }
func hook(o Options, stage string) error {
	if o.Hook != nil {
		return o.Hook(stage)
	}
	return nil
}

func read(root *os.Root, path string, limit int64) ([]byte, error) {
	if err := Check(root, path, false); err != nil {
		return nil, err
	}
	f, err := root.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	before, err := f.Stat()
	if err != nil {
		return nil, err
	}
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("%s exceeds %d bytes; keep the original and archive manually", path, limit)
	}
	after, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !before.ModTime().Equal(after.ModTime()) || before.Size() != after.Size() || after.Size() != int64(len(b)) {
		return nil, errors.New("file changed while reading task-log snapshot")
	}
	return b, nil
}

func plan(b []byte) (Manifest, []byte) {
	hash := digest(b)
	m := Manifest{Version: 1, SHA256: hash, Bytes: len(b), Original: original(hash), Retained: retained(hash), Parts: []Part{}}
	var toc strings.Builder
	toc.WriteString(Template)
	toc.WriteString("\n## Legacy history\n\n")
	toc.WriteString("Archived without rewriting. Slices may cross Markdown constructs; use the complete original for their full rendering and old anchors. Relative links keep their original .yesman/ base in the app.\n\n")
	fmt.Fprintf(&toc, "[Complete original](task-log/legacy-%s-original.md) · %d bytes · SHA256 `%s`.\n\n", hash, len(b), hash)
	for start := 0; start < len(b); {
		end := min(start+chunkSize, len(b))
		if end < len(b) {
			if line := bytes.LastIndexByte(b[start:end], '\n'); line >= chunkSize/2 {
				end = start + line + 1
			}
			for end > start && !utf8.RuneStart(b[end]) {
				end--
			}
		}
		p := Part{Path: fmt.Sprintf("%s/legacy-%s-%04d.md", Dir, hash, len(m.Parts)+1), Start: start, End: end, SHA256: digest(b[start:end])}
		m.Parts = append(m.Parts, p)
		start = end
	}
	if len(m.Parts) <= 64 {
		for i, p := range m.Parts {
			fmt.Fprintf(&toc, "- Legacy %04d · [bytes %d–%d](%s)\n", i+1, p.Start, p.End, strings.TrimPrefix(p.Path, ".yesman/"))
		}
	} else {
		files := catalogFiles(m)
		for start := 0; start < len(m.Parts); start += 64 {
			end := min(start+64, len(m.Parts))
			path := fmt.Sprintf("%s/legacy-%s-index-%04d.md", Dir, hash, start/64+1)
			m.Catalogs = append(m.Catalogs, Part{Path: path, Start: m.Parts[start].Start, End: m.Parts[end-1].End, SHA256: digest(files[path])})
			fmt.Fprintf(&toc, "- Legacy · [slices %04d–%04d](%s)\n", start+1, end, strings.TrimPrefix(path, ".yesman/"))
		}
	}
	text := []byte(toc.String())
	m.TOCHash = digest(text)
	return m, text
}

func catalogFiles(m Manifest) map[string][]byte {
	files := map[string][]byte{}
	if len(m.Parts) <= 64 {
		return files
	}
	for start := 0; start < len(m.Parts); start += 64 {
		end := min(start+64, len(m.Parts))
		var b strings.Builder
		fmt.Fprintf(&b, "# Legacy history — slices %04d–%04d\n\n[Contents](../task-log.md)\n\n", start+1, end)
		for i := start; i < end; i++ {
			p := m.Parts[i]
			fmt.Fprintf(&b, "- Legacy %04d · [bytes %d–%d](%s)\n", i+1, p.Start, p.End, strings.TrimPrefix(p.Path, Dir+"/"))
		}
		files[fmt.Sprintf("%s/legacy-%s-index-%04d.md", Dir, m.SHA256, start/64+1)] = []byte(b.String())
	}
	return files
}

func outputs(m Manifest, b, toc []byte) map[string][]byte {
	files := catalogFiles(m)
	files[m.Original] = b
	files[manifestPath(m.SHA256)] = manifestBytes(m)
	files[generation(m.SHA256)+"/toc.md"] = toc
	return files
}

func mkdir(root *os.Root, path string) error {
	// Check every parent without following links. Only these derived directories
	// are created; no recursive deletion or replacement of an occupied path.
	current := ""
	for _, part := range strings.Split(path, "/") {
		if current != "" {
			current += "/"
		}
		current += part
		st, err := root.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			if err = root.Mkdir(current, 0o755); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		if !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("occupied or linked directory: %s", current)
		}
	}
	return nil
}

// put installs complete derived content exclusively. An interrupted temporary
// file is retained, never mistaken for complete output. Existing output must
// match exactly; unknown same-name files are never overwritten.
func put(root *os.Root, path string, b []byte) error {
	if _, err := root.Lstat(path); err == nil {
		existing, err := read(root, path, maxSource+1<<20)
		if err != nil {
			return err
		}
		if !bytes.Equal(existing, b) {
			return fmt.Errorf("occupied output: %s", path)
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	parent, _, _ := strings.Cut(path, "/")
	if i := strings.LastIndexByte(path, '/'); i >= 0 {
		parent = path[:i]
	}
	tmp := parent + "/.tasklog-" + rand.Text() + ".tmp"
	f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := f.Write(b)
	syncErr := f.Sync()
	closeErr := f.Close()
	if err = errors.Join(writeErr, syncErr, closeErr); err != nil {
		return err
	}
	// Link is exclusive even on Windows; Rename would replace an unknown file.
	if err = root.Link(tmp, path); err != nil {
		return err
	}
	return root.Remove(tmp) // only our private temporary name; installed file stays
}

func loadMarker(root *os.Root) (Manifest, error) {
	b, err := read(root, Marker, 1<<20)
	if err != nil {
		return Manifest{}, err
	}
	var m Manifest
	if err = json.Unmarshal(b, &m); err != nil {
		return m, err
	}
	if m.Version != 1 || len(m.SHA256) != 64 {
		return m, errors.New("invalid task-log migration marker")
	}
	if _, err = hex.DecodeString(m.SHA256); err != nil {
		return m, err
	}
	// Ignore all other manifest path fields until recomputed from the original.
	return m, nil
}

func verify(root *os.Root, m Manifest, toc []byte, b []byte) error {
	var rebuilt []byte
	for _, p := range m.Parts {
		data, err := read(root, p.Path, chunkSize)
		if err != nil {
			return err
		}
		if digest(data) != p.SHA256 {
			return fmt.Errorf("part hash mismatch: %s", p.Path)
		}
		rebuilt = append(rebuilt, data...)
	}
	if !bytes.Equal(rebuilt, b) || digest(rebuilt) != m.SHA256 {
		return errors.New("reconstruction mismatch")
	}
	for path, want := range outputs(m, b, toc) {
		got, err := read(root, path, maxSource+1<<20)
		if err != nil {
			return err
		}
		if !bytes.Equal(got, want) {
			return fmt.Errorf("verification mismatch: %s", path)
		}
	}
	return nil
}
func manifestBytes(m Manifest) []byte {
	b, _ := json.MarshalIndent(m, "", "  ")
	return append(b, '\n')
}

// Run returns only metadata, never historical/private content.
func Run(workspace string, o Options) (Manifest, error) {
	workspace, err := CanonicalWorkspace(workspace)
	if err != nil {
		return Manifest{}, err
	}
	root, err := os.OpenRoot(workspace)
	if err != nil {
		return Manifest{}, err
	}
	defer root.Close()
	if o.Action == "" {
		o.Action = "dry-run"
	}
	if o.Action != "dry-run" && o.Action != "apply" && o.Action != "verify" && o.Action != "rollback" {
		return Manifest{}, errors.New("unknown action")
	}
	if (o.Action == "apply" || o.Action == "rollback") && (!o.Offline || o.Expect == "") {
		return Manifest{}, errors.New("stop the app and all history writers, then supply --offline and --expect-sha256 from dry-run")
	}
	if o.Action == "apply" || o.Action == "rollback" {
		const path = ".yesman/task-log-migrate.lock"
		if err := Check(root, path, true); err != nil {
			return Manifest{}, err
		}
		f, err := root.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
		if err != nil {
			return Manifest{}, err
		}
		defer f.Close()
		unlock, err := lockFile(f)
		if err != nil {
			return Manifest{}, fmt.Errorf("another migration holds the lock: %w", err)
		}
		defer unlock()
	}
	marker, markerErr := loadMarker(root)
	hasMarker := markerErr == nil
	if markerErr != nil && !errors.Is(markerErr, os.ErrNotExist) {
		return Manifest{}, markerErr
	}
	source := TOC
	if hasMarker {
		if _, e := root.Lstat(retained(marker.SHA256)); e == nil {
			source = retained(marker.SHA256)
		} else if !errors.Is(e, os.ErrNotExist) {
			return marker, e
		}
	}
	b, err := read(root, source, maxSource)
	if err != nil {
		return Manifest{}, err
	}
	if !utf8.Valid(b) || bytes.IndexByte(b, 0) >= 0 {
		return Manifest{}, errors.New("legacy log is not UTF-8 text; original preserved, convert a copy explicitly")
	}
	if o.Action == "rollback" && hasMarker {
		if o.Expect != marker.SHA256 {
			return marker, errors.New("rollback requires the prepared snapshot SHA256")
		}
		if source == TOC {
			if bytes.Contains(b, []byte("<!-- yesman-task-log-toc v1 -->")) {
				return marker, errors.New("retained source is missing; preserve the TOC and inspect the recovery directory")
			}
			marker.RestoredSHA256 = digest(b)
			return marker, nil // switch never happened; original (including new appends) is still here
		}
		snapshot, e := read(root, original(marker.SHA256), maxSource)
		if e != nil {
			return marker, e
		}
		if digest(snapshot) != marker.SHA256 {
			return marker, errors.New("snapshot original hash mismatch")
		}
		prepared, toc := plan(snapshot)
		prepared.SourceModTime = marker.SourceModTime
		e = rollback(root, prepared, toc, b) // restore the retained inode's latest bytes, including late appends
		prepared.RestoredSHA256 = digest(b)
		return prepared, e
	}
	if !hasMarker && bytes.Contains(b, []byte("<!-- yesman-task-log-toc v1 -->")) {
		return Manifest{}, errors.New("already a TOC; no migration needed")
	}
	sourceInfo, err := root.Stat(source)
	if err != nil {
		return Manifest{}, err
	}
	if sourceInfo.Size() != int64(len(b)) {
		return Manifest{}, errors.New("source changed after snapshot read")
	}
	m, toc := plan(b)
	m.SourceModTime = sourceInfo.ModTime().UTC().Format(time.RFC3339Nano)
	if hasMarker && (marker.SHA256 != m.SHA256 || marker.SourceModTime != m.SourceModTime) {
		return marker, errors.New("source changed since preparation; retained source is authoritative, preserve it and reconcile before retry")
	}
	if o.Expect != "" && o.Expect != m.SHA256 {
		return m, errors.New("source SHA256 differs from expected snapshot")
	}
	if !hasMarker {
		if err = Check(root, Dir+"/preflight.md", true); err != nil {
			return m, err
		}
		if err = Check(root, generation(m.SHA256)+"/preflight.json", true); err != nil {
			return m, err
		}
		if _, e := root.Lstat(generation(m.SHA256)); e == nil {
			return m, errors.New("existing recovery generation is preserved; inspect it before migration")
		} else if !errors.Is(e, os.ErrNotExist) {
			return m, e
		}
		for _, p := range append(append(m.Parts, m.Catalogs...), Part{Path: m.Original}) {
			if _, e := root.Lstat(p.Path); e == nil {
				return m, fmt.Errorf("existing record is preserved: %s", p.Path)
			} else if !errors.Is(e, os.ErrNotExist) {
				return m, e
			}
		}
	}
	if o.Action == "dry-run" {
		return m, nil
	}
	if o.Action == "rollback" {
		return m, rollback(root, m, toc, b)
	}
	if o.Action == "verify" {
		if !hasMarker {
			return m, errors.New("no migration marker")
		}
		if err = verify(root, m, toc, b); err != nil {
			return m, err
		}
		current, e := read(root, TOC, maxSource)
		if e != nil {
			return m, e
		}
		if !bytes.Equal(current, toc) && !bytes.Contains(current, []byte("<!-- yesman-task-log-toc v1 -->")) {
			return m, errors.New("TOC is not installed")
		}
		return m, nil
	}
	// Fresh migration refuses any same-name existing parts, even matching bytes.
	if !hasMarker {
		reservation := Manifest{Version: m.Version, SHA256: m.SHA256, SourceModTime: m.SourceModTime, Bytes: m.Bytes}
		if err = put(root, Marker, manifestBytes(reservation)); err != nil {
			return m, err
		}
	}
	if err = hook(o, "marker"); err != nil {
		return m, err
	}
	if err = mkdir(root, Dir); err != nil {
		return m, err
	}
	if err = mkdir(root, generation(m.SHA256)); err != nil {
		return m, err
	}
	for _, p := range m.Parts {
		if err = put(root, p.Path, b[p.Start:p.End]); err != nil {
			return m, err
		}
	}
	for path, data := range outputs(m, b, toc) {
		if err = put(root, path, data); err != nil {
			return m, err
		}
	}
	if err = verify(root, m, toc, b); err != nil {
		return m, err
	}
	if err = hook(o, "prepared"); err != nil {
		return m, err
	}
	if source != TOC {
		if current, e := read(root, TOC, maxSource); e == nil && bytes.Contains(current, []byte("<!-- yesman-task-log-toc v1 -->")) {
			return m, nil // already installed; preserve subsequent TOC entries
		} else if e != nil && !errors.Is(e, os.ErrNotExist) {
			return m, e
		}
	}
	if source == TOC {
		// Content CAS immediately before retaining the source inode. Recheck it
		// after rename as well, catching append/replacement during the switch.
		current, e := read(root, TOC, maxSource)
		if e != nil {
			return m, e
		}
		currentInfo, e := root.Stat(TOC)
		if e != nil {
			return m, e
		}
		if !bytes.Equal(current, b) || !os.SameFile(sourceInfo, currentInfo) || !sourceInfo.ModTime().Equal(currentInfo.ModTime()) {
			return m, errors.New("concurrent source change; original left at task-log.md")
		}
		if err = root.Rename(TOC, m.Retained); err != nil {
			return m, err
		}
	}
	if err = hook(o, "retained"); err != nil {
		return m, err
	}
	moved, e := read(root, m.Retained, maxSource)
	if e != nil {
		return m, e
	}
	movedInfo, e := root.Stat(m.Retained)
	if e != nil {
		return m, e
	}
	if !bytes.Equal(moved, b) || !os.SameFile(sourceInfo, movedInfo) || !sourceInfo.ModTime().Equal(movedInfo.ModTime()) {
		return m, errors.New("concurrent append retained in recovery/source.md; reconcile it before continuing")
	}
	if err = put(root, TOC, toc); err != nil {
		return m, err
	}
	if err = hook(o, "installed"); err != nil {
		return m, err
	}
	moved, e = read(root, m.Retained, maxSource)
	if e != nil {
		return m, e
	}
	movedInfo, e = root.Stat(m.Retained)
	if e != nil {
		return m, e
	}
	if !bytes.Equal(moved, b) || !os.SameFile(sourceInfo, movedInfo) || !sourceInfo.ModTime().Equal(movedInfo.ModTime()) {
		return m, errors.New("late writer changed retained source; migration NOT complete")
	}
	return m, verify(root, m, toc, b)
}

func rollback(root *os.Root, m Manifest, toc, b []byte) error {
	// Preserve any post-migration edits, refusing an automatic rollback if the
	// TOC changed. Historical parts/manifest are always retained.
	current, err := read(root, TOC, maxSource)
	if errors.Is(err, os.ErrNotExist) {
		return put(root, TOC, b)
	}
	if err != nil {
		return err
	}
	if bytes.Equal(current, b) {
		return nil
	}
	if !bytes.Equal(current, toc) {
		return errors.New("TOC changed; preserve/reconcile newer entries before rollback")
	}
	path := generation(m.SHA256) + "/rollback-toc.md"
	if err = put(root, path, current); err != nil {
		return err
	}
	// A private directory gives the rename a new destination on every attempt,
	// avoiding Windows replacement behavior or overwriting an unknown file.
	saved := generation(m.SHA256) + "/rollback-" + rand.Text()
	if err = root.Mkdir(saved, 0o700); err != nil {
		return err
	}
	if err = root.Rename(TOC, saved+"/toc.md"); err != nil {
		return err
	}
	return put(root, TOC, b)
}
