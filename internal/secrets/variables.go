package secrets

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"filippo.io/age/armor"
)

// Variables keeps the variables in a plain YAML file, as Format writes it.
// The file may be edited by hand; it is read again when it changes.
type Variables struct {
	path string

	mu      sync.Mutex
	entries []Entry
	err     error // the file's, as of stamp
	stamp   stamp
}

// NewVariables keeps variables in the file at path.
func NewVariables(path string) *Variables { return &Variables{path: path} }

// File is the variables' file.
func (v *Variables) File() string { return v.path }

// Entries are the variables; none when the file is not there.
func (v *Variables) Entries() ([]Entry, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	st, err := stampOf(v.path)
	if err != nil {
		return nil, err
	}
	if st != v.stamp || !st.ok {
		v.entries, v.err, v.stamp = nil, nil, st
		if st.ok {
			data, err := os.ReadFile(v.path)
			if err != nil {
				return nil, err
			}
			v.entries, v.err = Parse(KindVariables, data)
			if v.err != nil {
				v.err = fmt.Errorf("%s: %w", filepath.Base(v.path), v.err)
			}
		}
	}
	return slices.Clone(v.entries), v.err
}

// Save replaces the variables.
func (v *Variables) Save(entries []Entry) error {
	if err := Check(KindVariables, entries); err != nil {
		return err
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(v.path), 0o755); err != nil {
		return err
	}
	if err := writeFile(v.path, Format(KindVariables, entries)); err != nil {
		return err
	}
	st, err := stampOf(v.path)
	if err != nil {
		return err
	}
	v.entries, v.err, v.stamp = slices.Clone(entries), nil, st
	return nil
}

// ---------- Importing ----------

// Plan is what importing entries would change.
type Plan struct {
	Add    []string `json:"add"`    // names only in the file
	Change []string `json:"change"` // names whose value or description differ
	Same   []string `json:"same"`   // names the same in both
	Extra  []string `json:"extra"`  // names only here, which replacing removes
}

// PlanImport compares the entries here with those of a file.
func PlanImport(current, incoming []Entry) Plan {
	p := Plan{Add: []string{}, Change: []string{}, Same: []string{}, Extra: []string{}}
	have := map[string]Entry{}
	for _, e := range current {
		have[e.Name] = e
	}
	listed := map[string]bool{}
	for _, e := range incoming {
		listed[e.Name] = true
		switch c, ok := have[e.Name]; {
		case !ok:
			p.Add = append(p.Add, e.Name)
		case c != e:
			p.Change = append(p.Change, e.Name)
		default:
			p.Same = append(p.Same, e.Name)
		}
	}
	for _, e := range current {
		if !listed[e.Name] {
			p.Extra = append(p.Extra, e.Name)
		}
	}
	return p
}

// Merge is what importing leaves: replacing takes the file's entries
// alone; merging changes those here it lists, then adds the rest.
func Merge(current, incoming []Entry, replace bool) []Entry {
	if replace {
		return slices.Clone(incoming)
	}
	out := slices.Clone(current)
	at := map[string]int{}
	for i, e := range out {
		at[e.Name] = i
	}
	for _, e := range incoming {
		if i, ok := at[e.Name]; ok {
			out[i] = e
		} else {
			at[e.Name] = len(out)
			out = append(out, e)
		}
	}
	return out
}

// ageHeaders begin files age encrypted, armored or not.
var ageHeaders = []string{armor.Header, "age-encryption.org/v1"}

// IsEncrypted says whether data is an age file.
func IsEncrypted(data []byte) bool {
	s := strings.TrimLeft(string(data[:min(len(data), 64)]), " \t\r\n")
	for _, h := range ageHeaders {
		if strings.HasPrefix(s, h) {
			return true
		}
	}
	return false
}
