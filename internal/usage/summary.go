package usage

import (
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/idy/yesman/internal/yesmen"
)

// Roles of agents.
const (
	RoleWorker = "worker"
	RoleLead   = "lead"
)

// Workspace is one yesman's records, with what identifies its agents and
// the board whose tasks they completed.
type Workspace struct {
	Name    string
	HiredAt time.Time
	Workers []yesmen.Worker
	// Starts are the recorded starts of workers, removed ones included.
	Starts []yesmen.WorkerStart
	Board  yesmen.Board
	// Agents are the records of each session directory, by agent name.
	Agents map[string][]Record
	Stats  DirStats
}

// Query chooses what a summary counts.
type Query struct {
	// Workers and Leads choose the agents counted, by role.
	Workers, Leads bool
	// Since counts only what happened since then; zero counts all the
	// history the logs keep.
	Since time.Time
}

func (q Query) counts(role string) bool {
	return role == RoleWorker && q.Workers || role == RoleLead && q.Leads
}

// Row is the usage of one model at one effort.
type Row struct {
	// Provider and Model name the model requested, Model in full; both are
	// empty for usage pi records without a model. Effort is the Pi thinking
	// level requested, empty when not recorded.
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Effort   string `json:"effort"`
	// ResponseModels are the other models the provider said answered.
	ResponseModels []string `json:"responseModels,omitempty"`
	// Workers and Leads count the distinct agents with usage in the row.
	Workers int `json:"workers"`
	Leads   int `json:"leads"`
	// Tasks counts the done board tasks attributed to the model.
	Tasks int `json:"tasks"`
	// Responses counts model requests: assistant messages. Failed counts
	// those that ended in an error or were aborted. Other counts usage
	// records that are not responses.
	Responses int `json:"responses"`
	Failed    int `json:"failed"`
	Other     int `json:"other"`
	Tokens
	// Timed counts the completed responses whose duration pi measured, and
	// Untimed the others; TimedOutput and TimedMs sum their output tokens
	// and durations. Speed is TimedOutput per second of TimedMs, nil when
	// none was timed.
	Timed       int      `json:"timed"`
	Untimed     int      `json:"untimed"`
	TimedOutput int64    `json:"timedOutput"`
	TimedMs     int64    `json:"timedMs"`
	Speed       *float64 `json:"speed"`
	// First and Last are when its first and last records were made.
	First time.Time `json:"first,omitzero"`
	Last  time.Time `json:"last,omitzero"`
}

// Summary is the usage of each model at each effort, with totals and how
// much the records cover.
type Summary struct {
	Rows []Row `json:"rows"`
	// Total sums the rows. Its Workers and Leads count each agent once, and
	// its Speed is nil: speeds of different models are not combined.
	Total Row `json:"total"`
	// Overlap counts the workers in more than one row.
	Overlap  int        `json:"overlap"`
	Tasks    TaskCounts `json:"tasks"`
	Coverage Coverage   `json:"coverage"`
}

// TaskCounts account for the done tasks: Done is Attributed and each reason
// one was not. TimeUnknown are not in Done: tasks done at a time not
// recorded, left out of a period's count.
type TaskCounts struct {
	Done          int `json:"done"`
	Attributed    int `json:"attributed"`
	SeveralModels int `json:"severalModels"`
	NoResponses   int `json:"noResponses"`
	Unowned       int `json:"unowned"`
	UnknownOwner  int `json:"unknownOwner"`
	OtherRole     int `json:"otherRole"`
	TimeUnknown   int `json:"timeUnknown"`
	// Recorded counts those of Done whose completion time was recorded.
	Recorded int `json:"recorded"`
}

// Coverage says what the summary read and could not.
type Coverage struct {
	Workspaces int `json:"workspaces"`
	Files      int `json:"files"`
	// Records counts the usage records in the summary; From and To are the
	// earliest and latest.
	Records int       `json:"records"`
	From    time.Time `json:"from,omitzero"`
	To      time.Time `json:"to,omitzero"`
	// Unreadable lines are not JSON or hold malformed usage; Oversized lines
	// were too long to read.
	Unreadable int `json:"unreadable"`
	Oversized  int `json:"oversized"`
	// NoEffort counts responses without a recorded effort, NoModel records
	// without a model.
	NoEffort int      `json:"noEffort"`
	NoModel  int      `json:"noModel"`
	Errors   []string `json:"errors"`
}

// maxErrors bounds the read errors a summary lists.
const maxErrors = 20

// completed reports whether a response ended normally, with its whole
// output: those are timed for speed.
func completed(stop string) bool {
	return stop == "stop" || stop == "toolUse" || stop == "length"
}

func failed(stop string) bool { return stop == "error" || stop == "aborted" }

