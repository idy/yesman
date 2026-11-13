package secrets

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"filippo.io/age"
	"filippo.io/age/armor"
)

// A Vault keeps the secrets in secrets.sops.yaml, a sops file encrypted
// to an age key. The key is kept in identity.age, encrypted with the
// user's password, which yesman never stores: unlocking reads the key
// into memory, until the vault is locked again or the app quits.
//
// With the key, `age -d identity.age` and then sops itself
// (SOPS_AGE_KEY_FILE) read and edit the same file.
type Vault struct {
	dir string
	// WorkFactor is the scrypt work factor a password protects the key
	// with: 0 for age's default, which takes about a second. Tests lower it.
	WorkFactor int

	mu      sync.Mutex
	id      *age.X25519Identity // nil while locked
	entries []Entry             // the file's, decrypted, as of stamp
	stamp   stamp
}

// stamp tells a file read from one changed since.
type stamp struct {
	mod  time.Time
	size int64
	ok   bool // the file was there
}

func stampOf(path string) (stamp, error) {
	info, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return stamp{}, nil
	}
	if err != nil {
		return stamp{}, err
	}
	return stamp{mod: info.ModTime(), size: info.Size(), ok: true}, nil
}

var (
	ErrLocked        = errorf("the secrets are locked: unlock them with their password")
	ErrNotSetUp      = errorf("secrets are not set up yet")
	ErrWrongPassword = errorf("the password is wrong")
)

// minPassword is the fewest characters a password may have.
const minPassword = 8

// NewVault keeps secrets in dir.
func NewVault(dir string) *Vault { return &Vault{dir: dir} }

// Dir is the folder of the secrets and their key.
func (v *Vault) Dir() string { return v.dir }

// File is the sops file of the secrets.
func (v *Vault) File() string { return filepath.Join(v.dir, "secrets.sops.yaml") }

// KeyFile is the age key, encrypted with the password.
func (v *Vault) KeyFile() string { return filepath.Join(v.dir, "identity.age") }

// Configured says whether a password was set.
func (v *Vault) Configured() bool {
	_, err := os.Stat(v.KeyFile())
	return err == nil
}

// Unlocked says whether the key is in memory.
func (v *Vault) Unlocked() bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.id != nil
}

func checkPassword(password string) error {
	if len([]rune(password)) < minPassword {
		return errorf("the password should have at least %d characters", minPassword)
	}
	return nil
}

// Create sets up the vault: a new key, kept with password, and an empty
// secrets file. The vault is unlocked after.
func (v *Vault) Create(password string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.Configured() {
		return errorf("secrets are set up already")
	}
	if err := checkPassword(password); err != nil {
		return err
	}
	if _, err := os.Stat(v.File()); err == nil {
		return errorf("%s is there without its key, identity.age: put the key back, or move the file away", v.File())
	}
	id, err := age.GenerateX25519Identity()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(v.dir, 0o700); err != nil {
		return err
	}
	if err := v.writeKey(id, password); err != nil {
		return err
	}
	v.id = id
	if err := v.saveLocked(nil); err != nil {
		v.id = nil
		return err
	}
	return nil
}

// keyText is the key as age-keygen writes it, which sops reads.
func keyText(id *age.X25519Identity) string {
	return fmt.Sprintf("# created: %s\n# public key: %s\n%s\n", time.Now().UTC().Format(time.RFC3339), id.Recipient(), id)
}

func (v *Vault) writeKey(id *age.X25519Identity, password string) error {
	r, err := age.NewScryptRecipient(password)
	if err != nil {
		return err
	}
	if v.WorkFactor > 0 {
		r.SetWorkFactor(v.WorkFactor)
	}
	var buf bytes.Buffer
	aw := armor.NewWriter(&buf)
	w, err := age.Encrypt(aw, r)
	if err != nil {
		return err
	}
	if _, err := io.WriteString(w, keyText(id)); err != nil {
		return err
	}
	if err := errors.Join(w.Close(), aw.Close()); err != nil {
		return err
	}
	return writeFile(v.KeyFile(), buf.Bytes())
}

