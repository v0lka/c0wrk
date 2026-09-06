package credentials

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/v0lka/c0wrk/backend/config"
	keyring "github.com/zalando/go-keyring"
)

type memoryKeyStore struct {
	mu        sync.Mutex
	values    map[string]string
	getErr    error
	setErr    error
	deleteErr error
}

func (s *memoryKeyStore) Get(service, account string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.getErr != nil {
		return "", s.getErr
	}
	value, ok := s.values[service+"\x00"+account]
	if !ok {
		return "", keyring.ErrNotFound
	}
	return value, nil
}

func (s *memoryKeyStore) Set(service, account, secret string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.setErr != nil {
		return s.setErr
	}
	if s.values == nil {
		s.values = make(map[string]string)
	}
	s.values[service+"\x00"+account] = secret
	return nil
}

func (s *memoryKeyStore) Delete(service, account string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.deleteErr != nil {
		return s.deleteErr
	}
	delete(s.values, service+"\x00"+account)
	return nil
}

func TestCredentialStore_PersistsEncryptedCredentialAcrossRestart(t *testing.T) {
	keys := &memoryKeyStore{}
	dir := t.TempDir()
	credential := Credential{AccessToken: "token-that-must-never-be-visible", RefreshToken: "refresh-that-must-never-be-visible", ExpiresAt: "2026-10-01T00:00:00Z"}

	first := NewWithKeyStore(dir, keys)
	if err := first.Save("chatgpt", "user-42", credential); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	second := NewWithKeyStore(dir, keys)
	got, err := second.Load("chatgpt", "user-42")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got != credential {
		t.Errorf("Load() = %#v, want %#v", got, credential)
	}

	files, err := os.ReadDir(config.SubscriptionCredentialsDir(dir))
	if err != nil {
		t.Fatalf("ReadDir() error = %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("credential file count = %d, want 1", len(files))
	}
	blob, err := os.ReadFile(filepath.Join(config.SubscriptionCredentialsDir(dir), files[0].Name()))
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	for _, secret := range []string{credential.AccessToken, credential.RefreshToken, "user-42"} {
		if strings.Contains(string(blob), secret) {
			t.Errorf("encrypted file exposed %q", secret)
		}
	}
	info, err := os.Stat(filepath.Join(config.SubscriptionCredentialsDir(dir), files[0].Name()))
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	// Windows has no Unix permission bits — files always report 0666 regardless
	// of the mode requested at creation. The 0600 restriction is enforced by
	// ACL inheritance on Windows, not POSIX bits, so only verify them on
	// Unix-like systems.
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Errorf("credential mode = %o, want 600", info.Mode().Perm())
	}
}

