// Package crew is what yesman knows about the models its agents run as.
// The user lists the crew in Settings › yesman: options, each one of pi's
// models at one thinking level. A yesman's lead weighs their scores to
// choose which runs each worker. The scores come from Artificial Analysis
// (AA), which scores models at each reasoning effort, matched to pi's
// models by name; for an option AA has no record of, the user gives theirs.
//
// AA's scores are a snapshot embedded in the app, scores/aa-*.csv: AA's
// leaderboard, one row per model and effort. A newer snapshot replaces the
// file; the newest date in a file name wins.
package crew

import (
	"cmp"
	"embed"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io/fs"
	"math"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"unicode"
)

// Source is where the scores come from, to credit.
const Source = "https://artificialanalysis.ai"

// Levels are pi's thinking levels, least first.
var Levels = []string{"off", "minimal", "low", "medium", "high", "xhigh", "max"}

// Score is AA's measurement of one model at one effort, or the user's own
// estimate of it. Figures not measured are nil.
type Score struct {
	// Name is AA's name for the model at this effort, such as
	// "GPT-6.1 Sol (high)"; empty for the user's.
	Name string `json:"name,omitempty"`
	// Base is the model without the effort: "GPT-6.1 Sol".
	Base string `json:"base,omitempty"`
	// Effort is the pi thinking level AA ran it at, or "" when AA's name
	// says none.
	Effort  string `json:"effort,omitempty"`
	Creator string `json:"creator,omitempty"`
	Rank    int    `json:"rank,omitempty"`
	// Intelligence is AA's Intelligence Index: higher is smarter.
	Intelligence float64 `json:"intelligence"`
	// Speed is the output speed, in tokens per second.
	Speed *float64 `json:"speed,omitempty"`
	// Coding is the mean of AA's coding evaluations, in percent.
	Coding *float64 `json:"coding,omitempty"`
	// CostPerTask is what one Intelligence Index task costs, in US dollars.
	CostPerTask *float64 `json:"costPerTask,omitempty"`
	// MinutesPerTask is how long one Intelligence Index task takes.
	MinutesPerTask *float64 `json:"minutesPerTask,omitempty"`
	// TokensPerTask is how many tokens one task writes, reasoning included.
	TokensPerTask *float64 `json:"tokensPerTask,omitempty"`
}

// Snapshot is AA's scores as of one day.
type Snapshot struct {
	Date   string  `json:"date"` // YYYY-MM-DD
	Scores []Score `json:"scores"`
}

// Bases are the AA models the snapshot scores, by name.
func (s Snapshot) Bases() []string {
	var out []string
	for _, sc := range s.Scores {
		if !slices.Contains(out, sc.Base) {
			out = append(out, sc.Base)
		}
	}
	slices.SortFunc(out, func(a, b string) int { return cmp.Compare(strings.ToLower(a), strings.ToLower(b)) })
	return out
}

//go:embed scores/*.csv
var scoresFS embed.FS

// Embedded is the snapshot built into the app.
var Embedded = sync.OnceValue(func() Snapshot {
	s, err := loadNewest(scoresFS, "scores")
	if err != nil {
		panic(err) // the file is part of the build; tests read it
	}
	return s
})

var dated = regexp.MustCompile(`(\d{4}-\d{2}-\d{2})\.csv$`)

// loadNewest reads the snapshot of the newest dated CSV file in dir.
func loadNewest(fsys fs.FS, dir string) (Snapshot, error) {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return Snapshot{}, err
	}
	var name, date string
	for _, e := range entries {
		if m := dated.FindStringSubmatch(e.Name()); m != nil && m[1] > date {
			name, date = e.Name(), m[1]
		}
	}
	if name == "" {
		return Snapshot{}, fmt.Errorf("no dated scores in %s", dir)
	}
	data, err := fs.ReadFile(fsys, path.Join(dir, name))
	if err != nil {
		return Snapshot{}, err
	}
	scores, err := Parse(string(data))
	if err != nil {
		return Snapshot{}, fmt.Errorf("%s: %w", name, err)
	}
	return Snapshot{Date: date, Scores: scores}, nil
}

