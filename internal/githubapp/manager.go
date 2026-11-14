package githubapp

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/idy/yesman/internal/secrets"
)

// Keychain is implemented by the desktop's existing native credential store
// (Keychain, Windows Credential Manager, Secret Service). It only stores the
// random vault password, never a key in an agent environment.
type Keychain interface {
	Get(string) ([]byte, error)
	Set(string, []byte) error
	Delete(string) error
}

type Config struct {
	Enabled              bool              `json:"enabled"`
	Repositories         []string          `json:"repositories"`
	Workspaces           []string          `json:"workspaces"`
	WorkspaceGenerations map[string]string `json:"workspaceGenerations,omitempty"`
}

type View struct {
	Config           Config        `json:"config"`
	Configured       bool          `json:"configured"`
	Unlocked         bool          `json:"unlocked"`
	Keychain         bool          `json:"keychain"`
	App              *Registration `json:"app,omitempty"`
	Verified         bool          `json:"verified"`
	RoutingAvailable bool          `json:"routingAvailable"`
	Connections      []Connection  `json:"connections"`
	Error            string        `json:"error,omitempty"`
}

// Manager owns a separate encrypted vault, never the shell Secrets vault.
// Mutations are serialized; the small state mutex never covers network I/O.
// Every configuration/key/lock change cancels in-flight App operations.
type Manager struct {
	mu          sync.Mutex
	operations  sync.Mutex
	dir         string
	vault       *secrets.Vault
	keychain    Keychain
	gh          string
	cfg         Config
	loadErr     error
	closed      bool
	client      *Client
	app         *Registration
	verified    bool
	connections []Connection
	epoch       context.Context
	cancel      context.CancelFunc
	revision    uint64
	newClient   func(Registration, []byte, []string) (*Client, error)
}

