package server

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/idy/yesman/internal/agent"
	"github.com/idy/yesman/internal/yesmen"
)

const (
	searchPageBytes    = 3500
	readPageBytes      = 4100
	maxReadBytes       = 8000
	dispatchBriefBytes = 3500
)

var projectSources = []string{"knowledge", "working_logs", "topics", "board", "tasks"}

type projectQuery struct {
	Query  string `json:"query"`
	ID     string `json:"id"`
	Source string `json:"source"`
	From   string `json:"from"`
	To     string `json:"to"`
	Cursor string `json:"cursor,omitempty"`
}

type projectRead struct {
	Ref    string `json:"ref"`
	Cursor string `json:"cursor"`
	Large  bool   `json:"large"`
}

// Offsets are Unicode code points within an allowlisted field. Version is a
// digest of that item's public projection, independent of its collection.
type projectLocator struct {
	Scope   string `json:"w"`
	Source  string `json:"s"`
	Key     string `json:"k"`
	Version string `json:"v"`
	Field   int    `json:"f"`
	At      int    `json:"a"`
}

type projectField struct {
	Name, Text string
	Date       string
}

type projectItem struct {
	Source, Key, ID, Title, Date, Status, Revision string
	Fields                                         []projectField
}

func (i projectItem) version() string {
	return contextDigest(contextJSON(i))
}

func (i projectItem) locator(scope string, field, at int) projectLocator {
	return projectLocator{scope, i.Source, i.Key, i.version(), field, at}
}

type projectHit struct {
	Source  string `json:"source"`
	ID      string `json:"id,omitempty"`
	Title   string `json:"title"`
	Date    string `json:"date,omitempty"`
	Status  string `json:"itemStatus,omitempty"`
	Field   string `json:"field"`
	Excerpt string `json:"excerpt"`
	Ref     string `json:"ref,omitempty"`
}

type projectResponse struct {
	Version int          `json:"version"`
	Status  string       `json:"status"`
	Data    string       `json:"data,omitempty"`
	Hits    []projectHit `json:"hits,omitempty"`
	Item    *projectHit  `json:"item,omitempty"`
	Cursor  string       `json:"cursor,omitempty"`
	Sources []string     `json:"incompleteSources,omitempty"`
	Message string       `json:"message,omitempty"`
}

func contextJSON(v any) []byte {
	data, _ := json.Marshal(v)
	return data
}

func contextDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:16])
}

func contextScope(y yesmen.Yesman) string {
	return contextDigest([]byte(y.Path + "\x00" + y.Name + "\x00" + y.HiredAt.UTC().Format(time.RFC3339Nano)))
}

func contextToken(prefix string, v any) string {
	return prefix + base64.RawURLEncoding.EncodeToString(contextJSON(v))
}

func parseContextToken(token, prefix string, dst any) bool {
	b64, ok := strings.CutPrefix(token, prefix)
	if !ok || len(token) > 1000 {
		return false
	}
	data, err := base64.RawURLEncoding.DecodeString(b64)
	return err == nil && json.Unmarshal(data, dst) == nil && contextToken(prefix, dst) == token
}

func projectStatus(status, message string) projectResponse {
	return projectResponse{Version: 1, Status: status, Message: message}
}

func (s *Server) projectActor(r *http.Request) (string, agent.ContextActor, yesmen.Yesman, bool) {
	token, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	actor, err := s.Agents.ContextActor(token, r.Header.Get("X-Yesman-Input"))
	if err != nil || actor.Info.Role != agent.RoleLead && actor.Info.Role != agent.RoleWorker {
		return "", actor, yesmen.Yesman{}, false
	}
	y, err := s.Yesmen.Get(actor.Info.Yesman)
	return token, actor, y, err == nil && y.HiredAt.Equal(actor.Hire)
}

// Serialize final disclosure with process/workspace retirement. No reads or
// ranking hold the process gate. The response was already fully encoded.
func (s *Server) deliverProject(w http.ResponseWriter, r *http.Request, token string, actor agent.ContextActor, contentType string, body []byte) bool {
	delivered := false
	_ = s.Yesmen.Use(actor.Info.Yesman, func(y yesmen.Yesman) error {
		if !y.HiredAt.Equal(actor.Hire) {
			return agent.ErrRetired
		}
		release, err := s.Agents.BeginChange(token, y)
		if err != nil {
			return err
		}
		defer release()
		now, err := s.Agents.ContextActor(token, "")
		if err != nil || now.Stop != actor.Stop || now.Info.Role != actor.Info.Role || !now.Info.Since.Equal(actor.Info.Since) || r.Context().Err() != nil {
			return agent.ErrRetired
		}
		_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(time.Second))
		w.Header().Set("Content-Type", contentType)
		_, _ = w.Write(body)
		delivered = true
		return nil
	})
	return delivered
}