// Parse reads AA's scores from CSV with a header row, by column name.
// model, creator and intelligence_index are required; rank, tok_per_s,
// coding_mean_pct, cost_per_task_usd, time_min_per_task and
// tokens_per_task are read when present.
func Parse(text string) ([]Score, error) {
	r := csv.NewReader(strings.NewReader(text))
	rows, err := r.ReadAll()
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("no header")
	}
	col := map[string]int{}
	for i, name := range rows[0] {
		col[strings.TrimSpace(name)] = i
	}
	for _, need := range []string{"model", "intelligence_index"} {
		if _, ok := col[need]; !ok {
			return nil, fmt.Errorf("no %s column", need)
		}
	}
	cell := func(row []string, name string) string {
		if i, ok := col[name]; ok && i < len(row) {
			return strings.TrimSpace(row[i])
		}
		return ""
	}
	number := func(row []string, name string) (*float64, error) {
		v := cell(row, name)
		if v == "" {
			return nil, nil
		}
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		return &f, nil
	}
	var out []Score
	for n, row := range rows[1:] {
		s := Score{Name: cell(row, "model"), Creator: cell(row, "creator")}
		if s.Name == "" {
			continue
		}
		s.Base, s.Effort = splitEffort(s.Name)
		intel, err := number(row, "intelligence_index")
		if err == nil && intel == nil {
			err = fmt.Errorf("no intelligence_index")
		}
		if err == nil {
			s.Intelligence = *intel
			s.Speed, err = number(row, "tok_per_s")
		}
		if err == nil {
			s.Coding, err = number(row, "coding_mean_pct")
		}
		if err == nil {
			s.CostPerTask, err = number(row, "cost_per_task_usd")
		}
		if err == nil {
			s.MinutesPerTask, err = number(row, "time_min_per_task")
		}
		if err == nil {
			s.TokensPerTask, err = number(row, "tokens_per_task")
		}
		if err != nil {
			return nil, fmt.Errorf("row %d (%s): %w", n+2, s.Name, err)
		}
		if v := cell(row, "rank"); v != "" {
			s.Rank, _ = strconv.Atoi(v)
		}
		out = append(out, s)
	}
	return out, nil
}

var effortSuffix = regexp.MustCompile(`(?i)^(.*\S)\s*\((max|xhigh|high|medium|low|minimal|non-reasoning)\)$`)

// splitEffort splits AA's name for a model at an effort, such as
// "GPT-6.1 Sol (high)", into the model and pi's thinking level. A
// parenthesis that names no effort, such as Qwen3.8 Max (0902), stays in
// the name.
func splitEffort(name string) (base, effort string) {
	m := effortSuffix.FindStringSubmatch(name)
	if m == nil {
		return name, ""
	}
	effort = strings.ToLower(m[2])
	if effort == "non-reasoning" {
		effort = "off"
	}
	return m[1], effort
}

// Model is one of pi's models, with the thinking levels it supports.
type Model struct {
	Provider string   `json:"provider"`
	ID       string   `json:"id"`
	Name     string   `json:"name,omitempty"`
	Levels   []string `json:"levels"`
}

// Key names the model: provider/id.
func (m Model) Key() string { return m.Provider + "/" + m.ID }

// SupportedLevels are the thinking levels a model of pi's catalog takes,
// from its reasoning flag and thinkingLevelMap, as pi decides them: a
// model without reasoning only turns thinking off; one with it takes every
// level its map does not set to null, but xhigh and max only when the map
// names them.
func SupportedLevels(reasoning bool, levelMap map[string]json.RawMessage) []string {
	if !reasoning {
		return []string{"off"}
	}
	var out []string
	for _, l := range Levels {
		mapped, ok := levelMap[l]
		if ok && string(mapped) == "null" {
			continue
		}
		if (l == "xhigh" || l == "max") && !ok {
			continue
		}
		out = append(out, l)
	}
	return out
}

// normalize keeps a name's letters and digits, in lower case, so that
// "GPT-6.1 Sol" and "gpt-6.1-sol" compare equal.
func normalize(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// Suggest is the AA model a model of pi's catalog most likely is, by name,
// or "": its ID's last part, as auto/claude-opus-5-5 or
// codex-remote/codex/gpt-6.1-sol, or its name before any " · " that
// describes where it is served, as "Kimi K3.1 Pro · China Magpie".
func Suggest(m Model, bases []string) string {
	id := m.ID
	if i := strings.LastIndex(id, "/"); i >= 0 {
		id = id[i+1:]
	}
	name, _, _ := strings.Cut(m.Name, " · ")
	keys := []string{normalize(id), normalize(name)}
	for _, key := range keys {
		if key == "" {
			continue
		}
		for _, b := range bases {
			if normalize(b) == key {
				return b
			}
		}
	}
	return ""
}

// ShortName is pi's name for a model without where it is served, as
// "Kimi K3.1 Pro" for "Kimi K3.1 Pro · China Magpie", else its ID.
func ShortName(name, id string) string {
	if n, _, _ := strings.Cut(name, " · "); n != "" {
		return n
	}
	return id
}

// Lookup is AA's score for one of pi's models at a thinking level, the
// model found by name. AA names the effort of most of its scores; a model
// it scored only at an effort it does not name has that score at any.
func (s Snapshot) Lookup(m Model, effort string) (Score, bool) {
	base := Suggest(m, s.Bases())
	if base == "" {
		return Score{}, false
	}
	var plain []Score
	named := false
	for _, sc := range s.Scores {
		switch {
		case sc.Base != base:
		case sc.Effort == "":
			plain = append(plain, sc)
		case sc.Effort == effort:
			return sc, true
		default:
			named = true
		}
	}
	if !named && len(plain) == 1 {
		return plain[0], true
	}
	return Score{}, false
}

// CheckScore says what is wrong with the user's own score, if anything:
// it needs an intelligence on AA's scale, and no figure below zero.
func CheckScore(s Score) error {
	if !(s.Intelligence > 0 && s.Intelligence <= 100) {
		return fmt.Errorf("the intelligence score must be above 0 and at most 100")
	}
	for _, f := range []*float64{s.Speed, s.Coding, s.CostPerTask, s.MinutesPerTask} {
		if f != nil && (*f < 0 || math.IsInf(*f, 0) || math.IsNaN(*f)) {
			return fmt.Errorf("scores cannot be below zero")
		}
	}
	if s.Coding != nil && *s.Coding > 100 {
		return fmt.Errorf("coding is a percentage, at most 100")
	}
	return nil
}

// Option is a member of the crew as the user chose it: one of pi's models
// at one thinking level.
type Option struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Effort   string `json:"effort"`
	// Score is the user's own, for an option AA has no record of.
	Score *Score `json:"score,omitempty"`
}