func NewManager(dir string, keychain Keychain, gh string) *Manager {
	ctx, cancel := context.WithCancel(context.Background())
	m := &Manager{dir: dir, vault: secrets.NewVault(filepath.Join(dir, "vault")), keychain: keychain, gh: gh, epoch: ctx, cancel: cancel, newClient: ClientFromKey}
	m.cfg = Config{Repositories: []string{}, Workspaces: []string{}}
	data, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err == nil {
		if len(data) > 65536 || json.Unmarshal(data, &m.cfg) != nil {
			m.loadErr = errors.New("GitHub App settings could not be read; restore config.json before creating PRs")
		} else {
			normalized, checkErr := checkedConfig(m.cfg)
			m.loadErr = checkErr
			if checkErr == nil {
				m.cfg = normalized
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		m.loadErr = errors.New("GitHub App settings could not be read")
	}
	return m
}

func checkedConfig(cfg Config) (Config, error) {
	if len(cfg.Repositories) > 100 || len(cfg.Workspaces) > 100 {
		return Config{}, errors.New("select at most 100 repositories and workspaces")
	}
	cfg.Repositories = slices.Clone(cfg.Repositories)
	cfg.Workspaces = slices.Clone(cfg.Workspaces)
	for i, repo := range cfg.Repositories {
		if !validRepository(repo) {
			return Config{}, errors.New("repositories must be explicit owner/repo names")
		}
		cfg.Repositories[i] = strings.ToLower(repo)
	}
	generations := make(map[string]string, len(cfg.Workspaces))
	for _, name := range cfg.Workspaces {
		if !workspacePattern(name) {
			return Config{}, errors.New("select existing yesman workspaces")
		}
		generation := cfg.WorkspaceGenerations[name]
		if _, err := time.Parse(time.RFC3339Nano, generation); err != nil {
			return Config{}, errors.New("save the current workspace scope in Settings before using App identity")
		}
		generations[name] = generation
	}
	cfg.WorkspaceGenerations = generations
	slices.Sort(cfg.Repositories)
	cfg.Repositories = slices.Compact(cfg.Repositories)
	slices.Sort(cfg.Workspaces)
	cfg.Workspaces = slices.Compact(cfg.Workspaces)
	if cfg.Enabled && (len(cfg.Repositories) == 0 || len(cfg.Workspaces) == 0) {
		return Config{}, errors.New("select repositories and workspaces before enabling App identity")
	}
	return cfg, nil
}
func workspacePattern(name string) bool {
	if name == "" || name == "." || name == ".." || len(name) > 100 {
		return false
	}
	for _, r := range name {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

func (m *Manager) hasKeychain() bool {
	if m.keychain == nil {
		return false
	}
	_, err := m.keychain.Get("github-app-password/probe")
	return !errors.Is(err, errors.ErrUnsupported)
}
func (m *Manager) passwordKey() (string, error) {
	recipients, err := m.vault.Recipients()
	if err != nil {
		return "", err
	}
	if len(recipients) != 1 {
		return "", errors.New("GitHub App vault must have one local recipient")
	}
	return "github-app-password/" + recipients[0], nil
}

var errManagerClosed = errors.New("GitHub App identity service is closed")

func (m *Manager) closedError() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return errManagerClosed
	}
	return nil
}
func (m *Manager) Configured() bool { return m.vault.Configured() }

// With no wrapper, the extension must retain an existing App selection (or an
// unreadable policy) as a failure, never silently launch personal gh creation.
func (m *Manager) RequiresRouting() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cfg.Enabled || m.loadErr != nil
}

func (m *Manager) View() View {
	hasKeychain := m.hasKeychain() // native stores may prompt; never hold the state lock
	m.mu.Lock()
	defer m.mu.Unlock()
	v := View{Config: m.cfg, Configured: m.vault.Configured(), Unlocked: m.vault.Unlocked() && !m.closed, Keychain: hasKeychain, Verified: m.verified, RoutingAvailable: m.gh != "", Connections: slices.Clone(m.connections)}
	v.Config.Repositories = slices.Clone(m.cfg.Repositories)
	v.Config.Workspaces = slices.Clone(m.cfg.Workspaces)
	v.Config.WorkspaceGenerations = maps.Clone(m.cfg.WorkspaceGenerations)
	if m.app != nil {
		copy := *m.app
		v.App = &copy
	}
	if m.loadErr != nil {
		v.Error = m.loadErr.Error()
	}
	if m.closed {
		v.Error = errManagerClosed.Error()
	}
	return v
}

// Prepare creates or unlocks only this private vault. Without a native store,
// the UI supplies a password, which is not saved in settings or sent to agents.
func (m *Manager) Prepare(password string) error {
	m.operations.Lock()
	defer m.operations.Unlock()
	if err := m.closedError(); err != nil {
		return err
	}
	if m.vault.Unlocked() {
		return nil
	}
	if !m.vault.Configured() {
		generated := password == ""
		if generated {
			if !m.hasKeychain() {
				return errors.New("choose a password for the GitHub App vault")
			}
			b := make([]byte, 32)
			_, _ = rand.Read(b)
			password = base64.RawURLEncoding.EncodeToString(b)
		}
		if err := m.closedError(); err != nil {
			return err
		}
		if err := m.vault.Create(password); err != nil {
			return err
		}
		if m.hasKeychain() {
			key, err := m.passwordKey()
			if err == nil {
				err = m.keychain.Set(key, []byte(password))
			}
			if err != nil && generated {
				_ = m.vault.RemoveEmpty()
				return errors.New("the credential store did not keep the GitHub App vault password")
			}
		}
	} else {
		if password == "" {
			if m.keychain == nil {
				return errors.New("unlock the GitHub App vault with its password")
			}
			key, err := m.passwordKey()
			if err != nil {
				return err
			}
			value, err := m.keychain.Get(key)
			if err != nil || len(value) == 0 {
				return errors.New("unlock the GitHub App vault with its password")
			}
			password = string(value)
		}
		if err := m.closedError(); err != nil {
			return err
		}
		if err := m.vault.Unlock(password); err != nil {
			return err
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		m.vault.Lock()
		return errManagerClosed
	}
	m.invalidateLocked()
	return m.loadClientLocked()
}

func (m *Manager) loadClientLocked() error {
	entries, err := m.vault.Entries()
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		m.app = nil
		return nil
	}
	var app Registration
	var key []byte
	for _, e := range entries {
		switch e.Name {
		case "APP_METADATA":
			if json.Unmarshal([]byte(e.Value), &app) != nil {
				return errors.New("invalid stored GitHub App metadata")
			}
		case "APP_PRIVATE_KEY":
			key = []byte(e.Value)
		}
	}
	// No repository selected is a valid, not-yet-enabled configuration.
	c, err := m.newClient(app, key, m.cfg.Repositories)
	if err != nil {
		return err
	}
	m.app = &app
	m.client = c
	return nil
}

// SaveRegistration is called only inside the Go desktop backend after GitHub's
// manifest exchange or a native file import. It never returns PEM to the page.
func (m *Manager) SaveRegistration(app Registration, key []byte) error {
	if !slugPattern.MatchString(app.Slug) || !ownerPattern.MatchString(app.Owner) || app.URL != "https://github.com/apps/"+app.Slug {
		return errors.New("incomplete GitHub App registration")
	}
	if _, err := ClientFromKey(app, key, nil); err != nil {
		return err
	}
	m.operations.Lock()
	defer m.operations.Unlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return errManagerClosed
	}
	if !m.vault.Unlocked() {
		return errors.New("unlock the GitHub App vault before importing a key")
	}
	cfg := m.cfg
	cfg.Enabled = false
	if err := m.saveConfigLocked(cfg); err != nil {
		return err
	}
	m.invalidateLocked()
	data, _ := json.Marshal(app)
	if err := m.vault.Save([]secrets.Entry{{Name: "APP_METADATA", Value: string(data)}, {Name: "APP_PRIVATE_KEY", Value: string(key)}}); err != nil {
		return errors.New("could not save the encrypted GitHub App key")
	}
	return m.loadClientLocked()
}

