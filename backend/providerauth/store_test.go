package providerauth

import (
	"errors"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"
)

func TestMemorySecretStore_RoundTrip(t *testing.T) {
	s := NewMemorySecretStore()

	if _, err := s.Get("k"); !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("Get on empty store err = %v, want ErrSecretNotFound", err)
	}
	if err := s.Set("k", "v1"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if v, err := s.Get("k"); err != nil || v != "v1" {
		t.Fatalf("Get = %q, %v; want %q, nil", v, err, "v1")
	}
	if err := s.Set("k", "v2"); err != nil {
		t.Fatalf("Set overwrite: %v", err)
	}
	if v, _ := s.Get("k"); v != "v2" {
		t.Fatalf("Get after overwrite = %q, want v2", v)
	}
	if err := s.Delete("k"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := s.Delete("k"); !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("Delete of missing key err = %v, want ErrSecretNotFound", err)
	}
}

// withFakeKeyring swaps the keyring entry points for the test's duration.
func withFakeKeyring(t *testing.T, get func(service, user string) (string, error), set func(service, user, pass string) error, del func(service, user string) error) {
	t.Helper()
	oldGet, oldSet, oldDel := keyringGet, keyringSet, keyringDelete
	keyringGet, keyringSet, keyringDelete = get, set, del
	t.Cleanup(func() { keyringGet, keyringSet, keyringDelete = oldGet, oldSet, oldDel })
}

func TestKeyringSecretStore_NotFoundMapsToSentinel(t *testing.T) {
	withFakeKeyring(t,
		func(_, _ string) (string, error) { return "", keyring.ErrNotFound },
		func(_, _, _ string) error { return nil },
		func(_, _ string) error { return keyring.ErrNotFound },
	)
	s := NewKeyringSecretStore()
	if _, err := s.Get("k"); !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("Get err = %v, want ErrSecretNotFound", err)
	}
	if err := s.Delete("k"); !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("Delete err = %v, want ErrSecretNotFound", err)
	}
}

// TestKeyringSecretStore_BackendFailureCarriesActionableHint simulates the
// dominant Linux failure (no Secret Service reachable) and verifies the
// wrapped error names the key and an actionable hint, never the secret.
func TestKeyringSecretStore_BackendFailureCarriesActionableHint(t *testing.T) {
	dbusErr := errors.New("dbus: connection refused by rules")
	withFakeKeyring(t,
		func(_, _ string) (string, error) { return "", dbusErr },
		func(_, _, _ string) error { return dbusErr },
		func(_, _ string) error { return dbusErr },
	)
	s := NewKeyringSecretStore()

	for name, fn := range map[string]func() error{
		"get":    func() error { _, err := s.Get("providerauth:chatgpt"); return err },
		"set":    func() error { return s.Set("providerauth:chatgpt", "SECRETDATA") },
		"delete": func() error { return s.Delete("providerauth:chatgpt") },
	} {
		err := fn()
		if err == nil {
			t.Fatalf("%s: expected error, got nil", name)
		}
		if !errors.Is(err, dbusErr) {
			t.Errorf("%s: error should wrap the keyring error, got %v", name, err)
		}
		if !strings.Contains(err.Error(), "providerauth:chatgpt") {
			t.Errorf("%s: error should name the key: %v", name, err)
		}
		if !strings.Contains(err.Error(), "(") || !strings.Contains(err.Error(), ")") {
			t.Errorf("%s: error should carry a parenthesized hint: %v", name, err)
		}
		if strings.Contains(err.Error(), "SECRETDATA") {
			t.Errorf("%s: error leaks the secret value: %v", name, err)
		}
	}
}

// TestKeyringSecretStore_UsesC0wrkService pins the service name every secret
// is stored under.
func TestKeyringSecretStore_UsesC0wrkService(t *testing.T) {
	var gotService string
	withFakeKeyring(t,
		func(_, _ string) (string, error) { return "", keyring.ErrNotFound },
		func(service, _, _ string) error { gotService = service; return nil },
		func(_, _ string) error { return nil },
	)
	s := NewKeyringSecretStore()
	if err := s.Set("k", "v"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if gotService != "c0wrk" {
		t.Fatalf("keyring service = %q, want %q", gotService, "c0wrk")
	}
}