// Key names the option: provider/model:effort.
func (o Option) Key() string { return MemberKey(o.Provider, o.Model, o.Effort) }

// Config is the user's crew settings, kept with yesman's settings.
type Config struct {
	// Options are the crew, in the order the user added them.
	Options []Option `json:"options,omitempty"`
	// Yesman is the key of the option a new yesman's lead runs as.
	Yesman string `json:"yesman,omitempty"`
}

// Clone copies a Config, so a change to it leaves the original alone. The
// options' scores are shared: replace one rather than change it.
func (c Config) Clone() Config {
	c.Options = slices.Clone(c.Options)
	return c
}

// IsZero reports whether nothing is configured, so settings leave it out.
func (c Config) IsZero() bool { return len(c.Options) == 0 && c.Yesman == "" }

// Index is where the option with a key is, or -1.
func (c Config) Index(key string) int {
	return slices.IndexFunc(c.Options, func(o Option) bool { return o.Key() == key })
}

// Where a member's scores come from.
const (
	SourceAA   = "aa"   // Artificial Analysis
	SourceUser = "user" // the user's own estimate
)

// Member is an option of the crew with what yesman knows of it.
type Member struct {
	Key      string `json:"key"`
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Effort   string `json:"effort,omitempty"`
	// ModelName is pi's name for the model.
	ModelName string `json:"modelName,omitempty"`
	// Score is AA's, else the user's, else nil; Source says which.
	Score  *Score `json:"score,omitempty"`
	Source string `json:"source,omitempty"`
	// Missing: pi no longer has the model, or it no longer takes the
	// effort, so no agent can run as it.
	Missing bool `json:"missing,omitempty"`
}

// MemberKey names a model at a thinking level, "" for pi's default.
func MemberKey(provider, model, effort string) string {
	if effort == "" {
		return provider + "/" + model
	}
	return provider + "/" + model + ":" + effort
}

// ParseKey splits a member's key. Model IDs may hold slashes and colons:
// the provider ends at the first slash, and the effort is what follows the
// last colon only when it is a thinking level.
func ParseKey(key string) (provider, model, effort string, ok bool) {
	provider, model, ok = strings.Cut(key, "/")
	if !ok || provider == "" || model == "" {
		return "", "", "", false
	}
	if i := strings.LastIndex(model, ":"); i >= 0 && slices.Contains(Levels, model[i+1:]) {
		model, effort = model[:i], model[i+1:]
	}
	return provider, model, effort, model != ""
}

// Members are the crew's options, in the user's order, with their scores.
// models is pi's catalog, nil when it is not known, which marks no member
// Missing.
func Members(c Config, models []Model, snap Snapshot) []Member {
	out := []Member{}
	for _, o := range c.Options {
		m := Model{Provider: o.Provider, ID: o.Model}
		missing := false
		if models != nil {
			i := slices.IndexFunc(models, func(x Model) bool { return x.Provider == o.Provider && x.ID == o.Model })
			if i >= 0 {
				m = models[i]
			}
			missing = i < 0 || !slices.Contains(m.Levels, o.Effort)
		}
		mem := Member{Key: o.Key(), Provider: o.Provider, Model: o.Model, Effort: o.Effort, ModelName: m.Name, Missing: missing}
		if s, ok := snap.Lookup(m, o.Effort); ok {
			mem.Score, mem.Source = &s, SourceAA
		} else if o.Score != nil {
			s := *o.Score
			mem.Score, mem.Source = &s, SourceUser
		}
		out = append(out, mem)
	}
	return out
}

// Smartest sorts members by intelligence, most first, those without
// scores last.
func Smartest(members []Member) []Member {
	out := slices.Clone(members)
	slices.SortStableFunc(out, func(a, b Member) int {
		switch {
		case a.Score == nil && b.Score == nil:
			return 0
		case a.Score == nil:
			return 1
		case b.Score == nil:
			return -1
		}
		return cmp.Compare(b.Score.Intelligence, a.Score.Intelligence)
	})
	return out
}

// Find is the member with a key that agents can run as.
func Find(members []Member, key string) (Member, bool) {
	i := slices.IndexFunc(members, func(m Member) bool { return m.Key == key && !m.Missing })
	if i < 0 {
		return Member{}, false
	}
	return members[i], true
}

// Usable reports whether agents can run as the member with a key.
func Usable(members []Member, key string) bool {
	_, ok := Find(members, key)
	return ok
}
