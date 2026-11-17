// Package restart keeps what an explicit restart of the app interrupted, so
// that the next instance can let that work continue.
//
// Restarting from the app (Restart and continue, or Restart to update)
// records an Intent just before the old instance stops its agents: which
// agents were in the middle of a run, with the identities that tell them from
// a later agent of the same name. The next instance claims it once, checks
// each agent against what the workspace holds now, and sends the agents that
// may go on a short note to continue from their session. Nothing else is
// kept: no credentials, no messages, no session text. A plain Quit records
// nothing, so starting the app never wakes agents on its own.
package restart

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Kinds of restart.
const (
	KindRestart = "restart" // Restart and continue: what is installed starts again
	KindUpdate  = "update"  // Restart to update: the installed update starts
)

// Reasons an agent is in an intent: what it was doing when the app restarted.
const (
	// ReasonWorking: it was running a turn.
	ReasonWorking = "working"
	// ReasonQuestion: a worker waited on a question to its lead, which the
	// restart cancelled.
	ReasonQuestion = "question"
	// ReasonReport: a worker had finished a run and its report was on its
	// way to the lead.
	ReasonReport = "report"
	// ReasonNeedsYou: it waited on the user's answer. It is not woken.
	ReasonNeedsYou = "needs-you"
)

// Outcomes of an agent after a restart.
const (
	Resumed  = "resumed"  // it was asked to continue, and pi took the note
	Reported = "reported" // its report was handed to the lead again
	Waiting  = "waiting"  // it waits for the user; not woken
	Active   = "active"   // someone talked to it first; not woken again
	Finished = "finished" // it had finished; nothing to continue
	Gone     = "gone"     // its workspace or worker is gone or was replaced
	Stopped  = "stopped"  // it was stopped, or the app quit, before it went on
	Failed   = "failed"   // waking it failed; it waits for a message
)

// Version is the format of the intent. A file of another version is
// refused, and kept.
const Version = 1

const (
	// MaxAge is how long an intent waits for the next instance.
	MaxAge = 15 * time.Minute
	// maxSkew tolerates a clock that moved back a little.
	maxSkew = 2 * time.Minute
	// maxAgents bounds the agents of one intent.
	maxAgents = 256
	// maxIntentBytes and maxResultBytes bound what is read.
	maxIntentBytes = 64 << 10
	maxResultBytes = 256 << 10
)

// Names in the restart directory.
const (
	intentName = "intent.json"
	lastName   = "last.json"
)

// Agent is an agent whose work a restart interrupted.
type Agent struct {
	Yesman string `json:"yesman"`
	Name   string `json:"name"`
	Role   string `json:"role"` // lead or worker
	// WorkspaceHiredAt is when its yesman was hired, and AgentSince when
	// the agent was (the lead: the same; a worker: when it started). They
	// tell it from a later workspace or worker of the same name.
	WorkspaceHiredAt time.Time `json:"workspaceHiredAt"`
	AgentSince       time.Time `json:"agentSince"`
	Reason           string    `json:"reason"`
}

// Intent is what a restart records for the next instance.
type Intent struct {
	Version     int       `json:"version"`
	ID          string    `json:"id"`
	Kind        string    `json:"kind"`
	CreatedAt   time.Time `json:"createdAt"`
	FromVersion string    `json:"fromVersion,omitempty"`
	// ToVersion is the update a restart to update starts.
	ToVersion string  `json:"toVersion,omitempty"`
	Agents    []Agent `json:"agents"`
}

// Outcome is what became of one agent of an intent.
type Outcome struct {
	Yesman  string `json:"yesman"`
	Name    string `json:"name"`
	Role    string `json:"role"`
	Reason  string `json:"reason"`
	Outcome string `json:"outcome"`
	Error   string `json:"error,omitempty"`
}

// Result is what the last restart came to, for the window.
type Result struct {
	ID          string    `json:"id,omitempty"`
	Kind        string    `json:"kind,omitempty"`
	RestartedAt time.Time `json:"restartedAt,omitzero"`
	ResumedAt   time.Time `json:"resumedAt"`
	FromVersion string    `json:"fromVersion,omitempty"`
	ToVersion   string    `json:"toVersion,omitempty"`
	// Version is the version that started after it.
	Version string    `json:"version,omitempty"`
	Agents  []Outcome `json:"agents"`
	// Rejected says why a recorded intent was not used; it was kept aside.
	Rejected string `json:"rejected,omitempty"`
}

// NewID returns a new intent ID.
func NewID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