func (m *Manager) ImportKey(ctx context.Context, id int64, key []byte) (Registration, error) {
	if err := m.closedError(); err != nil {
		return Registration{}, err
	}
	c, err := m.newClient(Registration{ID: id}, key, nil)
	if err != nil {
		return Registration{}, err
	}
	defer c.Close()
	app, err := c.Inspect(ctx)
	if err != nil {
		return Registration{}, err
	}
	if err := m.SaveRegistration(app, key); err != nil {
		return Registration{}, err
	}
	return app, nil
}

func (m *Manager) invalidateLocked() {
	m.cancel()
	m.epoch, m.cancel = context.WithCancel(context.Background())
	m.revision++
	if m.client != nil {
		m.client.Close()
	}
	m.client = nil
	m.verified = false
	m.connections = nil
}
func (m *Manager) Lock() {
	m.operations.Lock()
	defer m.operations.Unlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return
	}
	m.invalidateLocked()
	m.vault.Lock()
}

// Close must not wait for a native keychain prompt held by Prepare. Late
// operations recheck closed before publishing a client, so shutdown cannot
// restore credentials or allow a personal fallback while agents stop.
func (m *Manager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return
	}
	m.closed = true
	m.invalidateLocked()
	m.cancel()
	m.vault.Lock()
}

func (m *Manager) saveConfigLocked(cfg Config) error {
	if m.closed {
		return errManagerClosed
	}
	if err := os.MkdirAll(m.dir, 0700); err != nil {
		return err
	}
	data, _ := json.MarshalIndent(cfg, "", "  ")
	f, err := os.CreateTemp(m.dir, ".config-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(append(data, '\n')); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(f.Name(), filepath.Join(m.dir, "config.json")); err != nil {
		return err
	}
	m.cfg = cfg
	m.loadErr = nil
	return nil
}

// Configure always saves disabled. A fresh Test of this exact scope/key is
// required before Enable. Other Settings and configuration imports do not touch it.
func (m *Manager) Configure(cfg Config) error {
	cfg.Enabled = false
	cfg, err := checkedConfig(cfg)
	if err != nil {
		return err
	}
	m.operations.Lock()
	defer m.operations.Unlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.saveConfigLocked(cfg); err != nil {
		return err
	}
	m.invalidateLocked()
	if m.vault.Unlocked() {
		return m.loadClientLocked()
	}
	return nil
}
func (m *Manager) Enable(enabled bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return errManagerClosed
	}
	if enabled {
		if m.loadErr != nil {
			return m.loadErr
		}
		if !m.verified || m.client == nil || !m.vault.Unlocked() {
			return errors.New("test the selected repositories with the unlocked App before enabling")
		}
		if m.gh == "" {
			return errors.New("GitHub App command routing is unavailable; install GitHub CLI or repair the private runtime wrapper, then restart yesman")
		}
	}
	cfg := m.cfg
	cfg.Enabled = enabled
	if _, err := checkedConfig(cfg); err != nil {
		return err
	}
	if err := m.saveConfigLocked(cfg); err != nil {
		return err
	}
	// Cancel old operations even on disable, but preserve the tested client.
	m.cancel()
	m.epoch, m.cancel = context.WithCancel(context.Background())
	m.revision++
	return nil
}

