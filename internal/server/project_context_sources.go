package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/idy/yesman/internal/safefs"
	"github.com/idy/yesman/internal/schedule"
	"github.com/idy/yesman/internal/yesmen"
)

type projectCatalog struct {
	server    *Server
	workspace yesmen.Yesman
}

const projectDocBytes = 2 << 20

var (
	projectTaskID     = regexp.MustCompile(`^t[1-9][0-9]{0,11}$`)
	projectTopicID    = regexp.MustCompile(`^tp[1-9][0-9]{0,11}$`)
	projectScheduleID = regexp.MustCompile(`^s[1-9][0-9]{0,5}(?:-[a-z0-9-]{1,80})?$`)
	projectDate       = regexp.MustCompile(`(?:^|[^0-9])([0-9]{4}-[0-9]{2}-[0-9]{2})(?:[^0-9]|$)`)
)

func validProjectComponent(part string) bool {
	return part != "" && !strings.HasPrefix(part, ".") && !strings.HasSuffix(part, " ") && !strings.HasSuffix(part, ".") &&
		!strings.ContainsAny(part, `\/:~<>"|?*`) && strings.IndexFunc(part, func(r rune) bool { return r < 32 || r == 127 }) < 0 && utf8.ValidString(part)
}

func validProjectKey(source, key string) bool {
	switch source {
	case "board":
		return projectTaskID.MatchString(key)
	case "topics":
		return projectTopicID.MatchString(key)
	case "tasks":
		return projectScheduleID.MatchString(key)
	case "knowledge", "working_logs":
		if len(key) > 160 || !strings.EqualFold(path.Ext(key), ".md") {
			return false
		}
		parts := strings.Split(key, "/")
		if len(parts) > 5 {
			return false
		}
		for _, part := range parts {
			if !validProjectComponent(part) {
				return false
			}
		}
		return true
	}
	return false
}

// The configured data root may have an OS alias. Every component below it
// is opened relative to its parent and checked by the same native handle
// layer used for managed files. No filename is interpreted as a link target.
func projectMeta(y yesmen.Yesman) (*safefs.Dir, error) {
	root, err := safefs.Open(filepath.Dir(y.Path))
	if err != nil {
		return nil, err
	}
	defer root.Close()
	return safefs.OpenPath(root, []string{y.Name, ".yesman"}, nil)
}

var errProjectUnsafe = errors.New("unsafe project document")