func (s *Server) projectLookup(read bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		respond := func(result projectResponse) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(contextJSON(result))
		}
		denied := projectStatus("denied", "This project context request is not authorized.")
		token, actor, y, ok := s.projectActor(r)
		if !ok {
			respond(denied)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
		decoder.DisallowUnknownFields()
		sources := projectCatalog{server: s, workspace: y}
		result := projectStatus("invalid", "Check the search filters or reference format.")
		if read {
			var in projectRead
			if decoder.Decode(&in) == nil {
				result = sources.read(ctx, in)
			}
		} else {
			var in projectQuery
			if decoder.Decode(&in) == nil {
				result = sources.search(ctx, in)
			}
		}
		if r.Context().Err() != nil {
			return
		}
		if !s.deliverProject(w, r, token, actor, "application/json", contextJSON(result)) {
			respond(denied)
		}
	}
}

func contextDate(s string) bool {
	if s == "" {
		return true
	}
	_, err := time.Parse(time.DateOnly, s)
	return err == nil
}

func (q projectQuery) valid() bool {
	return len(q.Query) <= 640 && utf8.RuneCountInString(q.Query) <= 160 && len(q.ID) <= 160 && len(q.Source) <= 20 &&
		contextDate(q.From) && contextDate(q.To) && (q.To == "" || q.From <= q.To)
}

func (q projectQuery) dateMatches(date string) bool {
	return q.From == "" && q.To == "" || date != "" && date >= q.From && (q.To == "" || date <= q.To)
}

type rankedProjectHit struct {
	projectHit
	key  string
	rank int
}

type projectSearchCursor struct {
	Snapshot string `json:"s"`
	Offset   int    `json:"o"`
}

func (c projectCatalog) search(ctx context.Context, q projectQuery) projectResponse {
	if !q.valid() || q.Source != "" && q.Source != "history" && !slices.Contains(projectSources, q.Source) {
		return projectStatus("invalid", "Check the search filters.")
	}
	if q.Source == "history" {
		return projectStatus("not_integrated", "Chat History is not integrated with project context.")
	}
	q.Query = strings.ToLower(strings.TrimSpace(q.Query))
	if q.Query == "" && q.ID == "" && q.From == "" && q.To == "" {
		return projectStatus("invalid", "Supply a keyword, phrase, exact ID, or date range.")
	}
	cursor := q.Cursor
	q.Cursor = ""
	var hits []rankedProjectHit
	var incomplete []string
	covered := 0
	for _, source := range projectSources {
		if q.Source != "" && q.Source != source {
			continue
		}
		coverage := c.visit(ctx, source, q.ID, func(item projectItem) {
			if item.Source == "topics" && item.Status != yesmen.TopicOpen && q.ID == "" {
				return
			}
			if hit, match := item.match(q, contextScope(c.workspace)); match {
				hits = append(hits, hit)
			}
		})
		if coverage == "" || coverage == "partial" {
			covered++
		}
		if coverage != "" {
			incomplete = append(incomplete, source)
		}
	}
	slices.SortFunc(hits, func(a, b rankedProjectHit) int {
		if a.rank != b.rank {
			return a.rank - b.rank
		}
		return strings.Compare(a.key, b.key)
	})
	var snapshot strings.Builder
	snapshot.Write(contextJSON(q))
	snapshot.WriteString(contextScope(c.workspace))
	snapshot.Write(contextJSON(incomplete))
	for _, h := range hits {
		snapshot.WriteString(h.Ref)
		snapshot.WriteByte('\n')
	}
	digest := contextDigest([]byte(snapshot.String()))
	page := projectSearchCursor{Snapshot: digest}
	if cursor != "" {
		if !parseContextToken(cursor, "psc1.", &page) || page.Offset <= 0 {
			return projectStatus("denied", "This search cursor is invalid.")
		}
		if page.Snapshot != digest || page.Offset >= len(hits) {
			return projectStatus("changed", "Search coverage or results changed. Start a fresh search.")
		}
	}
	out := projectStatus("ok", "")
	if len(incomplete) != 0 {
		out.Status, out.Sources = "partial", incomplete
		if covered == 0 {
			out.Status = "source_unavailable"
		}
	} else if len(hits) == 0 {
		out.Status = "no_match"
	}
	if page.Offset < len(hits) {
		out.Data = "Project source data"
	}
	for index := page.Offset; index < len(hits) && len(out.Hits) < 5; index++ {
		next := out
		next.Hits = append(next.Hits, hits[index].projectHit)
		if index+1 < len(hits) {
			next.Cursor = contextToken("psc1.", projectSearchCursor{digest, index + 1})
		} else {
			next.Cursor = ""
		}
		if len(contextJSON(next)) > searchPageBytes {
			break
		}
		out = next
	}
	return out
}

