package schedule

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/idy/yesman/internal/yesmen"
)

// Plan is one scheduled task: what to ask the lead and when. It is the
// configuration the user saves; its runs are records of their own.
type Plan struct {
	ID           string `json:"id"`
	Title        string `json:"title"`
	Instructions string `json:"instructions"`
	Schedule     Spec   `json:"schedule"`
	Timezone     string `json:"timezone"`
	Enabled      bool   `json:"enabled"`
	// Revision counts the saved changes, so that an edit made from an older
	// copy is refused instead of overwriting a newer one.
	Revision int `json:"revision"`
	// ActiveSince is when the plan's current schedule took effect, by a
	// save, or by turning it on: nothing before it is due.
	ActiveSince time.Time `json:"activeSince,omitzero"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
	ArchivedAt  time.Time `json:"archivedAt,omitzero"`
	// CreatedBy is "user", or "lead" for a draft the lead saved at the
	// user's request; drafts stay off until the user turns them on.
	CreatedBy string `json:"createdBy,omitempty"`
}

// Archived reports whether the plan was archived.
func (p Plan) Archived() bool { return !p.ArchivedAt.IsZero() }

// Run states. claimed, boarded and sending are on the way to the lead;
// sent and queued were accepted by pi; failed was definitely not delivered;
// unknown cannot be confirmed either way. missed and skipped record
// occurrences that did not run.
const (
	StateClaimed = "claimed"
	StateBoarded = "boarded"
	StateSending = "sending"
	StateSent    = "sent"
	StateQueued  = "queued"
	StateFailed  = "failed"
	StateUnknown = "unknown"
	StateMissed  = "missed"
	StateSkipped = "skipped"
	StateDamaged = "damaged" // a record that cannot be read; its occurrence counts as handled
)

// Run kinds.
const (
	RunScheduled = "scheduled"
	RunManual    = "manual"
	RunCatchUp   = "catchup"
	RunMissed    = "missed"
	RunSkipped   = "skipped"
)

// Run is one record of a plan: a run sent to the lead, or occurrences that
// were missed or skipped.
type Run struct {
	ID       string `json:"id"`
	Plan     string `json:"plan"`
	Revision int    `json:"revision"`
	Kind     string `json:"kind"`
	// Key and At name the occurrence: its key and the instant it was due.
	// A manual run's At is when it was asked for.
	Key string    `json:"key,omitempty"`
	At  time.Time `json:"at"`
	// A missed record covers Count occurrences from First to Last.
	First       time.Time `json:"first,omitzero"`
	Last        time.Time `json:"last,omitzero"`
	Count       int       `json:"count,omitempty"`
	CountCapped bool      `json:"countCapped,omitempty"`
	// Due is when the on-time window of its delivery starts: its occurrence,
	// or when the user asked for it.
	Due       time.Time `json:"due"`
	ClaimedAt time.Time `json:"claimedAt"`
	UpdatedAt time.Time `json:"updatedAt"`
	State     string    `json:"state"`
	Reason    string    `json:"reason,omitempty"`
	// Board is the ID of the board task that tracks the run.
	Board    string    `json:"board,omitempty"`
	Attempts []Attempt `json:"attempts,omitempty"`
	// Outcome is its board task's status as last seen.
	Outcome *Outcome `json:"outcome,omitempty"`
	// What was asked, as it was when the run was claimed.
	Title        string `json:"title"`
	Instructions string `json:"instructions,omitempty"`
	Schedule     Spec   `json:"schedule"`
	Timezone     string `json:"timezone,omitempty"`
	// Agent and AgentSince identify the lead it was sent to.
	Agent      string    `json:"agent,omitempty"`
	AgentSince time.Time `json:"agentSince,omitzero"`
	// From is the missed record a catch-up runs for; CaughtUp is the run
	// that caught a missed record up; Prev is the open run a skip waited on.
	From     string `json:"from,omitempty"`
	CaughtUp string `json:"caughtUp,omitempty"`
	Prev     string `json:"prev,omitempty"`
	// Claimer is the scheduler instance that claimed the run, or retried it.
	Claimer string `json:"claimer,omitempty"`
	// BoardSync is a change of its board task still to be made (boardSync).
	BoardSync *BoardSync `json:"boardSync,omitempty"`
}

// BoardSync is a change of a run's board task that its change of state
// calls for: blocked with a note while it was definitely not delivered, and
// back to do when it is sent again. It is recorded with the run in the same
// write as that change of state, then made on the board, and made again at
// each tick until the board has it, so that neither a board write that
// failed nor a quit in between leaves the lead a task it never got. It is
// made only to the task as it stood when it was decided: a task the lead or
// the user changed or removed since is left as they left it.
type BoardSync struct {
	Status string `json:"status"`
	Note   string `json:"note"`
	// From is the task as it stood, nil when the board could not be read
	// then: the change is then made only to a task in the state it calls
	// for (to do, or blocked by the scheduler) not changed after At.
	From *TaskStamp `json:"from,omitempty"`
	At   time.Time  `json:"at"` // the board's clock, when it was decided
}

// TaskStamp tells a board task's state: its status, a digest of its note,
// and when the board last changed it. Any change of the task changes it.
type TaskStamp struct {
	Status    string    `json:"status"`
	NoteSum   string    `json:"noteSum"`
	UpdatedAt time.Time `json:"updatedAt"`
}

func stampOf(t yesmen.Task) *TaskStamp {
	sum := sha256.Sum256([]byte(t.Note))
	return &TaskStamp{Status: t.Status, NoteSum: hex.EncodeToString(sum[:]), UpdatedAt: t.UpdatedAt}
}

// Attempt is one submission of a run to the lead, under its own stable
// submission ID.
type Attempt struct {
	ID    string    `json:"id"`
	At    time.Time `json:"at"`
	State string    `json:"state"` // the receipt's state, or "" before it has one
	// Rev is the revision of the receipt last applied: an older copy of it,
	// read before a newer one arrived, never takes its place.
	Rev   uint64 `json:"rev,omitempty"`
	Error string `json:"error,omitempty"`
	// Definite: it was proven not to reach pi, so a new ID may be used.
	Definite bool `json:"definite,omitempty"`
}

// Outcome is a run's board task as last seen.
type Outcome struct {
	Status string    `json:"status"` // a board status, or "removed"
	Note   string    `json:"note,omitempty"`
	At     time.Time `json:"at"`
	// Removed: the board no longer has the task; Status is how it last stood.
	Removed bool `json:"removed,omitempty"`
}

// inFlight reports whether a run is still on its way to the lead.
func (r Run) inFlight() bool {
	return r.State == StateClaimed || r.State == StateBoarded || r.State == StateSending
}

// waiting reports whether the run, as recorded, waits in the lead's queue.
// After a quit its receipt may say the queue was lost, so such a run is
// checked again. (A run pi accepted is sent, whatever came of it after.)
func (r Run) waiting() bool {
	a := r.attempt()
	return a != nil && r.State == StateQueued && (a.State == "queued" || a.State == "sending")
}

// retained reports whether a record stays in the history whatever its age:
// one on its way, one that cannot be read, and a run whose board task is
// still open (or a board that cannot be read). Once its board task is done,
// dropped or removed, a run is history like any other, also one whose
// delivery stayed unknown: the lead, or the user, settled it.
func retained(r Run, tasks map[string]yesmen.Task, boardErr error) bool {
	switch {
	case r.State == StateDamaged, r.inFlight():
		return true
	case !r.fired():
		return false
	case boardErr != nil:
		return true
	}
	t, ok := taskOf(r, tasks)
	return ok && t.Open()
}

// fired reports whether the record is a run sent, or to be sent, to the lead.
func (r Run) fired() bool {
	return r.Kind == RunScheduled || r.Kind == RunManual || r.Kind == RunCatchUp
}

// latest is the last instant a record covers.
func (r Run) latest() time.Time {
	if !r.Last.IsZero() {
		return r.Last
	}
	return r.At
}

func (r Run) attempt() *Attempt {
	if len(r.Attempts) == 0 {
		return nil
	}
	return &r.Attempts[len(r.Attempts)-1]
}

// Limits of what a workspace keeps.
const (
	MaxTitle        = 120
	MaxInstructions = 4000
	MaxActivePlans  = 50
	maxPlans        = 200 // with archived ones
	keepRuns        = 30  // closed records per plan, besides the retained ones
	maxOpenRuns     = 100 // retained runs of a plan before new runs wait for some to close
	maxRunsPerPlan  = 60  // records of a plan a view lists, the newest; it counts the rest
	// maxPlanBytes bounds one plan as plans.json keeps it. Its title and
	// instructions at their longest, every character one JSON writes as six
	// bytes ("<" or U+2028, say), take 24,720 bytes; its other fields,
	// indented, under 1 KiB. TestWorstPlanFitsItsShare checks it.
	maxPlanBytes = 32 << 10
	// maxPlansFile is the most of plans.json yesman reads, and so the most
	// it writes: maxPlans plans at their largest, with room for the file's
	// own fields. A change that would make the file larger is refused before
	// anything is written, so every file yesman saved reads back.
	maxPlansFile = maxPlans*maxPlanBytes + 64<<10
	// maxRunFile is the most of a run record yesman reads and writes; one at
	// its largest (its plan's text, maxAttempts errors, a note) is about
	// 40 KiB.
	maxRunFile  = 64 << 10
	maxAttempts = 5
)

// plansFile is .yesman/schedules/plans.json. Fields this version does not
// know, on the file or on a plan, are kept as they are.
type plansFile struct {
	Version int    `json:"version"`
	Next    int    `json:"next"`
	Plans   []Plan `json:"plans"`

	extra      map[string]json.RawMessage
	planExtras map[string]map[string]json.RawMessage
}

const plansVersion = 1

func schedulesDir(y yesmen.Yesman) string { return filepath.Join(y.MetaDir(), "schedules") }
func plansPath(y yesmen.Yesman) string    { return filepath.Join(schedulesDir(y), "plans.json") }
func runsDir(y yesmen.Yesman) string      { return filepath.Join(schedulesDir(y), "runs") }

var runIDRE = regexp.MustCompile(`^s[0-9]{1,6}-[a-z0-9-]{1,80}$`)

func runPath(y yesmen.Yesman, id string) string { return filepath.Join(runsDir(y), id+".json") }

// knownKeys lists the JSON names of a struct's fields.
func knownKeys(v any) []string {
	t := reflect.TypeOf(v)
	var keys []string
	for i := range t.NumField() {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name == "" {
			name = f.Name
		}
		if name != "-" {
			keys = append(keys, name)
		}
	}
	return keys
}

var (
	planKeys = knownKeys(Plan{})
	runKeys  = knownKeys(Run{})
	fileKeys = []string{"version", "next", "plans"}
)

// unknown returns the members of a JSON object that are not known keys.
func unknown(raw json.RawMessage, known []string) (map[string]json.RawMessage, error) {
	var all map[string]json.RawMessage
	if err := json.Unmarshal(raw, &all); err != nil {
		return nil, err
	}
	if all == nil {
		return nil, errors.New("not a JSON object")
	}
	for _, k := range known {
		delete(all, k)
	}
	if len(all) == 0 {
		return nil, nil
	}
	return all, nil
}

// withExtra encodes v as a JSON object with extra members it lacks.
func withExtra(v any, extra map[string]json.RawMessage) (json.RawMessage, error) {
	data, err := json.Marshal(v)
	if err != nil || len(extra) == 0 {
		return data, err
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(data, &obj); err != nil {
		return nil, err
	}
	for k, val := range extra {
		if _, ok := obj[k]; !ok {
			obj[k] = val
		}
	}
	return json.Marshal(obj)
}

func decodePlans(data []byte) (plansFile, error) {
	var f plansFile
	var raw struct {
		Version int               `json:"version"`
		Next    int               `json:"next"`
		Plans   []json.RawMessage `json:"plans"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return f, err
	}
	extra, err := unknown(data, fileKeys)
	if err != nil {
		return f, err
	}
	if raw.Version != plansVersion {
		return f, fmt.Errorf("version %d of the tasks file is not one this yesman reads", raw.Version)
	}
	f = plansFile{Version: raw.Version, Next: raw.Next, extra: extra, planExtras: map[string]map[string]json.RawMessage{}}
	seen := map[string]bool{}
	for _, item := range raw.Plans {
		var p Plan
		if err := json.Unmarshal(item, &p); err != nil {
			return f, fmt.Errorf("a task cannot be read: %w", err)
		}
		if !planIDRE.MatchString(p.ID) || seen[p.ID] {
			return f, fmt.Errorf("a task has a missing or repeated ID %q", p.ID)
		}
		seen[p.ID] = true
		if n := planNumber(p.ID); n > f.Next {
			f.Next = n
		}
		x, err := unknown(item, planKeys)
		if err != nil {
			return f, err
		}
		if x != nil {
			f.planExtras[p.ID] = x
		}
		f.Plans = append(f.Plans, p)
	}
	return f, nil
}