// openKey decrypts the key file with password.
func openKey(data []byte, password string) (*age.X25519Identity, error) {
	sid, err := age.NewScryptIdentity(password)
	if err != nil {
		return nil, err
	}
	r, err := age.Decrypt(armor.NewReader(bytes.NewReader(data)), sid)
	if err != nil {
		var none *age.NoIdentityMatchError
		if errors.As(err, &none) || errors.Is(err, age.ErrIncorrectIdentity) {
			return nil, ErrWrongPassword
		}
		return nil, fmt.Errorf("read the key: %w", err)
	}
	ids, err := age.ParseIdentities(r)
	if err != nil {
		return nil, fmt.Errorf("read the key: %w", err)
	}
	for _, id := range ids {
		if x, ok := id.(*age.X25519Identity); ok {
			return x, nil
		}
	}
	return nil, errors.New("the key file has no age key")
}

func (v *Vault) readKey(password string) (*age.X25519Identity, error) {
	data, err := os.ReadFile(v.KeyFile())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrNotSetUp
	}
	if err != nil {
		return nil, err
	}
	return openKey(data, password)
}

// Unlock reads the key with password, and the secrets with it.
func (v *Vault) Unlock(password string) error {
	id, err := v.readKey(password)
	if err != nil {
		return err
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	v.id, v.stamp, v.entries = id, stamp{}, nil
	if _, err := v.loadLocked(); err != nil {
		v.id = nil
		return err
	}
	return nil
}

// RemoveEmpty removes a vault without secrets, as one just set up whose
// password could not be kept. It refuses one that has secrets.
func (v *Vault) RemoveEmpty() error {
	names, err := v.Names()
	if err != nil {
		return err
	}
	if len(names) > 0 {
		return errorf("the secrets file has secrets, so it stays")
	}
	v.Lock()
	return errors.Join(removeIfThere(v.File()), removeIfThere(v.KeyFile()))
}

func removeIfThere(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// Lock forgets the key and the secrets.
func (v *Vault) Lock() {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.id, v.entries, v.stamp = nil, nil, stamp{}
}

// ChangePassword keeps the key with a new password. The secrets file
// stays as it is.
func (v *Vault) ChangePassword(old, password string) error {
	if err := checkPassword(password); err != nil {
		return err
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	id, err := v.readKey(old)
	if err != nil {
		return err
	}
	return v.writeKey(id, password)
}

// Key is the age key, as age-keygen writes it, for sops.
func (v *Vault) Key() (string, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.id == nil {
		return "", ErrLocked
	}
	return keyText(v.id), nil
}

// Recipients lists the age recipients the secrets file is encrypted to,
// which it keeps in the clear: none without a file.
func (v *Vault) Recipients() ([]string, error) {
	data, err := os.ReadFile(v.File())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return Recipients(data)
}

// Recipient is the vault's own age recipient, while unlocked.
func (v *Vault) Recipient() (string, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.id == nil {
		return "", ErrLocked
	}
	return v.id.Recipient().String(), nil
}

// Files are the key file and the secrets file as they are, encrypted, to
// carry to another computer with the password.
func (v *Vault) Files() (key, file []byte, err error) {
	if key, err = os.ReadFile(v.KeyFile()); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil, ErrNotSetUp
		}
		return nil, nil, err
	}
	if file, err = os.ReadFile(v.File()); errors.Is(err, fs.ErrNotExist) {
		err = nil // no secrets saved yet
	}
	return key, file, err
}

// OpenFiles reads the secrets of another vault's files with its password.
func OpenFiles(key, file []byte, password string) ([]Entry, error) {
	id, err := openKey(key, password)
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(file)) == 0 {
		return nil, nil
	}
	tree, err := Decrypt(file, id)
	if err != nil {
		return nil, err
	}
	return fromNode(KindSecrets, tree)
}

