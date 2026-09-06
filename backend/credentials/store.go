// Package credentials provides isolated encrypted-at-rest storage for
// subscription credentials. It intentionally exposes no model- or RPC-facing
// types: callers receive credentials only after explicitly addressing a
// provider/account pair.
package credentials

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sync"

	"github.com/v0lka/c0wrk/backend/config"
	keyring "github.com/zalando/go-keyring"
)

const (
	keyService  = "c0wrk.subscription-credentials"
	keyAccount  = "encryption-key.v1"
	fileVersion = 1
)

var (
	// ErrNotFound means no credential exists for the provider/account pair.
	ErrNotFound = errors.New("subscription credential not found")
	// ErrUnavailable means the system secure store cannot provide the encryption key.
	ErrUnavailable = errors.New("subscription credentials unavailable; sign in again")
	// ErrInvalidState means encrypted credential data is corrupt, tampered with, or unreadable.
	ErrInvalidState = errors.New("subscription credential state is invalid; sign in again")
	// ErrInvalidIdentity means a provider or account name cannot safely namespace a credential.
	ErrInvalidIdentity = errors.New("invalid subscription credential identity")

	identityPattern = regexp.MustCompile(`\A[a-zA-Z0-9][a-zA-Z0-9._@-]{0,127}\z`)
)

// Credential is the secret material held by the store. It must never be used
// as a log field or RPC/model-visible response.
type Credential struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token,omitempty"`
	ExpiresAt    string `json:"expires_at,omitempty"`
	AccountID    string `json:"account_id,omitempty"`
	Residency    string `json:"residency,omitempty"`
}

// KeyStore represents the OS secure-store operations needed by CredentialStore.
// It is deliberately small so tests can use an in-memory implementation.
type KeyStore interface {
	Get(service, account string) (string, error)
	Set(service, account, secret string) error
	Delete(service, account string) error
}

type systemKeyStore struct{}

func (systemKeyStore) Get(service, account string) (string, error) {
	return keyring.Get(service, account)
}
func (systemKeyStore) Set(service, account, secret string) error {
	return keyring.Set(service, account, secret)
}
func (systemKeyStore) Delete(service, account string) error { return keyring.Delete(service, account) }

// CredentialStore persists each provider/account credential as an AES-256-GCM
// encrypted file below agentDir. The encryption key is generated once and held
// only in the platform secure store, never in config.yaml or the credential file.
type CredentialStore struct {
	agentDir string
	keys     KeyStore
	mu       sync.Mutex
}

// New creates a CredentialStore backed by the platform secure store.
func New(agentDir string) *CredentialStore {
	return NewWithKeyStore(agentDir, systemKeyStore{})
}

// NewWithKeyStore creates a CredentialStore with an injected secure store.
// It is intended for platform adapters and tests.
func NewWithKeyStore(agentDir string, keys KeyStore) *CredentialStore {
	return &CredentialStore{agentDir: agentDir, keys: keys}
}

