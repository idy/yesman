package yesmen

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Topics are a yesman's questions and discussions worth keeping: what the user
// asked or discussed with the lead, distilled into a short summary, the
// conclusions the user confirmed and the questions still open. They live in
// .yesman/topics.json, beside the board. Task status stays on the board,
// task history in the task log and durable rules in Knowledge; a topic links
// to those instead of copying them.
//
// The file is bounded, read and written whole under the workspace's use lease
// (so a dismissal waits for a write and nothing recreates an archived
// workspace), replaced atomically, and never overwritten when it cannot be
// read. Fields this version does not know are kept, at the file and topic
// level, across writes.

// Topic statuses.
const (
	TopicOpen     = "open"
	TopicResolved = "resolved"
	TopicArchived = "archived"
)

// Who changed a topic.
const (
	TopicByUser = "user"
	TopicByLead = "lead"
)

// Limits keep the file small enough to read whole and to show at once.
const (
	maxTopics           = 300
	maxTopicTitle       = 120
	maxTopicSummary     = 4000
	maxTopicItems       = 30
	maxTopicItemText    = 1000
	maxTopicSources     = 30
	maxTopicSends       = 50
	maxTopicLabel       = 200
	maxTopicRef         = 500
	maxTopicSendText    = 512
	maxTopicsFile       = 4 << 20
	maxTopicsFileRead   = 8 << 20
	topicsFileVersion   = 1
	topicMarkerPrefix   = "[Topic "
	topicMarkerTitleCap = 120
)

var (
	topicIDRE      = regexp.MustCompile(`^tp[1-9][0-9]{0,5}$`)
	topicItemIDRE  = regexp.MustCompile(`^[cqs][1-9][0-9]{0,5}$`)
	topicSendIDRE  = regexp.MustCompile(`^[a-f0-9]{48}$`)
	topicTaskIDRE  = regexp.MustCompile(`^t[1-9][0-9]{0,5}$`)
	topicSendState = regexp.MustCompile(`^[a-z]{1,20}$`)
)

// TopicStatuses lists the statuses a topic can have.
var TopicStatuses = []string{TopicOpen, TopicResolved, TopicArchived}

// Source kinds: where a topic's content came from.
const (
	SourceMessage      = "message"      // one native message of an agent's conversation: role + pi timestamp
	SourceSubmission   = "submission"   // a message sent from this topic, by its submission ID
	SourceTask         = "task"         // a board task
	SourceWorker       = "worker"       // a worker's reports, cited by the lead; not a user question
	SourceConversation = "conversation" // the conversation as a whole, when no exact message is known
	SourceKnowledge    = "knowledge"    // a Knowledge note
	SourceLink         = "link"         // a web page, such as a pull request
)

var sourceKinds = []string{SourceMessage, SourceSubmission, SourceTask, SourceWorker, SourceConversation, SourceKnowledge, SourceLink}

// TopicItem is one conclusion or open question.
type TopicItem struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

// TopicSource says where something in a topic came from. Names are routing
// identities; Label is only shown.
type TopicSource struct {
	ID        string    `json:"id"`
	Kind      string    `json:"kind"`
	Agent     string    `json:"agent,omitempty"`
	Since     time.Time `json:"since,omitzero"`
	Role      string    `json:"role,omitempty"`
	Timestamp int64     `json:"timestamp,omitempty"`
	Ref       string    `json:"ref,omitempty"`
	Label     string    `json:"label,omitempty"`
}

// TopicSend is a message the user sent to the lead from a topic. It is app
// metadata written before the message is dispatched, keyed by its submission
// ID; it never claims the lead read or answered it.
type TopicSend struct {
	ID         string    `json:"id"`
	At         time.Time `json:"at"`
	Preview    string    `json:"preview,omitempty"`
	Truncated  bool      `json:"truncated,omitempty"`
	ImageCount int       `json:"imageCount,omitempty"`
	// Context is the visible line sent before the user's text, frozen at the
	// first attempt so a retry sends the same message. Empty for commands.
	Context    string    `json:"context,omitempty"`
	State      string    `json:"state,omitempty"`
	AgentSince time.Time `json:"agentSince,omitzero"`
}

