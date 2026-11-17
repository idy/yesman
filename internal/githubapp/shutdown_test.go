package githubapp

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

type promptingKeychain struct {
	base    *testKeychain
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (k *promptingKeychain) Get(name string) ([]byte, error) {
	if strings.HasPrefix(name, "github-app-password/") && !strings.HasSuffix(name, "/probe") {
		k.once.Do(func() { close(k.started) })
		<-k.release
	}
	return k.base.Get(name)
}
func (k *promptingKeychain) Set(name string, value []byte) error { return k.base.Set(name, value) }
func (k *promptingKeychain) Delete(name string) error            { return k.base.Delete(name) }

func TestCloseDoesNotWaitForNativePromptOrRestoreCredentials(t *testing.T) {
	manager, app, key, base := managerFixture(t, nil)
	configureManager(t, manager, app, key)
	manager.Lock()
	prompt := &promptingKeychain{base: base, started: make(chan struct{}), release: make(chan struct{})}
	manager.keychain = prompt
	var release sync.Once
	finishPrompt := func() { release.Do(func() { close(prompt.release) }) }
	t.Cleanup(finishPrompt)
	unlocked := make(chan error, 1)
	go func() { unlocked <- manager.Prepare("") }()
	<-prompt.started
	closed := make(chan struct{})
	go func() { manager.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		finishPrompt()
		t.Fatal("shutdown waited for a native credential prompt")
	}
	finishPrompt()
	if err := <-unlocked; !errors.Is(err, errManagerClosed) {
		t.Fatalf("late unlock: %v", err)
	}
	if manager.vault.Unlocked() {
		t.Fatal("late native reply restored the key")
	}
	if mode, err := manager.Mode("alice", fixtureGeneration); !errors.Is(err, errManagerClosed) || mode != "" {
		t.Fatal("closed service allowed App use or personal fallback")
	}
	if err := manager.Enable(false); !errors.Is(err, errManagerClosed) {
		t.Fatal("closed service accepted a mutation")
	}
}