// instance is one agent: a lead of one hire, or a worker of one start.
// A worker started again under a name continues that name's session, so
// the name's records are split by when each start happened: the recorded
// starts, removed workers' included, and the current workers'. Records
// before the first known start belong to earlier starts that were not
// recorded, counted as one; a name with no known start is one removed
// worker.
type instance struct {
	key, role string
	groups    map[string]int // responses by group, at any time
	timeline  []stamp        // responses in time order, at any time
}

type stamp struct {
	at    time.Time
	group string
}

type row struct {
	Row
	members   map[string]string // instance key -> role
	responses map[string]bool
}

func groupKey(provider, model, effort string) string {
	return provider + "\x00" + model + "\x00" + effort
}

func recordGroup(r Record) string {
	if r.Kind == KindSummary || r.Kind == KindTool {
		return groupKey("", "", "")
	}
	return groupKey(r.Provider, r.Model, r.Effort)
}

// Summarize counts the usage of the workspaces' agents of the roles asked,
// since the time asked, by provider, model and effort.
//
// Workers in a row are the distinct worker instances with usage in it; one
// that used several models is in several rows, and the total counts it
// once. A done task counts for the model its owner was using when it was
// marked done, when the board recorded that time; otherwise for the only
// model and effort its owner used, and for none when it used several.
func Summarize(spaces []Workspace, q Query) Summary {
	s := Summary{Rows: []Row{}, Coverage: Coverage{Errors: []string{}}}
	rows := map[string]*row{}
	get := func(group string) *row {
		r := rows[group]
		if r == nil {
			parts := strings.SplitN(group, "\x00", 3)
			r = &row{Row: Row{Provider: parts[0], Model: parts[1], Effort: parts[2]}, members: map[string]string{}, responses: map[string]bool{}}
			rows[group] = r
		}
		return r
	}
	for _, w := range spaces {
		s.Coverage.Workspaces++
		s.Coverage.Files += w.Stats.Files
		s.Coverage.Unreadable += w.Stats.Unreadable
		s.Coverage.Oversized += w.Stats.Oversized
		for _, e := range w.Stats.Errors {
			if len(s.Coverage.Errors) < maxErrors {
				s.Coverage.Errors = append(s.Coverage.Errors, w.Name+": "+e)
			}
		}
		starts := map[string][]time.Time{}
		for _, x := range w.Starts {
			starts[x.Name] = append(starts[x.Name], x.StartedAt)
		}
		for _, wk := range w.Workers {
			starts[wk.Name] = append(starts[wk.Name], wk.StartedAt)
		}
		for name, ts := range starts {
			slices.SortFunc(ts, time.Time.Compare)
			starts[name] = slices.CompactFunc(ts, time.Time.Equal)
		}
		instances := map[string]*instance{}
		find := func(agent string, at time.Time) *instance {
			key, role := w.Name+"/"+agent+"@unlisted", RoleWorker
			if agent == yesmen.LeadName {
				key, role = w.Name+"/lead@"+w.HiredAt.UTC().Format(time.RFC3339Nano), RoleLead
			} else if ts := starts[agent]; len(ts) > 0 {
				i := sort.Search(len(ts), func(i int) bool { return ts[i].After(at) })
				if i == 0 {
					key = w.Name + "/" + agent + "@earlier"
				} else {
					key = w.Name + "/" + agent + "@" + ts[i-1].UTC().Format(time.RFC3339Nano)
				}
			}
			in := instances[key]
			if in == nil {
				in = &instance{key: key, role: role, groups: map[string]int{}}
				instances[key] = in
			}
			return in
		}
		agents := make([]string, 0, len(w.Agents))
		for name := range w.Agents {
			agents = append(agents, name)
		}
		sort.Strings(agents)
		for _, agent := range agents {
			for _, r := range w.Agents[agent] {
				in := find(agent, r.At)
				group := recordGroup(r)
				if r.Kind == KindResponse {
					in.groups[group]++
					in.timeline = append(in.timeline, stamp{r.At, group})
				}
				if !q.counts(in.role) || !q.Since.IsZero() && r.At.Before(q.Since) {
					continue
				}
				x := get(group)
				x.add(r)
				x.members[in.key] = in.role
				s.Coverage.Records++
				if s.Coverage.From.IsZero() || r.At.Before(s.Coverage.From) {
					s.Coverage.From = r.At
				}
				if r.At.After(s.Coverage.To) {
					s.Coverage.To = r.At
				}
				if r.Kind == KindResponse && r.Effort == "" {
					s.Coverage.NoEffort++
				}
				if r.Model == "" {
					s.Coverage.NoModel++
				}
			}
		}
		for _, in := range instances {
			sort.SliceStable(in.timeline, func(i, j int) bool { return in.timeline[i].at.Before(in.timeline[j].at) })
		}
		for _, t := range w.Board.Tasks {
			if t.Status != yesmen.TaskDone {
				continue
			}
			done := t.DoneAt
			if done.IsZero() && !q.Since.IsZero() {
				s.Tasks.TimeUnknown++
				continue
			}
			if !done.IsZero() && done.Before(q.Since) {
				continue
			}
			s.Tasks.Done++
			if !done.IsZero() {
				s.Tasks.Recorded++
			}
			// The owner when it was done, if recorded; else the owner now.
			owner := t.DoneOwner
			if done.IsZero() {
				owner = t.Owner
			}
			_, hasLog := w.Agents[owner]
			switch {
			case owner == "":
				s.Tasks.Unowned++
				continue
			case owner != yesmen.LeadName && len(starts[owner]) == 0 && !hasLog:
				s.Tasks.UnknownOwner++
				continue
			}
			// The owner is the instance that held the name when the task was
			// done: at most when it last changed, without a recorded time.
			at := done
			if at.IsZero() {
				at = t.UpdatedAt
			}
			in := find(owner, at)
			if !q.counts(in.role) {
				s.Tasks.OtherRole++
				continue
			}
			switch group, why := in.attribute(done); why {
			case attributed:
				// Its owner counts among the model's agents, even when its
				// usage of it was before the period.
				x := get(group)
				x.Tasks++
				x.members[in.key] = in.role
				s.Tasks.Attributed++
			case severalModels:
				s.Tasks.SeveralModels++
			default:
				s.Tasks.NoResponses++
			}
		}
	}
	keys := make([]string, 0, len(rows))
	for k := range rows {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	type member struct {
		role string
		rows int
	}
	seen := map[string]*member{}
	total := Row{}
	for _, k := range keys {
		x := rows[k]
		for key, role := range x.members {
			if role == RoleLead {
				x.Leads++
			} else {
				x.Workers++
			}
			if seen[key] == nil {
				seen[key] = &member{role: role}
			}
			seen[key].rows++
		}
		x.ResponseModels = sortedKeys(x.responses)
		if x.TimedMs > 0 {
			speed := float64(x.TimedOutput) / (float64(x.TimedMs) / 1000)
			x.Speed = &speed
		}
		s.Rows = append(s.Rows, x.Row)
		total.Tasks += x.Tasks
		total.Responses += x.Responses
		total.Failed += x.Failed
		total.Other += x.Other
		total.Input += x.Input
		total.Output += x.Output
		total.CacheRead += x.CacheRead
		total.CacheWrite += x.CacheWrite
		total.Tokens.Total += x.Tokens.Total
		total.Timed += x.Timed
		total.Untimed += x.Untimed
		total.TimedOutput += x.TimedOutput
		total.TimedMs += x.TimedMs
		total.First, total.Last = earlier(total.First, x.First), later(total.Last, x.Last)
	}
	for _, m := range seen {
		if m.role == RoleLead {
			total.Leads++
			continue
		}
		total.Workers++
		if m.rows > 1 {
			s.Overlap++
		}
	}
	s.Total = total
	return s
}

// Outcomes of attributing a task to a model.
const (
	attributed = iota
	severalModels
	noResponses
)

// attribute names the group of the model the instance was using when a
// task was done: its last response by then, when that time is known; else
// the only group it used.
func (in *instance) attribute(done time.Time) (string, int) {
	if !done.IsZero() {
		i := sort.Search(len(in.timeline), func(i int) bool { return in.timeline[i].at.After(done) })
		if i == 0 {
			return "", noResponses
		}
		return in.timeline[i-1].group, attributed
	}
	switch len(in.groups) {
	case 0:
		return "", noResponses
	case 1:
		for g := range in.groups {
			return g, attributed
		}
	}
	return "", severalModels
}

func (x *row) add(r Record) {
	x.Input += r.Input
	x.Output += r.Output
	x.CacheRead += r.CacheRead
	x.CacheWrite += r.CacheWrite
	x.Tokens.Total += r.Tokens.Total
	x.First, x.Last = earlier(x.First, r.At), later(x.Last, r.At)
	if r.Kind != KindResponse {
		x.Other++
		return
	}
	x.Responses++
	if r.ResponseModel != "" {
		x.responses[r.ResponseModel] = true
	}
	switch {
	case failed(r.Stop):
		x.Failed++
	case completed(r.Stop) && r.DurationMs > 0:
		x.Timed++
		x.TimedOutput += r.Output
		x.TimedMs += r.DurationMs
	case completed(r.Stop):
		x.Untimed++
	}
}

func earlier(a, b time.Time) time.Time {
	if a.IsZero() || !b.IsZero() && b.Before(a) {
		return b
	}
	return a
}

func later(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

func sortedKeys(m map[string]bool) []string {
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}
