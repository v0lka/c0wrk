package providerauth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/v0lka/c0wrk/core/version"
)

func TestKimiDeviceHeadersStableAcrossManagers(t *testing.T) {
	dir := t.TempDir()
	manager, err := NewTokenManager(ManagerConfig{Profile: Kimi(), Store: NewMemorySecretStore(), AgentDir: dir})
	if err != nil {
		t.Fatalf("NewTokenManager: %v", err)
	}
	headers := manager.profile.TokenHeaders
	if headers["X-Msh-Platform"] != "c0wrk" || headers["X-Msh-Version"] != version.Version {
		t.Errorf("platform headers = %v", headers)
	}
	if headers["X-Msh-Device-Model"] != runtime.GOOS+" "+runtime.GOARCH || headers["X-Msh-Os-Version"] != runtime.GOOS+"/"+runtime.GOARCH {
		t.Errorf("OS headers = %v", headers)
	}
	for name, value := range headers {
		for _, r := range value {
			if r > 127 {
				t.Errorf("%s contains non-ASCII value %q", name, value)
			}
		}
	}

	id := headers["X-Msh-Device-Id"]
	if _, err := uuid.Parse(id); err != nil {
		t.Fatalf("Device ID = %q, want UUID: %v", id, err)
	}
	info, err := os.Stat(filepath.Join(dir, "device_id"))
	if err != nil {
		t.Fatalf("stat device_id: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("device_id mode = %o, want 600", got)
	}

	restored, err := NewTokenManager(ManagerConfig{Profile: Kimi(), Store: NewMemorySecretStore(), AgentDir: dir})
	if err != nil {
		t.Fatalf("restore NewTokenManager: %v", err)
	}
	if got := restored.profile.TokenHeaders["X-Msh-Device-Id"]; got != id {
		t.Errorf("Device ID changed after restart: %q → %q", id, got)
	}
	other, err := NewTokenManager(ManagerConfig{Profile: Kimi(), Store: NewMemorySecretStore(), AgentDir: t.TempDir()})
	if err != nil {
		t.Fatalf("other NewTokenManager: %v", err)
	}
	if got := other.profile.TokenHeaders["X-Msh-Device-Id"]; got == id {
		t.Errorf("Device ID is shared across agent dirs: %q", got)
	}
}

func TestKimiDeviceHeaderASCIISanitization(t *testing.T) {
	if got := asciiHeader("host-имя"); got != "host-" {
		t.Errorf("asciiHeader = %q, want host-", got)
	}
	if got := asciiHeader("имя"); got != "unknown" {
		t.Errorf("asciiHeader fallback = %q, want unknown", got)
	}
}

func TestKimiDeviceHeadersReachRefresh(t *testing.T) {
	manager, err := NewTokenManager(ManagerConfig{Profile: Kimi(), Store: NewMemorySecretStore(), AgentDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, name := range []string{"X-Msh-Platform", "X-Msh-Version", "X-Msh-Device-Name", "X-Msh-Device-Model", "X-Msh-Os-Version", "X-Msh-Device-Id"} {
			if got := r.Header.Get(name); got != manager.profile.TokenHeaders[name] {
				t.Errorf("%s = %q, want %q", name, got, manager.profile.TokenHeaders[name])
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"at","refresh_token":"rt","token_type":"Bearer","expires_in":60}`))
	}))
	t.Cleanup(srv.Close)
	profile := manager.profile
	profile.Issuer = srv.URL
	if _, err := refreshTokens(context.Background(), srv.Client(), profile, "secret"); err != nil {
		t.Fatalf("refreshTokens: %v", err)
	}
}

func TestKimiDeviceIDRepairsPermissions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "device_id")
	id := uuid.NewString()
	if err := os.WriteFile(path, []byte(id+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	headers, err := deviceHeaders(dir)
	if err != nil {
		t.Fatal(err)
	}
	if headers["X-Msh-Device-Id"] != id {
		t.Fatalf("Device ID = %q, want %q", headers["X-Msh-Device-Id"], id)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("device_id mode after read = %o, want 600", got)
	}
}

func TestKimiDeviceIDConcurrentCreation(t *testing.T) {
	dir := t.TempDir()
	const workers = 16
	start := make(chan struct{})
	ids := make(chan string, workers)
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			headers, err := deviceHeaders(dir)
			if err != nil {
				errs <- err
				return
			}
			ids <- headers["X-Msh-Device-Id"]
		}()
	}
	close(start)
	wg.Wait()
	close(ids)
	close(errs)
	for err := range errs {
		t.Errorf("deviceHeaders: %v", err)
	}
	var first string
	for id := range ids {
		if first == "" {
			first = id
		}
		if id != first {
			t.Fatalf("concurrent Device IDs differ: %q and %q", first, id)
		}
	}
}
