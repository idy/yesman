package main

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"log"

	"github.com/egoist/mygo"
)

// The secrets' password is random, made when they are set up, and kept in
// the system's credential store (mygo.Secrets: the login Keychain on
// macOS, the Credential Manager on Windows, the Secret Service on Linux),
// which unlocks them when yesman starts. The configuration file carries it
// to another computer. Where there is no credential store, the user
// chooses the password and types it to unlock.

// keychain keeps small secrets for the user: mygo.Secrets in the app, a
// map in tests, nil for none.
type keychain interface {
	Get(key string) ([]byte, error)
	Set(key string, value []byte) error
	Delete(key string) error
}

// keychainKey is where the credential store keeps the password of the
// secrets encrypted to recipient, an age recipient the secrets file names
// in the clear: the data folder can move, and two data folders keep two.
func keychainKey(recipient string) string { return "secrets-password/" + recipient }

// randomPassword is a password for the secrets: 256 random bits.
func randomPassword() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand does not fail
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// hasKeychain says whether the system has a credential store.
func (a *app) hasKeychain() bool {
	if a.keychain == nil {
		return false
	}
	_, err := a.keychain.Get("secrets-password/probe")
	return !errors.Is(err, errors.ErrUnsupported)
}

// storedPassword is the password the credential store keeps for the
// secrets, and the recipient it is kept for, or "".
func (a *app) storedPassword() (password, recipient string) {
	if a.keychain == nil {
		return "", ""
	}
	recipients, err := a.vault.Recipients()
	if err != nil {
		return "", ""
	}
	if r, err := a.vault.Recipient(); err == nil {
		recipients = append([]string{r}, recipients...) // its own first
	}
	for _, r := range recipients {
		if p, err := a.keychain.Get(keychainKey(r)); err == nil && len(p) > 0 {
			return string(p), r
		}
	}
	return "", ""
}

// keepPassword keeps the password of the unlocked secrets in the
// credential store, when there is one.
func (a *app) keepPassword(password string) error {
	if a.keychain == nil {
		return nil
	}
	r, err := a.vault.Recipient()
	if err != nil {
		return err
	}
	err = a.keychain.Set(keychainKey(r), []byte(password))
	if errors.Is(err, errors.ErrUnsupported) {
		return nil
	}
	return err
}

// unlockFromKeychain unlocks the secrets with the password the credential
// store keeps, and says whether it did.
func (a *app) unlockFromKeychain() bool {
	if !a.vault.Configured() || a.vault.Unlocked() {
		return a.vault.Unlocked()
	}
	password, _ := a.storedPassword()
	if password == "" {
		return false
	}
	if err := a.vault.Unlock(password); err != nil {
		log.Print("secrets: unlocking with the keychain's password: ", trError(err))
		return false
	}
	return true
}

// appKeychain is the app's credential store.
func appKeychain() keychain { return mygo.Secrets }

// setUpSecrets sets the secrets up: with a random password kept in the
// credential store, or with the one the user chose where there is none.
func (a *app) setUpSecrets(password string) error {
	random := password == ""
	if random {
		if !a.hasKeychain() {
			return errors.New(tr("this system keeps no passwords for apps: choose a password for the secrets"))
		}
		password = randomPassword()
	}
	if err := a.vault.Create(password); err != nil {
		return trError(err)
	}
	if err := a.keepPassword(password); err != nil {
		if random {
			// Nobody knows this password: set up nothing.
			_ = a.vault.RemoveEmpty()
			return errors.New(tr("the keychain did not keep the password: %s", err))
		}
		log.Print("secrets: keeping the password in the keychain: ", err)
	}
	return nil
}
