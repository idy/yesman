package schedule

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
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
}

// Attempt is one submission of a run to the lead, under its own stable
// submission ID.
type Attempt struct {
	ID    string    `json:"id"`
	At    time.Time `json:"at"`
	State string    `json:"state"` // the receipt's state, or "" before it has one
	Error string    `json:"error,omitempty"`
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
	keepRuns        = 30  // records per plan, besides runs still under way
	maxRunsPerPlan  = 60
	maxPlansFile    = 1 << 20
	maxRunFile      = 64 << 10
	maxAttempts     = 5
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

// errUnsafePath reports a path yesman will not write through.
type errUnsafePath struct{ path string }

func (e errUnsafePath) Error() string {
	return fmt.Sprintf("%s is a link or not a regular file or folder, so yesman left it unchanged", e.path)
}

// safeDir makes sure dir is a real directory, creating it when missing. A
// link or a file at its path is left alone.
func safeDir(dir string) error {
	info, err := os.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.Mkdir(dir, 0o755); err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
		info, err = os.Lstat(dir)
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errUnsafePath{dir}
	}
	return nil
}

// readLimited reads a regular file of at most limit bytes. A missing file
// reads as nil.
func readLimited(path string, limit int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errUnsafePath{path}
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%s is larger than %d KiB, so yesman left it unchanged", filepath.Base(path), limit>>10)
	}
	return data, nil
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// writeFile replaces a file atomically, unless something other than a
// regular file is at its path.
func writeFile(path string, data []byte) error {
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
		return errUnsafePath{path}
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	tmp := path + "." + randomHex(4) + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		_ = os.Remove(tmp)
	}
	return err
}

// errClaimed: the record already exists, made by another tick, process or
// click.
var errClaimed = errors.New("already claimed")

// createFile creates a file that must not exist yet: the claim of a run.
func createFile(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if errors.Is(err, os.ErrExist) {
		return errClaimed
	}
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		// The claim was ours; without its content it must not stand. If it
		// cannot be removed, it reads as damaged and its occurrence as handled.
		_ = os.Remove(path)
	}
	return err
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

// readRuns reads every run record of a workspace. A record that cannot be
// read stays on disk and reads as damaged: its occurrence counts as handled
// and it is never sent.
func readRuns(dir string) (map[string]*record, error) {
	runs := map[string]*record{}
	info, err := os.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) {
		return runs, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errUnsafePath{dir}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		name, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok || !runIDRE.MatchString(name) {
			continue // temporary files and anything else
		}
		plan, _, _ := strings.Cut(name, "-")
		data, err := readLimited(filepath.Join(dir, e.Name()), maxRunFile)
		r := &record{}
		if err == nil {
			err = json.Unmarshal(data, &r.Run)
		}
		if err == nil && (r.ID != name || r.Plan != plan) {
			err = errors.New("its ID does not match its file")
		}
		if err == nil {
			r.extra, err = unknown(data, runKeys)
		}
		if err != nil {
			r = &record{Run: Run{ID: name, Plan: plan, Kind: RunScheduled, State: StateDamaged, Reason: "This record cannot be read: " + clip(err.Error(), 200)}}
		}
		runs[name] = r
	}
	return runs, nil
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

// prune lists records of a plan to remove: the oldest beyond keepRuns, never
// one still on its way, one whose board task is open, or the newest.
func prune(list []*record, open func(*record) bool) []*record {
	var out []*record
	kept := 0
	for i, r := range list {
		switch {
		case i == 0, r.inFlight(), r.State == StateDamaged:
			kept++
		case kept < keepRuns:
			kept++
		case open(r) && kept < maxRunsPerPlan:
			kept++
		default:
			out = append(out, r)
		}
	}
	return out
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
	slices.SortFunc(out, func(a, b Plan) int { return a.ArchivedAt.Compare(b.ArchivedAt) })
	return out
}