func joinedContext(parent, epoch context.Context) (context.Context, func()) {
	ctx, cancel := context.WithCancel(parent)
	stop := context.AfterFunc(epoch, cancel)
	if epoch.Err() != nil {
		cancel()
	}
	return ctx, func() { stop(); cancel() }
}
func (m *Manager) Test(ctx context.Context) ([]Connection, error) {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, errManagerClosed
	}
	c, revision, epoch, repos := m.client, m.revision, m.epoch, slices.Clone(m.cfg.Repositories)
	m.mu.Unlock()
	if c == nil {
		return nil, errors.New("import and unlock a GitHub App key first")
	}
	if len(repos) == 0 {
		return nil, errors.New("select repositories to test")
	}
	ctx, cancel := joinedContext(ctx, epoch)
	defer cancel()
	results := make([]Connection, 0, len(repos))
	for _, repo := range repos {
		result, err := c.Test(ctx, repo)
		if err != nil {
			return nil, err
		}
		results = append(results, result)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if revision != m.revision || ctx.Err() != nil {
		return nil, errors.New("GitHub App configuration changed during the connection test; test again")
	}
	m.verified = true
	m.connections = results
	return slices.Clone(results), nil
}

// Mode is public, scoped to the server-authenticated caller's workspace. An
// unreadable config is an error, not permission to use a personal credential.
func (m *Manager) Mode(workspace, generation string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return "", errManagerClosed
	}
	if m.loadErr != nil {
		return "", m.loadErr
	}
	if m.cfg.Enabled && slices.Contains(m.cfg.Workspaces, workspace) {
		if m.cfg.WorkspaceGenerations[workspace] != generation {
			return "", errors.New("this workspace was recreated; save and test its GitHub App scope again")
		}
		return "app", nil
	}
	return "personal", nil
}
func (m *Manager) Create(ctx context.Context, workspace, generation string, in PullInput) (Pull, error) {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return Pull{}, errManagerClosed
	}
	if m.loadErr != nil {
		err := m.loadErr
		m.mu.Unlock()
		return Pull{}, err
	}
	if !m.cfg.Enabled || !slices.Contains(m.cfg.Workspaces, workspace) {
		m.mu.Unlock()
		return Pull{}, errors.New("App identity is no longer enabled for this workspace; retry after checking Settings")
	}
	if m.cfg.WorkspaceGenerations[workspace] != generation {
		m.mu.Unlock()
		return Pull{}, errors.New("this workspace was recreated; save and test its GitHub App scope again")
	}
	c, epoch, gh := m.client, m.epoch, m.gh
	m.mu.Unlock()
	if c == nil {
		return Pull{}, errors.New("the GitHub App key is locked or unavailable; unlock it in Settings")
	}
	ctx, cancel := joinedContext(ctx, epoch)
	defer cancel()
	return c.CreateWithGH(ctx, gh, in)
}

func (m *Manager) Context(workspace, generation string) string {
	mode, err := m.Mode(workspace, generation)
	if err != nil {
		return "\n## GitHub PR identity\n\nyesman's GitHub App settings are unavailable. gh pr create will fail until Settings is repaired; do not bypass it with personal credentials.\n"
	}
	if mode != "app" {
		return ""
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	identity := "the configured GitHub App"
	if m.app != nil {
		identity = m.app.Slug + "[bot]"
	}
	return fmt.Sprintf("\n## GitHub PR identity\n\nyesman routes gh pr create to %s for these selected repositories: %s. Push branches using the existing git credentials first, then use gh pr create with explicit --repo, --head, --base, --title and --body or --body-file (and --draft when appropriate). This operation uses the App even when personal GH_TOKEN is configured; other gh commands and git push retain existing credentials. A locked key or repository outside this list fails without personal fallback. Do not bypass the wrapper with an absolute gh path or another PR-creation API. This does not authorize merging, releasing, reviewing your own PR, or any new repository access.\n", identity, strings.Join(m.cfg.Repositories, ", "))
}
