// Package keychain keeps secrets in the OS keychain: macOS Keychain, Windows Credential Manager,
// or the Secret Service (D-Bus) on Linux.
package keychain

import (
	"errors"
	"sync"

	"github.com/zalando/go-keyring"
)

// service names every entry airlock writes.
const service = "airlock"

// probeAccount is looked up to tell whether the keychain answers; it is never written.
const probeAccount = "airlock-availability-probe"

// ErrNotFound reports that no secret is stored for the account.
var ErrNotFound = errors.New("no secret in the keychain")

// Store keeps one secret per account.
type Store interface {
	// Get returns the secret of account, or ErrNotFound.
	Get(account string) (string, error)
	// Set stores secret for account, replacing any previous one.
	Set(account, secret string) error
	// Delete removes the secret of account; a missing one is not an error.
	Delete(account string) error
	// DeleteAll removes every secret airlock stored; none is not an error.
	DeleteAll() error
}

type osStore struct{}

// NewOSStore returns the Store backed by the OS keychain.
func NewOSStore() Store { return osStore{} }

func (osStore) Get(account string) (string, error) {
	secret, err := keyring.Get(service, account)
	if errors.Is(err, keyring.ErrNotFound) {
		return "", ErrNotFound
	}
	return secret, err
}

func (osStore) Set(account, secret string) error {
	return keyring.Set(service, account, secret)
}

func (osStore) Delete(account string) error {
	if err := keyring.Delete(service, account); err != nil && !errors.Is(err, keyring.ErrNotFound) {
		return err
	}
	return nil
}

func (osStore) DeleteAll() error {
	if err := keyring.DeleteAll(service); err != nil && !errors.Is(err, keyring.ErrNotFound) {
		return err
	}
	return nil
}

// Available reports whether s answers: a lookup either finds the probe entry or reports it missing.
func Available(s Store) bool {
	if s == nil {
		return false
	}
	_, err := s.Get(probeAccount)
	return err == nil || errors.Is(err, ErrNotFound)
}

// MemoryStore is an in-memory Store for tests; a non-nil Err fails every call.
type MemoryStore struct {
	Err     error
	secrets map[string]string
	mu      sync.Mutex
}

// NewMemoryStore returns an empty MemoryStore.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{secrets: map[string]string{}}
}

func (m *MemoryStore) Get(account string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Err != nil {
		return "", m.Err
	}
	secret, ok := m.secrets[account]
	if !ok {
		return "", ErrNotFound
	}
	return secret, nil
}

func (m *MemoryStore) Set(account, secret string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Err != nil {
		return m.Err
	}
	m.secrets[account] = secret
	return nil
}

func (m *MemoryStore) Delete(account string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Err != nil {
		return m.Err
	}
	delete(m.secrets, account)
	return nil
}

func (m *MemoryStore) DeleteAll() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Err != nil {
		return m.Err
	}
	clear(m.secrets)
	return nil
}