func readProjectFile(d *safefs.Dir, name string, limit int64) ([]byte, error) {
	f, err := d.OpenFile(name, os.O_RDONLY, 0)
	if err != nil {
		if errors.Is(err, safefs.ErrLink) {
			return nil, errProjectUnsafe
		}
		return nil, err
	}
	defer f.Close()
	before, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if links, err := linkCount(f, before); !before.Mode().IsRegular() || err != nil || links != 1 {
		return nil, errProjectUnsafe
	}
	if before.Size() > limit {
		return nil, errors.New("project document exceeds read budget")
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	after, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if links, err := linkCount(f, after); err != nil || links != 1 {
		return nil, errProjectUnsafe
	}
	if int64(len(data)) > limit || before.Size() != after.Size() || before.ModTime() != after.ModTime() || int64(len(data)) != after.Size() {
		return nil, errors.New("project document changed during read")
	}
	return data, nil
}

func projectBoard(y yesmen.Yesman) (yesmen.Board, error) {
	var board yesmen.Board
	dir, err := projectMeta(y)
	if err != nil {
		return board, err
	}
	defer dir.Close()
	data, err := readProjectFile(dir, "board.json", 4<<20)
	if errors.Is(err, fs.ErrNotExist) {
		return board, nil
	}
	if err != nil {
		return board, err
	}
	if err := json.Unmarshal(data, &board); err != nil {
		return board, err
	}
	seen := make(map[string]bool, len(board.Tasks))
	for _, t := range board.Tasks {
		if !projectTaskID.MatchString(t.ID) || seen[t.ID] {
			return yesmen.Board{}, errors.New("invalid task identity")
		}
		seen[t.ID] = true
	}
	return board, nil
}

// visit is the only source allowlist. A targeted request names one logical
// item. A scan can report incomplete coverage without returning raw I/O errors.
func (c projectCatalog) visit(ctx context.Context, source, key string, emit func(projectItem)) string {
	if ctx.Err() != nil {
		return "unavailable"
	}
	if key != "" && !validProjectKey(source, key) {
		return ""
	}
	visit := func(i projectItem) {
		if ctx.Err() == nil && (key == "" || key == i.Key) {
			emit(i)
		}
	}
	switch source {
	case "knowledge", "working_logs":
		return c.visitDocuments(ctx, source, key, visit)
	case "board":
		board, err := projectBoard(c.workspace)
		if err != nil {
			return "unavailable"
		}
		for _, t := range board.Tasks {
			if ctx.Err() != nil {
				break
			}
			if key == "" || key == t.ID {
				visit(boardProjectItem(t))
			}
		}
	case "topics":
		topics, err := c.server.Yesmen.Topics(c.workspace.Name, c.workspace.HiredAt)
		if err != nil {
			return "unavailable"
		}
		for _, t := range topics {
			if ctx.Err() != nil {
				break
			}
			if key == "" || key == t.ID {
				visit(topicProjectItem(t))
			}
		}
	case "tasks":
		partial, err := schedule.VisitContextRecords(ctx, c.workspace, key, func(p schedule.Plan) { visit(scheduleProjectItem(p)) }, func(r schedule.Run) { visit(runProjectItem(r)) })
		if err != nil {
			return "unavailable"
		}
		if partial {
			return "partial"
		}
	}
	if ctx.Err() != nil {
		return "partial"
	}
	return ""
}

func boardProjectItem(t yesmen.Task) projectItem {
	i := projectItem{Source: "board", Key: t.ID, ID: t.ID, Title: t.Title, Status: t.Status, Date: day(t.UpdatedAt), Revision: t.UpdatedAt.UTC().Format(time.RFC3339Nano)}
	if w := t.Wait; w != nil {
		i.add("Wait kind", w.Kind)
		i.add("Blocker", w.Reason)
		i.add("Waits for Board tasks", strings.Join(w.Tasks, ", "))
		i.add("Needs You handoff", w.Handoff)
	}
	i.add("Detail", t.Detail)
	i.add("Note", t.Note)
	i.add("Owner", t.Owner)
	if t.Schedule != nil {
		i.add("Scheduled plan", t.Schedule.Plan)
	}
	return i.withFallback()
}

func topicProjectItem(t yesmen.Topic) projectItem {
	i := projectItem{Source: "topics", Key: t.ID, ID: t.ID, Title: t.Title, Status: t.Status, Date: day(t.UpdatedAt), Revision: projectNumber(t.Revision)}
	for _, line := range t.Conclusions {
		i.add("Confirmed conclusion "+line.ID, line.Text)
	}
	for _, line := range t.Questions {
		i.add("Open question "+line.ID, line.Text)
	}
	i.add("Interim summary", t.Summary)
	for _, src := range t.Sources {
		// Context retrieval never exposes submission IDs, internal session
		// coordinates or URLs. The explicit Topics tool remains available.
		if src.Kind == yesmen.SourceTask || src.Kind == yesmen.SourceKnowledge {
			i.add("Source "+src.Kind, src.Ref)
		}
	}
	return i.withFallback()
}

func scheduleProjectItem(p schedule.Plan) projectItem {
	status := "off"
	if p.Archived() {
		status = "archived"
	} else if p.Enabled {
		status = "on"
	}
	i := projectItem{Source: "tasks", Key: p.ID, ID: p.ID, Title: p.Title, Status: status, Date: day(p.UpdatedAt), Revision: projectNumber(int64(p.Revision))}
	i.add("Instructions", p.Instructions)
	i.add("Schedule", p.Schedule.Describe())
	i.add("Time zone", p.Timezone)
	return i.withFallback()
}

func runProjectItem(r schedule.Run) projectItem {
	i := projectItem{Source: "tasks", Key: r.ID, ID: r.ID, Title: r.Title, Status: r.State, Date: day(r.At), Revision: r.UpdatedAt.UTC().Format(time.RFC3339Nano)}
	i.add("Scheduled run kind", r.Kind)
	i.add("Instructions at run time", r.Instructions)
	i.add("Plan", r.Plan)
	i.add("Linked Board task", r.Board)
	return i.withFallback()
}

func (i *projectItem) add(field, text string) {
	if text != "" {
		i.Fields = append(i.Fields, projectField{Name: field, Text: text})
	}
}

func (i projectItem) withFallback() projectItem {
	if len(i.Fields) == 0 {
		i.Fields = []projectField{{Name: "Title", Text: i.Title}}
	}
	return i
}

func day(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.DateOnly)
}

func declaredDate(s string) string {
	if m := projectDate.FindStringSubmatch(s); len(m) != 0 && contextDate(m[1]) {
		return m[1]
	}
	return ""
}