var (
	idRE   = regexp.MustCompile(`^[a-f0-9]{32}$`)
	nameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`) // as yesmen.ValidName
	verRE  = regexp.MustCompile(`^[0-9A-Za-z.+-]{0,64}$`)
)

// Validate checks an intent as the next instance reads it, at now.
func (in Intent) Validate(now time.Time) error {
	switch {
	case in.Version != Version:
		return fmt.Errorf("version %d is not %d", in.Version, Version)
	case !idRE.MatchString(in.ID):
		return errors.New("its ID is malformed")
	case in.Kind != KindRestart && in.Kind != KindUpdate:
		return fmt.Errorf("unknown kind %q", in.Kind)
	case !verRE.MatchString(in.FromVersion) || !verRE.MatchString(in.ToVersion):
		return errors.New("a version is malformed")
	case in.CreatedAt.IsZero():
		return errors.New("it has no time")
	case in.CreatedAt.After(now.Add(maxSkew)):
		return errors.New("it was written in the future")
	case now.Sub(in.CreatedAt) > MaxAge:
		return fmt.Errorf("it is older than %s", MaxAge)
	case len(in.Agents) > maxAgents:
		return fmt.Errorf("it has more than %d agents", maxAgents)
	}
	seen := map[string]bool{}
	for _, a := range in.Agents {
		if !nameRE.MatchString(a.Yesman) || !nameRE.MatchString(a.Name) {
			return errors.New("an agent's name is malformed")
		}
		switch {
		case a.Role == "lead" && a.Name == "lead":
		case a.Role == "worker" && a.Name != "lead":
		default:
			return fmt.Errorf("%s/%s has an unknown role", a.Yesman, a.Name)
		}
		switch a.Reason {
		case ReasonWorking, ReasonNeedsYou:
		case ReasonQuestion, ReasonReport:
			if a.Role != "worker" {
				return fmt.Errorf("%s/%s: only a worker can be in for %s", a.Yesman, a.Name, a.Reason)
			}
		default:
			return fmt.Errorf("%s/%s: unknown reason %q", a.Yesman, a.Name, a.Reason)
		}
		if a.WorkspaceHiredAt.IsZero() || a.AgentSince.IsZero() || a.Role == "lead" && !a.AgentSince.Equal(a.WorkspaceHiredAt) {
			return fmt.Errorf("%s/%s has no identity", a.Yesman, a.Name)
		}
		key := a.Yesman + "/" + a.Name
		if seen[key] {
			return fmt.Errorf("%s is listed twice", key)
		}
		seen[key] = true
	}
	return nil
}

// Dir is the directory, in the app's home, that restarts are kept in.
type Dir struct {
	Path string
	// now is time.Now, for tests.
	now func() time.Time
}

// Open returns the restart directory at path. Nothing is created yet.
func Open(path string) *Dir { return &Dir{Path: path, now: time.Now} }

// root opens the directory, refusing anything but a real directory in its
// place; create makes it when it is missing.
func (d *Dir) root(create bool) (*os.Root, error) {
	fi, err := os.Lstat(d.Path)
	if errors.Is(err, fs.ErrNotExist) && create {
		if err := os.Mkdir(d.Path, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
			return nil, err
		}
		fi, err = os.Lstat(d.Path)
	}
	if err != nil {
		return nil, err
	}
	if !fi.IsDir() {
		return nil, fmt.Errorf("%s is not a directory (a link, file or device)", d.Path)
	}
	r, err := os.OpenRoot(d.Path)
	if err != nil {
		return nil, err
	}
	opened, err := r.Stat(".")
	if err != nil || !os.SameFile(fi, opened) {
		r.Close()
		return nil, fmt.Errorf("%s changed while it was opened", d.Path)
	}
	return r, nil
}

// Write records an intent, replacing one an earlier restart left. It is
// on disk, synced, when Write returns.
func (d *Dir) Write(in Intent) error {
	if err := in.Validate(in.CreatedAt); err != nil {
		return err
	}
	data, err := json.MarshalIndent(in, "", "  ")
	if err != nil {
		return err
	}
	if len(data)+1 > maxIntentBytes {
		return fmt.Errorf("the intent takes more than %d bytes", maxIntentBytes)
	}
	r, err := d.root(true)
	if err != nil {
		return err
	}
	defer r.Close()
	return writeAtomic(r, intentName, append(data, '\n'))
}

// Cancel removes the intent id, as a restart that did not happen leaves
// it. Another restart's intent stays.
func (d *Dir) Cancel(id string) error {
	r, err := d.root(false)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer r.Close()
	data, err := readRegular(r, intentName, maxIntentBytes)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var in Intent
	if json.Unmarshal(data, &in) != nil || in.ID != id {
		return nil
	}
	return r.Remove(intentName)
}

// Claim takes the intent a restart left, once: the file is moved aside
// before it is read, so that no later start sees it again. found is false
// when there was none. An intent that cannot be used is kept as
// rejected-<time>.json and reported in rejected; it wakes nobody.
func (d *Dir) Claim() (in Intent, claim string, found bool, rejected string, err error) {
	r, err := d.root(false)
	if errors.Is(err, fs.ErrNotExist) {
		return Intent{}, "", false, "", nil
	}
	if err != nil {
		return Intent{}, "", false, "", err
	}
	defer r.Close()
	if _, err := r.Lstat(intentName); errors.Is(err, fs.ErrNotExist) {
		return Intent{}, "", false, "", nil
	} else if err != nil {
		return Intent{}, "", false, "", err
	}
	now := d.now()
	stamp := now.UTC().Format("20060102T150405.000000000Z")
	claim = "claimed-" + stamp + ".json"
	// Renaming moves a link or a pipe as it is, without following it.
	if err := r.Rename(intentName, claim); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Intent{}, "", false, "", nil
		}
		return Intent{}, "", false, "", err
	}
	reject := func(why string) (Intent, string, bool, string, error) {
		aside := "rejected-" + stamp + ".json"
		if err := r.Rename(claim, aside); err != nil {
			return Intent{}, claim, true, why, err
		}
		return Intent{}, aside, true, why, nil
	}
	data, err := readRegular(r, claim, maxIntentBytes)
	if err != nil {
		return reject("unreadable: " + err.Error())
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		return reject("malformed: " + err.Error())
	}
	if dec.More() {
		return reject("malformed: trailing data")
	}
	if err := in.Validate(now); err != nil {
		return reject(err.Error())
	}
	return in, claim, true, "", nil
}

// Finish records what an intent came to and removes its claimed file.
func (d *Dir) Finish(claim string, res Result) error {
	r, err := d.root(true)
	if err != nil {
		return err
	}
	defer r.Close()
	data, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		return err
	}
	if len(data)+1 > maxResultBytes {
		return fmt.Errorf("the result takes more than %d bytes", maxResultBytes)
	}
	if err := writeAtomic(r, lastName, append(data, '\n')); err != nil {
		return err
	}
	if strings.HasPrefix(claim, "claimed-") {
		if err := r.Remove(claim); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return nil
}

// Last returns what the last restart came to; ok is false when there is
// none.
func (d *Dir) Last() (res Result, ok bool, err error) {
	r, err := d.root(false)
	if errors.Is(err, fs.ErrNotExist) {
		return Result{}, false, nil
	}
	if err != nil {
		return Result{}, false, err
	}
	defer r.Close()
	data, err := readRegular(r, lastName, maxResultBytes)
	if errors.Is(err, fs.ErrNotExist) {
		return Result{}, false, nil
	}
	if err != nil {
		return Result{}, false, err
	}
	if err := json.Unmarshal(data, &res); err != nil {
		return Result{}, false, err
	}
	return res, true, nil
}

// writeAtomic writes name in r through a new file of its own, synced, then
// renamed over name.
func writeAtomic(r *os.Root, name string, data []byte) error {
	tmp := "." + name + "." + NewID()[:12] + ".tmp"
	f, err := r.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
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
		err = r.Rename(tmp, name)
	}
	if err != nil {
		_ = r.Remove(tmp)
		return err
	}
	syncDir(r)
	return nil
}

// readRegular reads the regular file name in r, of at most limit bytes,
// opening it without blocking: a link, pipe, directory or device in its
// place, or one swapped in while it is opened, is refused unread.
func readRegular(r *os.Root, name string, limit int64) ([]byte, error) {
	fi, err := r.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, errors.New("not a regular file (a link, pipe, directory or device)")
	}
	if fi.Size() > limit {
		return nil, fmt.Errorf("larger than %d bytes", limit)
	}
	f, err := r.OpenFile(name, os.O_RDONLY|openNoBlock, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !opened.Mode().IsRegular() || !os.SameFile(fi, opened) {
		return nil, errors.New("it changed while it was opened")
	}
	if n, err := linkCount(opened); err != nil || n != 1 {
		return nil, errors.New("it has other links")
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("larger than %d bytes", limit)
	}
	return data, nil
}

// Path returns the restart directory of an app home.
func Path(home string) string { return filepath.Join(home, "restart") }