// Restore sets the vault up with another's files, as they are. With their
// password it checks them first and is unlocked after; without, it stays
// locked until unlocked with it.
func (v *Vault) Restore(key, file []byte, password string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.Configured() {
		return errorf("secrets are set up already")
	}
	if _, err := os.Stat(v.File()); err == nil {
		return errorf("%s is there without its key, identity.age: put the key back, or move the file away", v.File())
	}
	var id *age.X25519Identity
	if password != "" {
		var err error
		if id, err = openKey(key, password); err != nil {
			return err
		}
		if len(bytes.TrimSpace(file)) > 0 {
			tree, err := Decrypt(file, id)
			if err != nil {
				return err
			}
			if _, err := fromNode(KindSecrets, tree); err != nil {
				return err
			}
		}
	}
	if err := os.MkdirAll(v.dir, 0o700); err != nil {
		return err
	}
	if len(bytes.TrimSpace(file)) > 0 {
		if err := writeFile(v.File(), file); err != nil {
			return err
		}
	}
	if err := writeFile(v.KeyFile(), key); err != nil {
		return err
	}
	v.id, v.entries, v.stamp = id, nil, stamp{}
	if id != nil {
		if _, err := v.loadLocked(); err != nil {
			v.id = nil
			return err
		}
	}
	return nil
}

// Names lists the secrets' names, which the sops file keeps in the clear,
// locked or not.
func (v *Vault) Names() ([]string, error) {
	data, err := os.ReadFile(v.File())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return FileNames(data)
}

// FileNames lists the names of the secrets in a secrets file, without its
// key.
func FileNames(data []byte) ([]string, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, nil
	}
	tree, _, err := readSops(data)
	if err != nil {
		return nil, err
	}
	var names []string
	for i := 0; i+1 < len(tree.Content); i += 2 {
		if tree.Content[i].Value != string(KindSecrets) {
			continue
		}
		list := tree.Content[i+1]
		for j := 0; j+1 < len(list.Content); j += 2 {
			names = append(names, list.Content[j].Value)
		}
	}
	return names, nil
}

// Entries are the secrets, read again when the file changed.
func (v *Vault) Entries() ([]Entry, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.id == nil {
		return nil, ErrLocked
	}
	entries, err := v.loadLocked()
	return slices.Clone(entries), err
}

func (v *Vault) loadLocked() ([]Entry, error) {
	st, err := stampOf(v.File())
	if err != nil {
		return nil, err
	}
	if st == v.stamp && st.ok {
		return v.entries, nil
	}
	if !st.ok {
		v.entries, v.stamp = nil, st
		return nil, nil
	}
	data, err := os.ReadFile(v.File())
	if err != nil {
		return nil, err
	}
	tree, err := Decrypt(data, v.id)
	if err != nil {
		return nil, err
	}
	entries, err := fromNode(KindSecrets, tree)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", filepath.Base(v.File()), err)
	}
	v.entries, v.stamp = entries, st
	return entries, nil
}

// Save replaces the secrets. The file stays encrypted to the other age
// recipients it had, such as a key sops was given.
func (v *Vault) Save(entries []Entry) error {
	if err := Check(KindSecrets, entries); err != nil {
		return err
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.id == nil {
		return ErrLocked
	}
	return v.saveLocked(entries)
}

func (v *Vault) saveLocked(entries []Entry) error {
	recipients := []*age.X25519Recipient{v.id.Recipient()}
	if data, err := os.ReadFile(v.File()); err == nil {
		others, err := Recipients(data)
		if err != nil {
			return err
		}
		for _, s := range others {
			if s == v.id.Recipient().String() {
				continue
			}
			r, err := age.ParseX25519Recipient(s)
			if err != nil {
				return errorf("the secrets file is also encrypted to %s, which yesman cannot encrypt to", s)
			}
			recipients = append(recipients, r)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	data, err := Encrypt(toNode(KindSecrets, entries), recipients, time.Now())
	if err != nil {
		return err
	}
	if err := os.MkdirAll(v.dir, 0o700); err != nil {
		return err
	}
	if err := writeFile(v.File(), data); err != nil {
		return err
	}
	st, err := stampOf(v.File())
	if err != nil {
		return err
	}
	v.entries, v.stamp = slices.Clone(entries), st
	return nil
}

// writeFile replaces path whole, readable only by the user (CreateTemp
// makes files 0600), so a crash leaves the old file.
func writeFile(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // nothing is left after the rename
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