func TestCredentialStore_LogoutRemovesOnlyRequestedIdentity(t *testing.T) {
	keys := &memoryKeyStore{}
	store := NewWithKeyStore(t.TempDir(), keys)
	if err := store.Save("chatgpt", "first", Credential{AccessToken: "first-token"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Save("chatgpt", "second", Credential{AccessToken: "second-token"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Logout("chatgpt", "first"); err != nil {
		t.Fatalf("Logout() error = %v", err)
	}
	if _, err := store.Load("chatgpt", "first"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Load(logged out) error = %v, want ErrNotFound", err)
	}
	got, err := store.Load("chatgpt", "second")
	if err != nil {
		t.Fatalf("Load(other identity) error = %v", err)
	}
	if got.AccessToken != "second-token" {
		t.Errorf("other credential token = %q, want preserved value", got.AccessToken)
	}
}

func TestCredentialStore_ProviderNamespacesPersistAcrossRestartAndLogoutWithoutDisclosure(t *testing.T) {
	keys := &memoryKeyStore{}
	dir := t.TempDir()
	credentials := map[string]Credential{
		"chatgpt": {AccessToken: "chatgpt-access-token", RefreshToken: "chatgpt-refresh-token"},
		"kimi":    {AccessToken: "kimi-access-token", RefreshToken: "kimi-refresh-token"},
	}

	store := NewWithKeyStore(dir, keys)
	for provider, credential := range credentials {
		if err := store.Save(provider, "account", credential); err != nil {
			t.Fatalf("Save(%q) error = %v", provider, err)
		}
	}

	restarted := NewWithKeyStore(dir, keys)
	for provider, want := range credentials {
		got, err := restarted.Load(provider, "account")
		if err != nil {
			t.Fatalf("Load(%q) after restart error = %v", provider, err)
		}
		if got != want {
			t.Errorf("Load(%q) = %#v, want %#v", provider, got, want)
		}
	}
	if err := restarted.Logout("chatgpt", "account"); err != nil {
		t.Fatalf("Logout(chatgpt) error = %v", err)
	}
	if _, err := restarted.Load("chatgpt", "account"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Load(chatgpt) after logout error = %v, want ErrNotFound", err)
	}
	if got, err := restarted.Load("kimi", "account"); err != nil || got != credentials["kimi"] {
		t.Errorf("Load(kimi) after chatgpt logout = %#v, %v; want %#v, nil", got, err, credentials["kimi"])
	}

	entries, err := os.ReadDir(config.SubscriptionCredentialsDir(dir))
	if err != nil {
		t.Fatalf("ReadDir() error = %v", err)
	}
	for _, entry := range entries {
		blob, readErr := os.ReadFile(filepath.Join(config.SubscriptionCredentialsDir(dir), entry.Name()))
		if readErr != nil {
			t.Fatalf("ReadFile(%q) error = %v", entry.Name(), readErr)
		}
		for _, credential := range credentials {
			for _, secret := range []string{credential.AccessToken, credential.RefreshToken} {
				if strings.Contains(string(blob), secret) {
					t.Errorf("credential file %q disclosed a token", entry.Name())
				}
			}
		}
	}
}

func TestCredentialStore_CorruptionAndKeyLossRequireReloginWithoutSecretDisclosure(t *testing.T) {
	keys := &memoryKeyStore{}
	dir := t.TempDir()
	store := NewWithKeyStore(dir, keys)
	const token = "sensitive-token-must-not-appear"
	if err := store.Save("chatgpt", "account", Credential{AccessToken: token}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.path("chatgpt", "account"), []byte("not-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := store.Load("chatgpt", "account")
	if !errors.Is(err, ErrInvalidState) {
		t.Errorf("corrupt Load() error = %v, want ErrInvalidState", err)
	}
	assertNoSecret(t, token, err)

	if err := os.WriteFile(store.path("chatgpt", "account"), []byte(`{"version":1,"nonce":"","ciphertext":""}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = store.Load("chatgpt", "account")
	if !errors.Is(err, ErrInvalidState) {
		t.Errorf("malformed encrypted Load() error = %v, want ErrInvalidState", err)
	}
	assertNoSecret(t, token, err)

	if err := store.Save("chatgpt", "account", Credential{AccessToken: token}); err != nil {
		t.Fatal(err)
	}
	keys.values = nil // Simulate loss of the OS secure-store entry after restart.
	_, err = NewWithKeyStore(dir, keys).Load("chatgpt", "account")
	if !errors.Is(err, ErrUnavailable) {
		t.Errorf("key-loss Load() error = %v, want ErrUnavailable", err)
	}
	assertNoSecret(t, token, err)
}

func TestCredentialStore_SecretsNeverAppearInErrorsOrConfig(t *testing.T) {
	keys := &memoryKeyStore{setErr: errors.New("secure store rejected sensitive-token")}
	store := NewWithKeyStore(t.TempDir(), keys)
	const token = "sensitive-token"
	err := store.Save("chatgpt", "account", Credential{AccessToken: token})
	if !errors.Is(err, ErrUnavailable) {
		t.Errorf("Save() error = %v, want ErrUnavailable", err)
	}
	assertNoSecret(t, token, err)

	cfg := config.Config{}
	config.ApplyDefaults(&cfg)
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := config.Save(&cfg, configPath); err != nil {
		t.Fatalf("config.Save() error = %v", err)
	}
	contents, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(contents), token) || strings.Contains(string(contents), "subscription-credentials") {
		t.Error("config.yaml must not contain subscription credential material or state")
	}
}

func TestCredentialStore_RejectsUnsafeIdentity(t *testing.T) {
	store := NewWithKeyStore(t.TempDir(), &memoryKeyStore{})
	if err := store.Save("../../chatgpt", "account", Credential{AccessToken: "token"}); !errors.Is(err, ErrInvalidIdentity) {
		t.Errorf("Save() error = %v, want ErrInvalidIdentity", err)
	}
}

func assertNoSecret(t *testing.T, secret string, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), secret) {
		t.Errorf("error disclosed secret: %q", err)
	}
}