func (i projectItem) match(q projectQuery, scope string) (rankedProjectHit, bool) {
	if q.ID != "" && !strings.EqualFold(q.ID, i.ID) {
		return rankedProjectHit{}, false
	}
	// Plain phrases rank ahead of word-by-word fallback. All fallback terms
	// must occur in the same title or field.
	best, field, at, fieldTier := 5, -1, 0, 2
	date := i.Date
	if q.dateMatches(date) {
		switch {
		case q.ID != "" || (i.ID != "" && strings.EqualFold(i.ID, q.Query)):
			best = 0
		case q.Query != "":
			if _, tier := projectMatch(i.Title, q.Query); tier >= 0 {
				best = 1 + tier
			}
		}
	}
	for j, f := range i.Fields {
		fDate := f.Date
		if fDate == "" {
			fDate = i.Date
		}
		if !q.dateMatches(fDate) {
			continue
		}
		pos, tier := projectMatch(f.Text, q.Query)
		switch {
		case tier >= 0 && best < 3 && tier < fieldTier:
			field, at, date, fieldTier = j, pos, fDate, tier
		case tier >= 0 && 3+tier < best:
			best, field, at, date = 3+tier, j, pos, fDate
		case field == -1 && best < 3:
			field, date = j, fDate
		case field == -1 && q.Query == "":
			best, field, date = 3, j, fDate
		}
	}
	if best == 5 || field < 0 {
		return rankedProjectHit{}, false
	}
	f := i.Fields[field]
	text := []rune(f.Text)
	start := max(0, at-80)
	end := min(len(text), start+200)
	start = max(0, end-200)
	start, end = naturalExcerpt(text, start, end, at)
	hit := i.hit(f.Name, date, markedText(text, start, end))
	if (best == 1 || best == 2) && fieldTier == 2 {
		text := []rune(i.Title)
		pos, _ := projectMatch(i.Title, q.Query)
		start := max(0, min(pos-80, len(text)-200))
		hit.Excerpt = markedText(text, start, min(len(text), start+200))
	}
	hit.Ref = contextToken("pc1.", i.locator(scope, field, at))
	return rankedProjectHit{hit, i.Source + "/" + i.Key, best}, true
}

func projectMatch(text, query string) (int, int) {
	if query == "" {
		return 0, -1
	}
	lower := strings.ToLower(text)
	if at := strings.Index(lower, query); at >= 0 {
		return utf8.RuneCountInString(lower[:at]), 0
	}
	terms := strings.Fields(query)
	if len(terms) < 2 {
		return 0, -1
	}
	first := len(lower)
	for _, term := range terms {
		at := strings.Index(lower, term)
		if at < 0 {
			return 0, -1
		}
		first = min(first, at)
	}
	return utf8.RuneCountInString(lower[:first]), 1
}

func naturalExcerpt(text []rune, start, end, at int) (int, int) {
	boundary := func(r rune) bool { return unicode.IsSpace(r) || unicode.IsPunct(r) }
	if start > 0 {
		for j := start; j < min(start+18, at); j++ {
			if boundary(text[j-1]) {
				start = j
				break
			}
		}
	}
	if end < len(text) {
		for j := end; j > max(end-18, at+1); j-- {
			if boundary(text[j-1]) {
				end = j
				break
			}
		}
	}
	return start, end
}

func markedText(text []rune, start, end int) string {
	s := string(text[start:end])
	if start > 0 {
		s = "…" + s
	}
	if end < len(text) {
		s += "…"
	}
	return s
}

