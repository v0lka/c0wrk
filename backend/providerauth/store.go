package providerauth

import (
	"errors"
	"fmt"
	"runtime"
	"sync"

	"github.com/zalando/go-keyring"
)

// keyringService is the OS-keychain service name every c0wrk secret is stored
// under. Individual secrets are addressed by user ("account") keys such as
// "providerauth:chatgpt".
const keyringService = "c0wrk"

// ErrSecretNotFound reports that no secret is stored under the requested key
// (e.g. the provider is signed out).
var ErrSecretNotFound = errors.New("providerauth: secret not found")

// SecretStore persists opaque secret strings keyed by name. The production
// implementation is the OS keychain; tests use MemorySecretStore. Values are
// treated as confidential: implementations must never log them.
//
// All methods must be safe for concurrent use.
type SecretStore interface {
	// Get returns the secret stored under key, or ErrSecretNotFound when none is.
	Get(key string) (string, error)
	// Set stores secret under key, replacing any previous value.
	Set(key, secret string) error
	// Delete removes the secret under key. Deleting a missing key returns
	// ErrSecretNotFound so callers can distinguish a no-op from a failure.
	Delete(key string) error
}

// KeyringSecretStore stores secrets in the OS keychain (macOS Keychain,
// Windows Credential Manager, Linux Secret Service via go-keyring).
type KeyringSecretStore struct {
	service string
}

// NewKeyringSecretStore returns a store under the c0wrk keychain service.
func NewKeyringSecretStore() *KeyringSecretStore {
	return &KeyringSecretStore{service: keyringService}
}

// keyringGet/keyringSet/keyringDelete are the go-keyring entry points, kept
// as variables so tests can exercise the wrapper (error mapping, hints)
// without touching the real OS keychain.
var (
	keyringGet    = keyring.Get
	keyringSet    = keyring.Set
	keyringDelete = keyring.Delete
)

// Get reads a secret from the OS keychain. A missing entry maps to
// ErrSecretNotFound; backend failures (most commonly a Linux desktop without
// a running Secret Service) return an error carrying an actionable hint.
// The secret value is never part of the returned error.
func (s *KeyringSecretStore) Get(key string) (string, error) {
	secret, err := keyringGet(s.service, key)
	if err != nil {
		if errors.Is(err, keyring.ErrNotFound) {
			return "", ErrSecretNotFound
		}
		return "", fmt.Errorf("providerauth: reading %q from the OS keychain: %w%s", key, err, keyringHint())
	}
	return secret, nil
}

// Set stores a secret in the OS keychain.
func (s *KeyringSecretStore) Set(key, secret string) error {
	if err := keyringSet(s.service, key, secret); err != nil {
		return fmt.Errorf("providerauth: writing %q to the OS keychain: %w%s", key, err, keyringHint())
	}
	return nil
}

// Delete removes a secret from the OS keychain.
func (s *KeyringSecretStore) Delete(key string) error {
	err := keyringDelete(s.service, key)
	if err != nil {
		if errors.Is(err, keyring.ErrNotFound) {
			return ErrSecretNotFound
		}
		return fmt.Errorf("providerauth: deleting %q from the OS keychain: %w%s", key, err, keyringHint())
	}
	return nil
}

// keyringHint appends a platform-appropriate remediation note to keychain
// errors. On Linux the dominant cause is a missing or locked Secret Service
// (gnome-keyring); the other platforms surface their native managers.
func keyringHint() string {
	switch runtime.GOOS {
	case "linux":
		return " (no Secret Service reachable: install and unlock gnome-keyring or another libsecret provider, then sign in again)"
	case "windows":
		return " (check that the Windows Credential Manager is available)"
	default:
		return " (check that the login keychain is unlocked)"
	}
}

// MemorySecretStore is an in-process SecretStore for tests and tools. It is
// safe for concurrent use and never touches the OS keychain.
type MemorySecretStore struct {
	mu sync.Mutex
	m  map[string]string
}

// NewMemorySecretStore returns an empty in-memory store.
func NewMemorySecretStore() *MemorySecretStore {
	return &MemorySecretStore{m: make(map[string]string)}
}

// Get returns the stored secret or ErrSecretNotFound.
func (s *MemorySecretStore) Get(key string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	secret, ok := s.m[key]
	if !ok {
		return "", ErrSecretNotFound
	}
	return secret, nil
}

// Set stores the secret, replacing any previous value.
func (s *MemorySecretStore) Set(key, secret string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[key] = secret
	return nil
}

// Delete removes the secret, returning ErrSecretNotFound when absent.
func (s *MemorySecretStore) Delete(key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.m[key]; !ok {
		return ErrSecretNotFound
	}
	delete(s.m, key)
	return nil
}

// compile-time interface checks.
var (
	_ SecretStore = (*KeyringSecretStore)(nil)
	_ SecretStore = (*MemorySecretStore)(nil)
)