func markdownProjectItem(source, key, text string) projectItem {
	i := projectItem{Source: source, Key: key, ID: key, Title: path.Base(key), Date: declaredDate(path.Base(key))}
	name, date, start, offset := "Document", "", 0, 0
	for line := range strings.SplitSeq(text, "\n") {
		trim := strings.TrimSpace(line)
		title := strings.TrimSpace(strings.TrimLeft(trim, "#"))
		heading := strings.HasPrefix(trim, "#") && len(trim) > len(title) && title != ""
		lineDate := ""
		if heading || source == "working_logs" && (strings.HasPrefix(trim, "- ") || strings.HasPrefix(trim, "* ")) {
			lineDate = declaredDate(trim)
		}
		if heading && i.Title == path.Base(key) && strings.HasPrefix(trim, "# ") {
			i.Title = title
		}
		if (heading || lineDate != "") && offset > start {
			i.Fields = append(i.Fields, projectField{name, text[start:offset], date})
			start = offset
		}
		if heading {
			name, date = title, lineDate
		} else if lineDate != "" {
			name, date = "Dated log entry", lineDate
		}
		offset += len(line) + 1
	}
	if start < len(text) || len(i.Fields) == 0 {
		i.Fields = append(i.Fields, projectField{name, text[start:], date})
	}
	return i
}

func (c projectCatalog) visitDocuments(ctx context.Context, source, key string, visit func(projectItem)) string {
	meta, err := projectMeta(c.workspace)
	if err != nil {
		return "unavailable"
	}
	defer meta.Close()
	partial, scanned, namesSeen := false, 0, 0
	load := func(d *safefs.Dir, file, key string) string {
		if ctx.Err() != nil || scanned >= 12<<20 {
			partial = true
			return "partial"
		}
		data, err := readProjectFile(d, file, projectDocBytes)
		if err != nil || !utf8.Valid(data) {
			if errors.Is(err, fs.ErrNotExist) && key != "" {
				return ""
			}
			partial = true
			if errors.Is(err, errProjectUnsafe) {
				return "denied"
			}
			return "unavailable"
		}
		scanned += len(data)
		visit(markdownProjectItem(source, key, string(data)))
		return ""
	}
	folder := "knowledge"
	if source == "working_logs" {
		folder = "task-log"
		if key == "task-log.md" {
			return load(meta, key, key)
		}
		if key == "" {
			load(meta, "task-log.md", "task-log.md")
		}
	}
	dir, err := meta.OpenDir(folder)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) && !partial {
			return ""
		}
		if errors.Is(err, safefs.ErrLink) && key != "" {
			return "denied"
		}
		return "unavailable"
	}
	defer dir.Close()
	if key != "" {
		return readExactDocument(dir, strings.Split(key, "/"), key, load)
	}
	var walk func(*safefs.Dir, string, int)
	walk = func(d *safefs.Dir, prefix string, depth int) {
		names, err := d.ReadNames(2049)
		if err != nil && !errors.Is(err, io.EOF) || len(names) > 2048 {
			partial = true
			return
		}
		slices.Sort(names)
		for _, name := range names {
			namesSeen++
			if ctx.Err() != nil || namesSeen > 4096 || scanned >= 12<<20 {
				partial = true
				return
			}
			if !validProjectComponent(name) {
				continue
			}
			itemKey := prefix + name
			if validProjectKey(source, itemKey) {
				// The reserved ID always names the legacy log or TOC.
				if source != "working_logs" || itemKey != "task-log.md" {
					load(d, name, itemKey)
				}
				continue
			}
			if depth < 5 && len(itemKey) < 155 {
				sub, err := d.OpenDir(name)
				if err == nil {
					walk(sub, itemKey+"/", depth+1)
					sub.Close()
				} else if errors.Is(err, safefs.ErrLink) || errors.Is(err, fs.ErrPermission) {
					partial = true
				}
			}
		}
	}
	walk(dir, "", 1)
	if partial {
		return "partial"
	}
	return ""
}

func readExactDocument(dir *safefs.Dir, parts []string, key string, load func(*safefs.Dir, string, string) string) string {
	// Require an exact registered name, including on Windows where opening
	// by an alternate spelling could otherwise find the same file.
	names, err := dir.ReadNames(2049)
	if err != nil && !errors.Is(err, io.EOF) || len(names) > 2048 {
		return "unavailable"
	}
	if !slices.Contains(names, parts[0]) {
		return ""
	}
	if len(parts) == 1 {
		return load(dir, parts[0], key)
	}
	sub, err := dir.OpenDir(parts[0])
	if err != nil {
		if errors.Is(err, safefs.ErrLink) {
			return "denied"
		}
		return "unavailable"
	}
	defer sub.Close()
	return readExactDocument(sub, parts[1:], key, load)
}