func (i projectItem) hit(field, date, text string) projectHit {
	return projectHit{Source: i.Source, ID: i.ID, Title: oneLine(i.Title, 90), Date: date,
		Status: i.Status, Field: oneLine(field, 55), Excerpt: text}
}

type projectReadCursor struct {
	Ref    string `json:"r"`
	Offset int    `json:"o"`
}

func (c projectCatalog) read(ctx context.Context, in projectRead) projectResponse {
	if in.Ref == "history" {
		return projectStatus("not_integrated", "Chat History is not integrated with project context.")
	}
	var loc projectLocator
	if !parseContextToken(in.Ref, "pc1.", &loc) || loc.Scope != contextScope(c.workspace) ||
		!slices.Contains(projectSources, loc.Source) || !validProjectKey(loc.Source, loc.Key) || loc.Field < 0 || loc.At < 0 {
		return projectStatus("denied", "Use a project reference from this workspace.")
	}
	var item projectItem
	coverage := c.visit(ctx, loc.Source, loc.Key, func(candidate projectItem) {
		if candidate.Key == loc.Key {
			item = candidate
		}
	})
	if item.Key == "" {
		switch coverage {
		case "denied":
			return projectStatus("denied", "The original is outside the permitted source or is linked.")
		case "":
			return projectStatus("deleted", "The original is no longer at this reference. Search again.")
		default:
			return projectStatus("source_unavailable", "The original could not be verified.")
		}
	}
	if loc.Version != item.version() || loc.Field >= len(item.Fields) {
		return projectStatus("changed", "The original changed. Search again for a current reference.")
	}
	field := item.Fields[loc.Field]
	text := []rune(field.Text)
	if loc.At > len(text) {
		return projectStatus("denied", "Invalid reference locator.")
	}
	start := max(0, loc.At-100)
	refDigest := contextDigest([]byte(in.Ref))
	if in.Cursor != "" {
		var cursor projectReadCursor
		if !parseContextToken(in.Cursor, "prc1.", &cursor) || cursor.Ref != refDigest || cursor.Offset <= start || cursor.Offset >= len(text) {
			return projectStatus("denied", "Invalid continuation for this reference.")
		}
		start = cursor.Offset
	}
	maxBytes, maxRunes := readPageBytes, 2400
	if in.Large {
		maxBytes, maxRunes = maxReadBytes, 4800
	}
	date := field.Date
	if date == "" {
		date = item.Date
	}
	out := projectStatus("ok", "")
	out.Data = "Project source data"
	hit := item.hit(field.Name, date, "")
	out.Item = &hit
	// Choose the largest forward slice whose JSON, including escapes and
	// continuation, fits the page. The locator never seeks into an escaped byte.
	for low, high := start, min(len(text), start+maxRunes); low <= high; {
		mid := low + (high-low)/2
		next := out
		copyHit := hit
		copyHit.Excerpt = markedText(text, start, mid)
		next.Item = &copyHit
		if mid < len(text) {
			next.Cursor = contextToken("prc1.", projectReadCursor{refDigest, mid})
		} else {
			next.Cursor = ""
		}
		if len(contextJSON(next)) <= maxBytes {
			out, low = next, mid+1
		} else {
			high = mid - 1
		}
	}
	return out
}

func validateDispatch(d yesmen.Dispatch) error {
	if len(contextJSON(d)) > dispatchBriefBytes {
		return fmt.Errorf("dispatch brief exceeds %d encoded bytes; keep lengthy background in task or cite a source", dispatchBriefBytes)
	}
	if d.BoardTask != "" && !projectTaskID.MatchString(d.BoardTask) {
		return fmt.Errorf("dispatch.boardTask must be an exact Board task ID")
	}
	for _, field := range []string{d.Goal, d.Constraints, d.Deliverables} {
		if len(field) > 2400 || utf8.RuneCountInString(field) > 800 {
			return fmt.Errorf("each dispatch brief field has a 2,400-byte and 800-character limit")
		}
	}
	if len(d.Sources) > 5 {
		return fmt.Errorf("at most five dispatch source references")
	}
	for _, ref := range d.Sources {
		if len(ref) > 700 {
			return fmt.Errorf("dispatch source reference exceeds 700 bytes")
		}
	}
	return nil
}

func projectNumber(n int64) string { return strconv.FormatInt(n, 10) }