func (f plansFile) encode() ([]byte, error) {
	plans := make([]json.RawMessage, 0, len(f.Plans))
	for _, p := range f.Plans {
		data, err := withExtra(p, f.planExtras[p.ID])
		if err != nil {
			return nil, err
		}
		plans = append(plans, data)
	}
	data, err := withExtra(struct {
		Version int               `json:"version"`
		Next    int               `json:"next"`
		Plans   []json.RawMessage `json:"plans"`
	}{plansVersion, f.Next, plans}, f.extra)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	if err := json.Indent(&out, data, "", "  "); err != nil {
		return nil, err
	}
	out.WriteByte('\n')
	return out.Bytes(), nil
}

var planIDRE = regexp.MustCompile(`^s[1-9][0-9]{0,5}$`)

func planNumber(id string) int {
	n := 0
	for _, c := range strings.TrimPrefix(id, "s") {
		n = n*10 + int(c-'0')
	}
	return n
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// record is a run as kept in memory, with the fields of its file this
// version does not know.
type record struct {
	Run
	extra map[string]json.RawMessage
}

func encodeRun(r record) ([]byte, error) {
	data, err := withExtra(r.Run, r.extra)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	if err := json.Indent(&out, data, "", "  "); err != nil {
		return nil, err
	}
	out.WriteByte('\n')
	return out.Bytes(), nil
}

// plansOf returns a plan's records, newest first.
func plansRuns(runs map[string]*record, plan string) []*record {
	var out []*record
	for _, r := range runs {
		if r.Plan == plan {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if !a.ClaimedAt.Equal(b.ClaimedAt) {
			return a.ClaimedAt.After(b.ClaimedAt)
		}
		return a.ID > b.ID
	})
	return out
}

// prune lists records of a plan to remove: closed ones older than the
// newest keepRuns. A record that must stay (keep) is never removed, however
// many there are: new runs wait instead (maxOpenRuns).
func prune(list []*record, keep func(*record) bool) []*record {
	var out []*record
	kept := 0
	for i, r := range list {
		switch {
		case keep(r):
		case i == 0, kept < keepRuns:
			kept++
		default:
			out = append(out, r)
		}
	}
	return out
}

// ---------- What pruning removed ----------

// prunedFile is .yesman/schedules/pruned.json: what the history must still
// know of the records pruning removed. A manual run is named by the request
// ID of the click that asked for it; once its record is gone, the same
// request repeated must not pass for a new one. Plans hold the request IDs
// removed, the newest maxPrunedRequests of each plan exactly, and below them
// a floor: request IDs start with the time they were made (12 hex digits of
// milliseconds), and one made at or before the floor is refused.
type prunedFile struct {
	Version int                    `json:"version"`
	Plans   map[string]*prunedPlan `json:"plans"`
	extra   map[string]json.RawMessage
}

type prunedPlan struct {
	Requests []string `json:"requests,omitempty"` // oldest first
	Floor    string   `json:"floor,omitempty"`
}

const (
	prunedVersion     = 1
	maxPrunedRequests = 64
	// maxAheadRequests bounds the request IDs kept beyond maxPrunedRequests
	// because the floor may not cover them yet (prunedFile.add).
	maxAheadRequests = 32
	maxPrunedFile    = 1 << 20
)

var prunedKeys = []string{"version", "plans"}

func prunedPath(y yesmen.Yesman) string { return filepath.Join(schedulesDir(y), "pruned.json") }

func decodePruned(data []byte) (prunedFile, error) {
	f := prunedFile{Version: prunedVersion, Plans: map[string]*prunedPlan{}}
	if data == nil {
		return f, nil
	}
	if err := json.Unmarshal(data, &f); err != nil {
		return f, err
	}
	if f.Version != prunedVersion {
		return f, fmt.Errorf("version %d of pruned.json is not one this yesman reads", f.Version)
	}
	extra, err := unknown(data, prunedKeys)
	if err != nil {
		return f, err
	}
	f.extra = extra
	if f.Plans == nil {
		f.Plans = map[string]*prunedPlan{}
	}
	for id, p := range f.Plans {
		if !planIDRE.MatchString(id) || p == nil {
			return f, fmt.Errorf("pruned.json has an invalid task %q", id)
		}
		if p.Floor != "" && !floorRE.MatchString(p.Floor) {
			return f, fmt.Errorf("pruned.json has an invalid floor for %s", id)
		}
		for _, r := range p.Requests {
			if !requestRE.MatchString(r) {
				return f, fmt.Errorf("pruned.json has an invalid request ID for %s", id)
			}
		}
	}
	return f, nil
}

func (f prunedFile) encode() ([]byte, error) {
	data, err := withExtra(struct {
		Version int                    `json:"version"`
		Plans   map[string]*prunedPlan `json:"plans"`
	}{prunedVersion, f.Plans}, f.extra)
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

var floorRE = regexp.MustCompile(`^[a-f0-9]{12}$`)

// requestTime is the time part of a request ID, comparable as a string.
func requestTime(id string) string { return id[:12] }

// requestClock reads a request time as an instant.
func requestClock(t string) time.Time {
	var ms int64
	if _, err := fmt.Sscanf(t, "%x", &ms); err != nil {
		return time.Time{}
	}
	return time.UnixMilli(ms).UTC()
}

// requestAhead is how far ahead of the scheduler's clock a request ID's
// time may be: the page and the scheduler read the same computer's clock.
const requestAhead = 24 * time.Hour

// latestRequest is the latest request time taken from now: the latest a
// new click may carry, and the highest the floor is raised to while the
// plan keeps few enough IDs ahead of the clock.
func latestRequest(now time.Time) string {
	return fmt.Sprintf("%012x", now.Add(requestAhead).UnixMilli())
}

// clone copies the file deeply enough to change it.
func (f prunedFile) clone() prunedFile {
	out := prunedFile{Version: f.Version, Plans: make(map[string]*prunedPlan, len(f.Plans)), extra: f.extra}
	for id, p := range f.Plans {
		out.Plans[id] = &prunedPlan{Requests: slices.Clone(p.Requests), Floor: p.Floor}
	}
	return out
}

// used reports whether a plan's request ID was pruned, or may have been.
func (f prunedFile) used(plan, request string) bool {
	p := f.Plans[plan]
	if p == nil {
		return false
	}
	return slices.Contains(p.Requests, request) || p.Floor != "" && requestTime(request) <= p.Floor
}

// add records request IDs pruned from a plan, keeping the newest
// maxPrunedRequests exactly and raising the floor over the older ones, the
// oldest first. A request ID leaves the exact list only when the floor
// covers it, so no request ID taken is ever forgotten. The floor is not
// raised past latest (latestRequest) while it can be helped, or no new
// click could pass it: an ID whose time is later (taken before the clock
// was set back, or from a version whose request IDs did not start with
// their time) stays in the list until the clock reaches it, up to
// maxAheadRequests of them. Past that, the floor covers the earliest of
// them all the same, and Run now of the plan waits until the clock passes it
// (ErrRunNowWaits): refused, never sent twice. A plan keeps at most
// maxPrunedRequests+maxAheadRequests IDs, so pruned.json stays within the
// size it is read at (TestPrunedFileFitsItsLimit).
func (f *prunedFile) add(plan string, requests []string, latest string) {
	p := f.Plans[plan]
	if p == nil {
		p = &prunedPlan{}
		f.Plans[plan] = p
	}
	for _, r := range requests {
		if !slices.Contains(p.Requests, r) {
			p.Requests = append(p.Requests, r)
		}
	}
	for len(p.Requests) > maxPrunedRequests {
		i := slices.IndexFunc(p.Requests, func(r string) bool { return requestTime(r) <= latest })
		if i < 0 {
			if len(p.Requests) <= maxPrunedRequests+maxAheadRequests {
				break // none the floor may cover yet: all stay exactly
			}
			// Too many ahead of the clock: the earliest goes under the
			// floor, which so moves past the clock as little as it can.
			i = 0
			for j, r := range p.Requests {
				if requestTime(r) < requestTime(p.Requests[i]) {
					i = j
				}
			}
		}
		if t := requestTime(p.Requests[i]); t > p.Floor {
			p.Floor = t
		}
		p.Requests = slices.Delete(p.Requests, i, i+1)
	}
}

func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// archivedPlans lists the archived plans, oldest archive first.
func archivedPlans(plans []Plan) []Plan {
	var out []Plan
	for _, p := range plans {
		if p.Archived() {
			out = append(out, p)
		}
	}
	slices.SortFunc(out, func(a, b Plan) int {
		if c := a.ArchivedAt.Compare(b.ArchivedAt); c != 0 {
			return c
		}
		return planNumber(a.ID) - planNumber(b.ID)
	})
	return out
}
