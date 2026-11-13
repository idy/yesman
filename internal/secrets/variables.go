package secrets

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"filippo.io/age"
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

// ---------- Moving entries between computers ----------

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

// Seal encrypts data with a passphrase as an armored age file, which
// `age -d` opens.
func Seal(data []byte, passphrase string, workFactor int) ([]byte, error) {
	if err := checkPassword(passphrase); err != nil {
		return nil, err
	}
	r, err := age.NewScryptRecipient(passphrase)
	if err != nil {
		return nil, err
	}
	if workFactor > 0 {
		r.SetWorkFactor(workFactor)
	}
	var buf bytes.Buffer
	aw := armor.NewWriter(&buf)
	w, err := age.Encrypt(aw, r)
	if err != nil {
		return nil, err
	}
	if _, err := w.Write(data); err != nil {
		return nil, err
	}
	if err := errors.Join(w.Close(), aw.Close()); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Open decrypts an age file Seal wrote, armored or not.
func Open(data []byte, passphrase string) ([]byte, error) {
	sid, err := age.NewScryptIdentity(passphrase)
	if err != nil {
		return nil, err
	}
	var in io.Reader = bytes.NewReader(data)
	if strings.HasPrefix(strings.TrimLeft(string(data[:min(len(data), 64)]), " \t\r\n"), armor.Header) {
		in = armor.NewReader(in)
	}
	r, err := age.Decrypt(in, sid)
	if err != nil {
		var none *age.NoIdentityMatchError
		if errors.As(err, &none) || errors.Is(err, age.ErrIncorrectIdentity) {
			return nil, errorf("the password of this file is wrong")
		}
		return nil, err
	}
	return io.ReadAll(r)
}

// ReadFile reads a file of entries to import: one Format wrote, plain or
// sealed with a passphrase.
func ReadFile(kind Kind, path, passphrase string, limit int64) ([]Entry, error) {
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
		return nil, errorf("this file is too large to import")
	}
	if IsEncrypted(data) {
		if passphrase == "" {
			return nil, ErrNeedsPassword
		}
		if data, err = Open(data, passphrase); err != nil {
			return nil, err
		}
	}
	return Parse(kind, data)
}

// ErrNeedsPassword says a file to import is encrypted.
var ErrNeedsPassword = errorf("this file is encrypted: enter its password")