// Save encrypts and atomically persists credential for provider/account.
func (s *CredentialStore) Save(provider, account string, credential Credential) error {
	if err := validateIdentity(provider, account); err != nil {
		return err
	}
	if credential.AccessToken == "" {
		return ErrInvalidState
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	key, err := s.encryptionKey(true)
	if err != nil {
		return err
	}
	defer zeroBytes(key)
	plaintext, err := json.Marshal(credential)
	if err != nil {
		return ErrInvalidState
	}
	defer zeroBytes(plaintext)

	block, err := aes.NewCipher(key)
	if err != nil {
		return ErrUnavailable
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return ErrUnavailable
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return ErrUnavailable
	}
	ciphertext := gcm.Seal(nil, nonce, plaintext, s.additionalData(provider, account))
	state, err := json.Marshal(encryptedState{Version: fileVersion, Nonce: nonce, Ciphertext: ciphertext})
	if err != nil {
		return ErrInvalidState
	}
	defer zeroBytes(state)
	return s.atomicWrite(s.path(provider, account), state)
}

// Load decrypts the stored credential for provider/account.
func (s *CredentialStore) Load(provider, account string) (Credential, error) {
	if err := validateIdentity(provider, account); err != nil {
		return Credential{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	state, err := os.ReadFile(s.path(provider, account))
	if errors.Is(err, os.ErrNotExist) {
		return Credential{}, ErrNotFound
	}
	if err != nil {
		return Credential{}, ErrInvalidState
	}
	defer zeroBytes(state)

	var encrypted encryptedState
	if err := json.Unmarshal(state, &encrypted); err != nil || encrypted.Version != fileVersion {
		return Credential{}, ErrInvalidState
	}
	key, err := s.encryptionKey(false)
	if err != nil {
		return Credential{}, err
	}
	defer zeroBytes(key)
	block, err := aes.NewCipher(key)
	if err != nil {
		return Credential{}, ErrUnavailable
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return Credential{}, ErrUnavailable
	}
	if len(encrypted.Nonce) != gcm.NonceSize() || len(encrypted.Ciphertext) < gcm.Overhead() {
		return Credential{}, ErrInvalidState
	}
	plaintext, err := gcm.Open(nil, encrypted.Nonce, encrypted.Ciphertext, s.additionalData(provider, account))
	if err != nil {
		return Credential{}, ErrInvalidState
	}
	defer zeroBytes(plaintext)

	var credential Credential
	if err := json.Unmarshal(plaintext, &credential); err != nil || credential.AccessToken == "" {
		return Credential{}, ErrInvalidState
	}
	return credential, nil
}

// Logout removes the credentials for exactly one provider/account pair.
func (s *CredentialStore) Logout(provider, account string) error {
	if err := validateIdentity(provider, account); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	err := os.Remove(s.path(provider, account))
	if err == nil || errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return fmt.Errorf("remove subscription credential: %w", err)
}

type encryptedState struct {
	Version    int    `json:"version"`
	Nonce      []byte `json:"nonce"`
	Ciphertext []byte `json:"ciphertext"`
}

func (s *CredentialStore) encryptionKey(create bool) ([]byte, error) {
	if s.keys == nil {
		return nil, ErrUnavailable
	}
	encoded, err := s.keys.Get(keyService, keyAccount)
	if err == nil {
		key, decodeErr := base64.RawStdEncoding.DecodeString(encoded)
		if decodeErr != nil || len(key) != 32 {
			return nil, ErrUnavailable
		}
		return key, nil
	}
	if !errors.Is(err, keyring.ErrNotFound) || !create {
		return nil, ErrUnavailable
	}

	key := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return nil, ErrUnavailable
	}
	if err := s.keys.Set(keyService, keyAccount, base64.RawStdEncoding.EncodeToString(key)); err != nil {
		zeroBytes(key)
		return nil, ErrUnavailable
	}
	return key, nil
}

func (s *CredentialStore) path(provider, account string) string {
	identity := sha256.Sum256([]byte(provider + "\x00" + account))
	return filepath.Join(config.SubscriptionCredentialsDir(s.agentDir), base64.RawURLEncoding.EncodeToString(identity[:])+".json")
}

func (s *CredentialStore) additionalData(provider, account string) []byte {
	return []byte("c0wrk.subscription-credentials.v1\x00" + provider + "\x00" + account)
}

func (s *CredentialStore) atomicWrite(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create subscription credential directory: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".credential-*")
	if err != nil {
		return fmt.Errorf("create subscription credential file: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("secure subscription credential file: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write subscription credential: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync subscription credential: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close subscription credential: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("replace subscription credential: %w", err)
	}
	return nil
}

func validateIdentity(provider, account string) error {
	if !identityPattern.MatchString(provider) || !identityPattern.MatchString(account) {
		return ErrInvalidIdentity
	}
	return nil
}

func zeroBytes(data []byte) {
	for i := range data {
		data[i] = 0
	}
}