// Topic is one question or discussion.
type Topic struct {
	ID          string        `json:"id"`
	Title       string        `json:"title"`
	Status      string        `json:"status"`
	Summary     string        `json:"summary,omitempty"`
	Conclusions []TopicItem   `json:"conclusions"`
	Questions   []TopicItem   `json:"questions"`
	Sources     []TopicSource `json:"sources"`
	Sends       []TopicSend   `json:"sends"`
	// Revision counts changes to what the topic says, for compare-and-set.
	// Sends do not change it, so sending never conflicts with an edit.
	Revision     int64     `json:"revision"`
	ArchivedFrom string    `json:"archivedFrom,omitempty"`
	NextItem     int       `json:"nextItem"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
	CreatedBy    string    `json:"createdBy,omitempty"`
	UpdatedBy    string    `json:"updatedBy,omitempty"`

	extra map[string]json.RawMessage
}

var topicKeys = []string{"id", "title", "status", "summary", "conclusions", "questions", "sources", "sends", "revision", "archivedFrom", "nextItem", "createdAt", "updatedAt", "createdBy", "updatedBy"}

type topicAlias Topic

func (t *Topic) UnmarshalJSON(data []byte) error {
	var a topicAlias
	if err := json.Unmarshal(data, &a); err != nil {
		return err
	}
	extra, err := unknownKeys(data, topicKeys)
	if err != nil {
		return err
	}
	*t = Topic(a)
	t.extra = extra
	return nil
}

func (t Topic) MarshalJSON() ([]byte, error) {
	return withUnknown(topicAlias(t), t.extra)
}

// Clone copies a topic so callers cannot change the stored one.
func (t Topic) clone() Topic {
	t.Conclusions = slices.Clone(t.Conclusions)
	t.Questions = slices.Clone(t.Questions)
	t.Sources = slices.Clone(t.Sources)
	t.Sends = slices.Clone(t.Sends)
	return t
}

func (t *Topic) normalizeLists() {
	if t.Conclusions == nil {
		t.Conclusions = []TopicItem{}
	}
	if t.Questions == nil {
		t.Questions = []TopicItem{}
	}
	if t.Sources == nil {
		t.Sources = []TopicSource{}
	}
	if t.Sends == nil {
		t.Sends = []TopicSend{}
	}
}

type topicsFile struct {
	Version int     `json:"version"`
	Next    int     `json:"next"`
	Topics  []Topic `json:"topics"`
	extra   map[string]json.RawMessage
}

var topicsFileKeys = []string{"version", "next", "topics"}

type topicsFileAlias topicsFile

func (f *topicsFile) UnmarshalJSON(data []byte) error {
	var a topicsFileAlias
	if err := json.Unmarshal(data, &a); err != nil {
		return err
	}
	extra, err := unknownKeys(data, topicsFileKeys)
	if err != nil {
		return err
	}
	*f = topicsFile(a)
	f.extra = extra
	return nil
}

func (f topicsFile) MarshalJSON() ([]byte, error) {
	return withUnknown(topicsFileAlias(f), f.extra)
}

func unknownKeys(data []byte, known []string) (map[string]json.RawMessage, error) {
	all := map[string]json.RawMessage{}
	if err := json.Unmarshal(data, &all); err != nil {
		return nil, err
	}
	for _, k := range known {
		delete(all, k)
	}
	if len(all) == 0 {
		return nil, nil
	}
	return all, nil
}

func withUnknown(v any, extra map[string]json.RawMessage) ([]byte, error) {
	data, err := json.Marshal(v)
	if err != nil || len(extra) == 0 {
		return data, err
	}
	all := map[string]json.RawMessage{}
	if err := json.Unmarshal(data, &all); err != nil {
		return nil, err
	}
	for k, raw := range extra {
		if _, known := all[k]; !known {
			all[k] = raw
		}
	}
	return json.Marshal(all)
}

// TopicsFile is where a yesman's topics are kept.
func (y Yesman) TopicsFile() string { return filepath.Join(y.MetaDir(), "topics.json") }

// TopicID reads a topic ID as people write it: tp3, TP3 or #tp3.
func TopicID(s string) string {
	return strings.ToLower(strings.TrimPrefix(strings.TrimSpace(s), "#"))
}

// ValidTopicID reports whether an ID can name a topic. IDs are never paths.
func ValidTopicID(id string) error {
	if !topicIDRE.MatchString(id) {
		return fmt.Errorf("%q is not a topic ID: topics have IDs such as tp3", id)
	}
	return nil
}

// TopicConflictError reports a compare-and-set that lost to another change.
// Nothing was written; Current is the topic as it is now.
type TopicConflictError struct {
	Current Topic
}

func (e *TopicConflictError) Error() string {
	return fmt.Sprintf("topic %s changed since revision you edited; it is at revision %d now: read it again and apply your change to that", e.Current.ID, e.Current.Revision)
}

// ErrTopicArchived is returned when a message is sent to an archived topic.
var ErrTopicArchived = errors.New("this topic is archived: restore it before sending a message about it")

// TopicInput creates a topic.
type TopicInput struct {
	Title       string        `json:"title"`
	Summary     string        `json:"summary,omitempty"`
	Status      string        `json:"status,omitempty"`
	Conclusions []string      `json:"conclusions,omitempty"`
	Questions   []string      `json:"questions,omitempty"`
	Sources     []TopicSource `json:"sources,omitempty"`
}

// TopicItemEdit changes one item's text.
type TopicItemEdit struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

// TopicPatch changes the fields it sets of a topic at Revision.
type TopicPatch struct {
	Revision int64   `json:"revision"`
	Title    *string `json:"title,omitempty"`
	Summary  *string `json:"summary,omitempty"`
	// Status is open or resolved; archiving and restoring are Archive and Restore.
	Status *string `json:"status,omitempty"`

	// Whole lists, as an editing form saves them: items keep the IDs they
	// carry, new ones (without an ID) get one.
	Conclusions *[]TopicItem `json:"conclusions,omitempty"`
	Questions   *[]TopicItem `json:"questions,omitempty"`

	// Single changes, as the lead's tool makes them.
	AddConclusions    []string        `json:"addConclusions,omitempty"`
	UpdateConclusions []TopicItemEdit `json:"updateConclusions,omitempty"`
	RemoveConclusions []string        `json:"removeConclusions,omitempty"`
	AddQuestions      []string        `json:"addQuestions,omitempty"`
	UpdateQuestions   []TopicItemEdit `json:"updateQuestions,omitempty"`
	RemoveQuestions   []string        `json:"removeQuestions,omitempty"`
	AddSources        []TopicSource   `json:"addSources,omitempty"`
	RemoveSources     []string        `json:"removeSources,omitempty"`

	// Archive sets the status archived; Restore returns an archived topic to
	// the status it had.
	Archive bool `json:"archive,omitempty"`
	Restore bool `json:"restore,omitempty"`
}

func (p TopicPatch) empty() bool {
	return p.Title == nil && p.Summary == nil && p.Status == nil && p.Conclusions == nil && p.Questions == nil &&
		len(p.AddConclusions)+len(p.UpdateConclusions)+len(p.RemoveConclusions)+len(p.AddQuestions)+len(p.UpdateQuestions)+len(p.RemoveQuestions)+len(p.AddSources)+len(p.RemoveSources) == 0 &&
		!p.Archive && !p.Restore
}

// lockTopics serializes topic reads and writes of one workspace. Windows
// readers do not share delete access, so reads take it too.
func (s *Store) lockTopics(name string) func() {
	s.lmu.Lock()
	if s.topicLocks == nil {
		s.topicLocks = map[string]chan struct{}{}
	}
	l := s.topicLocks[name]
	if l == nil {
		l = make(chan struct{}, 1)
		s.topicLocks[name] = l
	}
	s.lmu.Unlock()
	l <- struct{}{}
	return func() { <-l }
}

// useTopics runs fn with a yesman's topics while its workspace is in use.
// since, when set, must be the workspace's hiring time: a page or callback
// of a dismissed yesman cannot write into a new one hired with its name.
func (s *Store) useTopics(name string, since time.Time, fn func(Yesman, *topicsFile) (bool, error)) error {
	y, release, err := s.BeginUse(name)
	if err != nil {
		return err
	}
	defer release()
	if !since.IsZero() && !y.HiredAt.Equal(since) {
		return fmt.Errorf("%s was replaced by a new yesman of the same name; reopen it", name)
	}
	unlock := s.lockTopics(name)
	defer unlock()
	meta, err := s.openTopicsDir(y, false)
	if err != nil {
		return err
	}
	var f *topicsFile
	if meta == nil {
		f = &topicsFile{Version: topicsFileVersion, Topics: []Topic{}}
	} else {
		defer meta.Close()
		if f, err = readTopics(meta); err != nil {
			return err
		}
	}
	changed, err := fn(y, f)
	if err != nil || !changed {
		return err
	}
	if meta == nil {
		if meta, err = s.openTopicsDir(y, true); err != nil {
			return err
		}
		defer meta.Close()
	}
	f.Version = topicsFileVersion
	return writeTopics(meta, f)
}

const topicsFileName = "topics.json"

// openTopicsDir opens the workspace's .yesman directory from the store's own
// root, the one directory yesman was configured with (which may itself be
// reached through a link). Below it the workspace and .yesman must be real
// directories: a link standing in for either, whether it leads out of the
// store or to another workspace, is refused rather than followed. All reads
// and writes then go through this one open directory. It returns nil, nil
// when .yesman does not exist and create is false.
func (s *Store) openTopicsDir(y Yesman, create bool) (*os.Root, error) {
	fail := func(err error) error {
		return fmt.Errorf("%s cannot be opened inside the workspace (%v); topics are left as they are", filepath.Join(".yesman", topicsFileName), err)
	}
	store, err := os.OpenRoot(s.root)
	if err != nil {
		return nil, fail(err)
	}
	defer store.Close()
	ws, err := openPlainDir(store, y.Name)
	if err != nil {
		return nil, fail(err)
	}
	defer ws.Close()
	if create {
		if err := ws.Mkdir(".yesman", 0o755); err != nil && !errors.Is(err, os.ErrExist) {
			return nil, fail(err)
		}
	}
	meta, err := openPlainDir(ws, ".yesman")
	if errors.Is(err, os.ErrNotExist) && !create {
		return nil, nil
	}
	if err != nil {
		return nil, fail(err)
	}
	return meta, nil
}

// openPlainDir opens directory name in parent only when it is a real
// directory entry there, not a link or junction, and checks that the
// directory opened is the one looked at, so an entry swapped meanwhile is not
// taken for it.
func openPlainDir(parent *os.Root, name string) (*os.Root, error) {
	seen, err := parent.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !seen.IsDir() || seen.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 {
		return nil, fmt.Errorf("%s is a link or not a directory", name)
	}
	dir, err := parent.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	opened, err := dir.Stat(".")
	if err == nil && !os.SameFile(seen, opened) {
		err = fmt.Errorf("%s changed while it was opened", name)
	}
	if err != nil {
		dir.Close()
		return nil, err
	}
	return dir, nil
}

func readTopics(meta *os.Root) (*topicsFile, error) {
	seen, err := meta.Lstat(topicsFileName)
	if errors.Is(err, os.ErrNotExist) {
		return &topicsFile{Version: topicsFileVersion, Topics: []Topic{}}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("%s cannot be opened inside the workspace (%v); it is left as it is", topicsFileName, err)
	}
	if !seen.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file; it is left as it is", topicsFileName)
	}
	fh, err := meta.OpenFile(topicsFileName, os.O_RDONLY|topicsOpenFlags, 0)
	if err != nil {
		return nil, fmt.Errorf("%s cannot be opened inside the workspace (%v); it is left as it is", topicsFileName, err)
	}
	defer fh.Close()
	// What was opened is checked, not the name again: a file swapped in
	// meanwhile is not the one read.
	info, err := fh.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file; it is left as it is", topicsFileName)
	}
	if !os.SameFile(seen, info) {
		return nil, fmt.Errorf("%s changed while it was opened; it is left as it is", topicsFileName)
	}
	// A file with other names may be another workspace's or anyone's data:
	// it is neither read nor replaced.
	if n, err := fileLinks(fh); err != nil {
		return nil, err
	} else if n != 1 {
		return nil, fmt.Errorf("%s has %d names (a hard link); it is left as it is", topicsFileName, n)
	}
	data, err := io.ReadAll(io.LimitReader(fh, maxTopicsFileRead+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxTopicsFileRead {
		return nil, fmt.Errorf("%s is larger than %d MiB; it is left as it is", topicsFileName, maxTopicsFileRead>>20)
	}
	var f topicsFile
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("%s cannot be read (%v); it is left as it is, fix or move it to use topics", topicsFileName, err)
	}
	if f.Version > topicsFileVersion {
		return nil, fmt.Errorf("%s was written by a newer yesman (version %d); it is left as it is", topicsFileName, f.Version)
	}
	if err := f.check(); err != nil {
		return nil, fmt.Errorf("%s is not a valid topics file (%v); it is left as it is, fix or move it to use topics", topicsFileName, err)
	}
	for i := range f.Topics {
		f.Topics[i].normalizeLists()
		if f.Next < topicNumber(f.Topics[i].ID) {
			f.Next = topicNumber(f.Topics[i].ID)
		}
	}
	return &f, nil
}

// check rejects a file that parses but is not one yesman wrote: no version
// (such as null or {}), no list of topics, or topics whose IDs, statuses or
// revisions are missing or repeated. Fields it does not know are not checked;
// they are kept.
func (f *topicsFile) check() error {
	if f.Version != topicsFileVersion {
		return fmt.Errorf("version %d", f.Version)
	}
	if f.Topics == nil {
		return errors.New("no topics list")
	}
	if f.Next < 0 {
		return fmt.Errorf("next %d", f.Next)
	}
	ids := map[string]bool{}
	for i := range f.Topics {
		t := &f.Topics[i]
		if !topicIDRE.MatchString(t.ID) || ids[t.ID] {
			return fmt.Errorf("topic %d has a missing, invalid or repeated ID %q", i+1, t.ID)
		}
		ids[t.ID] = true
		if !slices.Contains(TopicStatuses, t.Status) || t.Revision < 1 || t.NextItem < 0 {
			return fmt.Errorf("topic %s has status %q, revision %d", t.ID, t.Status, t.Revision)
		}
		items := map[string]bool{}
		item := func(id string, kind byte) error {
			if !topicItemIDRE.MatchString(id) || id[0] != kind || items[id] {
				return fmt.Errorf("topic %s has a missing, invalid or repeated item ID %q", t.ID, id)
			}
			items[id] = true
			if n, _ := strconv.Atoi(id[1:]); n > t.NextItem {
				t.NextItem = n
			}
			return nil
		}
		for _, c := range t.Conclusions {
			if err := item(c.ID, 'c'); err != nil {
				return err
			}
		}
		for _, q := range t.Questions {
			if err := item(q.ID, 'q'); err != nil {
				return err
			}
		}
		for _, src := range t.Sources {
			if err := item(src.ID, 's'); err != nil {
				return err
			}
		}
		sends := map[string]bool{}
		for _, send := range t.Sends {
			if !topicSendIDRE.MatchString(send.ID) || sends[send.ID] {
				return fmt.Errorf("topic %s has a missing, invalid or repeated message ID", t.ID)
			}
			sends[send.ID] = true
		}
	}
	return nil
}

func topicNumber(id string) int {
	n, _ := strconv.Atoi(strings.TrimPrefix(id, "tp"))
	return n
}

// writeTopics replaces topics.json atomically through a temporary file of its
// own, created exclusively in the same open directory; a failed write leaves
// the old file in place and removes only that temporary file.
func writeTopics(meta *os.Root, f *topicsFile) error {
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	if len(data) > maxTopicsFile {
		return fmt.Errorf("topics would take more than %d MiB: archive doesn't shrink them, shorten or remove some content", maxTopicsFile>>20)
	}
	var (
		tmp  *os.File
		name string
	)
	for range 10 {
		var b [8]byte
		_, _ = rand.Read(b[:])
		name = fmt.Sprintf(".topics-%x.tmp", b)
		if tmp, err = meta.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644); !errors.Is(err, os.ErrExist) {
			break
		}
	}
	if err != nil {
		return err
	}
	_, err = tmp.Write(append(data, '\n'))
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = renameReplace(meta, name, topicsFileName)
	}
	if err != nil {
		_ = meta.Remove(name)
	}
	return err
}

// renameReplace retries briefly where another process's open handle can
// block replacing a file (Windows).
func renameReplace(dir *os.Root, from, to string) error {
	var err error
	for i := 0; i < 5; i++ {
		if err = dir.Rename(from, to); err == nil || runtime.GOOS != "windows" {
			return err
		}
		time.Sleep(time.Duration(20*(i+1)) * time.Millisecond)
	}
	return err
}

func (f *topicsFile) index(id string) int {
	return slices.IndexFunc(f.Topics, func(t Topic) bool { return t.ID == id })
}

// Topics returns a yesman's topics, in the order they were created.
func (s *Store) Topics(name string, since time.Time) ([]Topic, error) {
	var out []Topic
	err := s.useTopics(name, since, func(_ Yesman, f *topicsFile) (bool, error) {
		out = make([]Topic, len(f.Topics))
		for i, t := range f.Topics {
			out[i] = t.clone()
		}
		return false, nil
	})
	return out, err
}

// Topic returns one topic.
func (s *Store) Topic(name string, since time.Time, id string) (Topic, error) {
	id = TopicID(id)
	if err := ValidTopicID(id); err != nil {
		return Topic{}, err
	}
	var out Topic
	err := s.useTopics(name, since, func(_ Yesman, f *topicsFile) (bool, error) {
		i := f.index(id)
		if i < 0 {
			return false, fmt.Errorf("no topic %s", id)
		}
		out = f.Topics[i].clone()
		return false, nil
	})
	return out, err
}

// CreateTopic adds a topic and returns it with its new ID.
func (s *Store) CreateTopic(name string, since time.Time, by string, in TopicInput) (Topic, error) {
	if err := validActor(by); err != nil {
		return Topic{}, err
	}
	var out Topic
	err := s.useTopics(name, since, func(_ Yesman, f *topicsFile) (bool, error) {
		if len(f.Topics) >= maxTopics {
			return false, fmt.Errorf("a yesman keeps at most %d topics", maxTopics)
		}
		now := time.Now().UTC()
		t := Topic{ID: "tp" + strconv.Itoa(f.Next+1), Status: TopicOpen, Revision: 1, CreatedAt: now, UpdatedAt: now, CreatedBy: by, UpdatedBy: by}
		t.normalizeLists()
		title, err := topicTitle(in.Title)
		if err != nil {
			return false, err
		}
		t.Title = title
		if t.Summary, err = topicText(in.Summary, maxTopicSummary, "the summary"); err != nil {
			return false, err
		}
		if in.Status != "" {
			st, err := topicStatus(in.Status)
			if err != nil {
				return false, err
			}
			t.Status = st
		}
		for _, text := range in.Conclusions {
			if err := t.addItem(&t.Conclusions, 'c', text); err != nil {
				return false, err
			}
		}
		for _, text := range in.Questions {
			if err := t.addItem(&t.Questions, 'q', text); err != nil {
				return false, err
			}
		}
		for _, src := range in.Sources {
			if err := t.addSource(src); err != nil {
				return false, err
			}
		}
		f.Next++
		f.Topics = append(f.Topics, t)
		out = t.clone()
		return true, nil
	})
	return out, err
}

// PatchTopic changes a topic if it is still at the patch's revision; else
// it returns a *TopicConflictError and changes nothing.
func (s *Store) PatchTopic(name string, since time.Time, by, id string, p TopicPatch) (Topic, error) {
	if err := validActor(by); err != nil {
		return Topic{}, err
	}
	id = TopicID(id)
	if err := ValidTopicID(id); err != nil {
		return Topic{}, err
	}
	var out Topic
	err := s.useTopics(name, since, func(_ Yesman, f *topicsFile) (bool, error) {
		i := f.index(id)
		if i < 0 {
			return false, fmt.Errorf("no topic %s", id)
		}
		current := f.Topics[i]
		if p.Revision != current.Revision {
			return false, &TopicConflictError{Current: current.clone()}
		}
		if p.empty() {
			out = current.clone()
			return false, nil
		}
		t := current.clone()
		if err := t.apply(p); err != nil {
			return false, err
		}
		t.Revision++
		t.UpdatedAt = time.Now().UTC()
		t.UpdatedBy = by
		f.Topics[i] = t
		out = t.clone()
		return true, nil
	})
	return out, err
}

func (t *Topic) apply(p TopicPatch) error {
	if p.Archive && p.Restore {
		return errors.New("archive or restore, not both")
	}
	if p.Title != nil {
		title, err := topicTitle(*p.Title)
		if err != nil {
			return err
		}
		t.Title = title
	}
	if p.Summary != nil {
		summary, err := topicText(*p.Summary, maxTopicSummary, "the summary")
		if err != nil {
			return err
		}
		t.Summary = summary
	}
	if p.Status != nil {
		st, err := topicStatus(*p.Status)
		if err != nil {
			return err
		}
		if st == TopicArchived {
			return errors.New("archive a topic with archive, so it can be restored")
		}
		if t.Status == TopicArchived {
			t.ArchivedFrom = st // stays archived; restoring returns to this
		} else {
			t.Status = st
		}
	}
	if p.Conclusions != nil {
		if err := t.replaceItems(&t.Conclusions, 'c', *p.Conclusions); err != nil {
			return fmt.Errorf("conclusions: %w", err)
		}
	}
	if p.Questions != nil {
		if err := t.replaceItems(&t.Questions, 'q', *p.Questions); err != nil {
			return fmt.Errorf("open questions: %w", err)
		}
	}
	if err := t.editItems(&t.Conclusions, 'c', p.UpdateConclusions, p.RemoveConclusions, p.AddConclusions); err != nil {
		return fmt.Errorf("conclusions: %w", err)
	}
	if err := t.editItems(&t.Questions, 'q', p.UpdateQuestions, p.RemoveQuestions, p.AddQuestions); err != nil {
		return fmt.Errorf("open questions: %w", err)
	}
	for _, id := range p.RemoveSources {
		i := slices.IndexFunc(t.Sources, func(s TopicSource) bool { return s.ID == strings.TrimSpace(id) })
		if i < 0 {
			return fmt.Errorf("no source %q", id)
		}
		t.Sources = slices.Delete(t.Sources, i, i+1)
	}
	for _, src := range p.AddSources {
		if err := t.addSource(src); err != nil {
			return err
		}
	}
	switch {
	case p.Archive && t.Status != TopicArchived:
		t.ArchivedFrom, t.Status = t.Status, TopicArchived
	case p.Restore && t.Status == TopicArchived:
		t.Status = t.ArchivedFrom
		if t.Status != TopicResolved {
			t.Status = TopicOpen
		}
		t.ArchivedFrom = ""
	}
	return nil
}

func (t *Topic) nextID(kind byte) string {
	t.NextItem++
	return string(kind) + strconv.Itoa(t.NextItem)
}

func (t *Topic) addItem(list *[]TopicItem, kind byte, text string) error {
	text, err := topicText(text, maxTopicItemText, "an item")
	if err != nil {
		return err
	}
	if text == "" {
		return errors.New("an item is empty")
	}
	if len(*list) >= maxTopicItems {
		return fmt.Errorf("a topic keeps at most %d items in each list", maxTopicItems)
	}
	*list = append(*list, TopicItem{ID: t.nextID(kind), Text: text})
	return nil
}

func (t *Topic) editItems(list *[]TopicItem, kind byte, updates []TopicItemEdit, removes, adds []string) error {
	for _, e := range updates {
		i := slices.IndexFunc(*list, func(it TopicItem) bool { return it.ID == strings.TrimSpace(e.ID) })
		if i < 0 {
			return fmt.Errorf("no item %q", e.ID)
		}
		text, err := topicText(e.Text, maxTopicItemText, "an item")
		if err != nil {
			return err
		}
		if text == "" {
			return errors.New("an item is empty: remove it instead")
		}
		(*list)[i].Text = text
	}
	for _, id := range removes {
		i := slices.IndexFunc(*list, func(it TopicItem) bool { return it.ID == strings.TrimSpace(id) })
		if i < 0 {
			return fmt.Errorf("no item %q", id)
		}
		*list = slices.Delete(*list, i, i+1)
	}
	for _, text := range adds {
		if err := t.addItem(list, kind, text); err != nil {
			return err
		}
	}
	return nil
}

// replaceItems takes a whole list from a form: known IDs keep their place
// in identity, unknown or missing IDs are new items. Empty items are dropped.
func (t *Topic) replaceItems(list *[]TopicItem, kind byte, items []TopicItem) error {
	if len(items) > maxTopicItems {
		return fmt.Errorf("a topic keeps at most %d items in each list", maxTopicItems)
	}
	known := map[string]bool{}
	for _, it := range *list {
		known[it.ID] = true
	}
	seen := map[string]bool{}
	out := make([]TopicItem, 0, len(items))
	for _, it := range items {
		text, err := topicText(it.Text, maxTopicItemText, "an item")
		if err != nil {
			return err
		}
		if text == "" {
			continue
		}
		id := strings.TrimSpace(it.ID)
		if id != "" && (!known[id] || seen[id] || id[0] != kind) {
			id = ""
		}
		if id == "" {
			id = t.nextID(kind)
		}
		seen[id] = true
		out = append(out, TopicItem{ID: id, Text: text})
	}
	*list = out
	return nil
}

func (t *Topic) addSource(src TopicSource) error {
	if len(t.Sources) >= maxTopicSources {
		return fmt.Errorf("a topic keeps at most %d sources", maxTopicSources)
	}
	src, err := validSource(src)
	if err != nil {
		return err
	}
	for _, have := range t.Sources {
		if sameSource(have, src) {
			return nil // already cited
		}
	}
	src.ID = t.nextID('s')
	t.Sources = append(t.Sources, src)
	return nil
}

func sameSource(a, b TopicSource) bool {
	return a.Kind == b.Kind && a.Agent == b.Agent && a.Since.Equal(b.Since) && a.Role == b.Role && a.Timestamp == b.Timestamp && a.Ref == b.Ref
}

func validSource(src TopicSource) (TopicSource, error) {
	out := TopicSource{Kind: strings.ToLower(strings.TrimSpace(src.Kind))}
	if !slices.Contains(sourceKinds, out.Kind) {
		return out, fmt.Errorf("source kind %q is not one of %s", src.Kind, strings.Join(sourceKinds, ", "))
	}
	label, err := topicText(src.Label, maxTopicLabel, "a source label")
	if err != nil {
		return out, err
	}
	out.Label = strings.Join(strings.Fields(label), " ")
	agentName := strings.TrimSpace(src.Agent)
	ref := strings.TrimSpace(src.Ref)
	switch out.Kind {
	case SourceMessage:
		if agentName == "" {
			agentName = LeadName
		}
		if err := ValidName(agentName); err != nil {
			return out, err
		}
		if src.Role != "user" && src.Role != "assistant" {
			return out, errors.New("a message source has role user or assistant")
		}
		if src.Timestamp <= 0 {
			return out, errors.New("a message source needs the message's timestamp")
		}
		out.Agent, out.Since, out.Role, out.Timestamp = agentName, src.Since.UTC(), src.Role, src.Timestamp
	case SourceSubmission:
		if !topicSendIDRE.MatchString(ref) {
			return out, errors.New("a submission source needs a submission ID")
		}
		out.Ref = ref
	case SourceTask:
		ref = TaskID(ref)
		if !topicTaskIDRE.MatchString(ref) {
			return out, fmt.Errorf("%q is not a task ID such as t3", src.Ref)
		}
		out.Ref = ref
	case SourceWorker, SourceConversation:
		if agentName == "" && out.Kind == SourceConversation {
			agentName = LeadName
		}
		if err := ValidName(agentName); err != nil {
			return out, err
		}
		out.Agent, out.Since = agentName, src.Since.UTC()
	case SourceKnowledge:
		p := path.Clean(strings.ReplaceAll(strings.TrimPrefix(ref, ".yesman/knowledge/"), "\\", "/"))
		if p == "." || strings.HasPrefix(p, "/") || strings.HasPrefix(p, "../") || p == ".." || !strings.HasSuffix(strings.ToLower(p), ".md") || len(p) > maxTopicLabel || strings.ContainsAny(p, ":\x00") {
			return out, fmt.Errorf("%q is not a Knowledge note such as INDEX.md", src.Ref)
		}
		out.Ref = p
	case SourceLink:
		u, err := url.Parse(ref)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || len(ref) > maxTopicRef || u.User != nil {
			return out, fmt.Errorf("%q is not an http(s) link", src.Ref)
		}
		out.Ref = u.String()
	}
	if out.Since.IsZero() {
		out.Since = time.Time{}
	}
	return out, nil
}

func validActor(by string) error {
	if by != TopicByUser && by != TopicByLead {
		return fmt.Errorf("unknown topic editor %q", by)
	}
	return nil
}

func topicStatus(s string) (string, error) {
	st := strings.ToLower(strings.TrimSpace(s))
	if !slices.Contains(TopicStatuses, st) {
		return "", fmt.Errorf("status %q is not one of %s", s, strings.Join(TopicStatuses, ", "))
	}
	return st, nil
}

func topicTitle(s string) (string, error) {
	title, err := topicText(s, maxTopicTitle*4, "the title")
	if err != nil {
		return "", err
	}
	title = strings.Join(strings.Fields(title), " ")
	if title == "" {
		return "", errors.New("a topic needs a title")
	}
	if utf8.RuneCountInString(title) > maxTopicTitle {
		return "", fmt.Errorf("the title is longer than %d characters: put the rest in the summary", maxTopicTitle)
	}
	return title, nil
}

// topicText trims text and rejects invalid UTF-8, control characters other
// than newlines and tabs, and text over limit characters.
func topicText(s string, limit int, what string) (string, error) {
	if !utf8.ValidString(s) {
		return "", fmt.Errorf("%s is not valid text", what)
	}
	s = strings.ReplaceAll(s, "\r\n", "\n")
	if strings.IndexFunc(s, func(r rune) bool { return unicode.IsControl(r) && r != '\n' && r != '\t' }) >= 0 {
		return "", fmt.Errorf("%s has control characters", what)
	}
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) > limit {
		return "", fmt.Errorf("%s is longer than %d characters", what, limit)
	}
	return s, nil
}

// TopicMarker is the visible line sent before a message from a topic, so the
// lead (and anyone reading the conversation) sees which topic it is about.
func TopicMarker(t Topic) string {
	title := strings.Join(strings.Fields(t.Title), " ")
	if r := []rune(title); len(r) > topicMarkerTitleCap {
		title = string(r[:topicMarkerTitleCap]) + "…"
	}
	return topicMarkerPrefix + t.ID + ": " + title + "]"
}

// TopicMessage is the text sent to the lead for a message from a topic: the
// frozen context line, a blank line, then the user's text unchanged. Text
// beginning with "/" is a pi command, skill or template and is sent as it is.
func TopicMessage(context, text string) string {
	if context == "" {
		return text
	}
	if strings.TrimSpace(text) == "" {
		return context
	}
	return context + "\n\n" + text
}

// TopicCommand reports whether text is sent without the context line.
func TopicCommand(text string) bool { return strings.HasPrefix(strings.TrimLeft(text, " \t\n"), "/") }

// RegisterTopicSend records a message about to be sent from a topic. A
// repeated submission ID returns the record already made, with its frozen
// context line, so a retry sends exactly the same message; an ID recorded
// under another topic is refused. Archived topics take no new messages.
func (s *Store) RegisterTopicSend(name string, since time.Time, id string, send TopicSend, text string) (TopicSend, error) {
	id = TopicID(id)
	if err := ValidTopicID(id); err != nil {
		return TopicSend{}, err
	}
	if !topicSendIDRE.MatchString(send.ID) {
		return TopicSend{}, errors.New("invalid message ID")
	}
	var out TopicSend
	err := s.useTopics(name, since, func(_ Yesman, f *topicsFile) (bool, error) {
		for _, other := range f.Topics {
			if other.ID == id {
				continue
			}
			if slices.ContainsFunc(other.Sends, func(x TopicSend) bool { return x.ID == send.ID }) {
				return false, fmt.Errorf("this message was sent from topic %s", other.ID)
			}
		}
		i := f.index(id)
		if i < 0 {
			return false, fmt.Errorf("no topic %s: it may have been removed", id)
		}
		t := &f.Topics[i]
		if j := slices.IndexFunc(t.Sends, func(x TopicSend) bool { return x.ID == send.ID }); j >= 0 {
			out = t.Sends[j]
			return false, nil
		}
		if t.Status == TopicArchived {
			return false, ErrTopicArchived
		}
		preview, truncated := clipRunes(text, maxTopicSendText)
		rec := TopicSend{ID: send.ID, At: time.Now().UTC(), Preview: preview, Truncated: truncated, ImageCount: send.ImageCount, AgentSince: send.AgentSince, State: "sending"}
		if !TopicCommand(text) {
			rec.Context = TopicMarker(*t)
		}
		t.Sends = append(t.Sends, rec)
		if len(t.Sends) > maxTopicSends {
			t.Sends = slices.Clone(t.Sends[len(t.Sends)-maxTopicSends:])
		}
		out = rec
		return true, nil
	})
	return out, err
}

// SetTopicSendState records the last receipt state the app saw for a send.
// It is a note for the page, not proof that the lead read the message.
func (s *Store) SetTopicSendState(name string, since time.Time, id, sendID, state string) error {
	id = TopicID(id)
	if ValidTopicID(id) != nil || !topicSendState.MatchString(state) {
		return errors.New("invalid send state")
	}
	return s.useTopics(name, since, func(_ Yesman, f *topicsFile) (bool, error) {
		i := f.index(id)
		if i < 0 {
			return false, nil
		}
		t := &f.Topics[i]
		j := slices.IndexFunc(t.Sends, func(x TopicSend) bool { return x.ID == sendID })
		if j < 0 || t.Sends[j].State == state {
			return false, nil
		}
		t.Sends[j].State = state
		return true, nil
	})
}

// TopicCounts sums a yesman's topics by status, for the info panel.
type TopicCounts struct {
	Open     int    `json:"open"`
	Resolved int    `json:"resolved"`
	Archived int    `json:"archived"`
	Error    string `json:"error,omitempty"`
}

// TopicCounts counts topics by status; an unreadable file reports its error.
func (s *Store) TopicCounts(name string) TopicCounts {
	var c TopicCounts
	err := s.useTopics(name, time.Time{}, func(_ Yesman, f *topicsFile) (bool, error) {
		for _, t := range f.Topics {
			switch t.Status {
			case TopicOpen:
				c.Open++
			case TopicResolved:
				c.Resolved++
			case TopicArchived:
				c.Archived++
			}
		}
		return false, nil
	})
	if err != nil {
		c.Error = err.Error()
	}
	return c
}

func clipRunes(s string, n int) (string, bool) {
	if utf8.RuneCountInString(s) <= n {
		return s, false
	}
	return string([]rune(s)[:n]), true
}
