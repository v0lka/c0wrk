// SPDX-License-Identifier: Apache-2.0
//
// webaccess_connection_test.go — the connection surface of the browser
// web-access sidecar (ADR-101), driven directly through the
// transport-neutral WebAccessService (the tests moved here from the desktop
// package with their code, P-11): SetConnection pins the bind/port,
// RegenerateToken rotates the persistent credential, and SetToken persists
// an operator-typed one. All persist to config.yaml through the same
// immediate write every settings change uses, apply live via a hot restart,
// and roll back when the new state cannot serve. The credential itself is
// minted once at the first start of an empty-token section and is
// deliberately NOT writable through the connection pin — the dedicated token
// RPCs are its only writers besides generation.

package backend

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/internal/webaccess"
)

// sidecarPort extracts the resolved listen port of a running sidecar, failing
// the test when it has none.
func sidecarPort(t *testing.T, s *WebAccessService) int {
	t.Helper()
	srv := s.server.Load()
	if srv == nil {
		t.Fatal("sidecar is not running")
	}
	addr, ok := srv.Addr()
	if !ok {
		t.Fatal("running sidecar has no resolved address")
	}
	_, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("sidecar address %q: %v", addr, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("sidecar port %q: %v", portStr, err)
	}
	return port
}

// sidecarBind extracts the host part of a running sidecar's resolved listen
// address, failing the test when it has none.
func sidecarBind(t *testing.T, s *WebAccessService) string {
	t.Helper()
	srv := s.server.Load()
	if srv == nil {
		t.Fatal("sidecar is not running")
	}
	addr, ok := srv.Addr()
	if !ok {
		t.Fatal("running sidecar has no resolved address")
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("sidecar address %q: %v", addr, err)
	}
	return host
}

// dialBind reports whether anything accepts TCP on host:port within a bounded
// wait — the bind-aware sibling of dialLoopback (which is pinned to
// 127.0.0.1 and cannot see a sidecar bound to ::1 only).
func dialBind(host string, port int) bool {
	dialer := &net.Dialer{Timeout: 500 * time.Millisecond}
	conn, err := dialer.DialContext(context.Background(), "tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// mintedToken reloads the config file and returns the persisted credential,
// failing the test unless it is a plausible 64-hex mint.
func mintedToken(t *testing.T, path string) string {
	t.Helper()
	reloaded, err := config.Load(path)
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}
	tok := reloaded.WebAccess.Token
	if len(tok) != 64 {
		t.Fatalf("persisted token = %q (len %d), want a 64-hex mint", tok, len(tok))
	}
	return tok
}

// configTokenOnDisk reloads config.yaml and returns the persisted web access
// token as-is — the mint-shape-asserting sibling above is for generated
// credentials; the operator-typed ones written through SetToken are arbitrary
// strings within the validation bounds.
func configTokenOnDisk(t *testing.T, path string) string {
	t.Helper()
	reloaded, err := config.Load(path)
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}
	return reloaded.WebAccess.Token
}

// newWebAccessHTTPTest serves the sidecar's real handler (auth included) from
// an httptest server.
func newWebAccessHTTPTest(t *testing.T, srv *webaccess.Server) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts
}

// loginWebAccessClient performs the token login so the returned client holds
// the session cookie an authenticated browser tab would carry.
func loginWebAccessClient(t *testing.T, baseURL, token string) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookiejar: %v", err)
	}
	client := &http.Client{Jar: jar}
	loginBody, err := json.Marshal(map[string]string{"token": token})
	if err != nil {
		t.Fatalf("marshal login body: %v", err)
	}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost,
		baseURL+"/api/login", strings.NewReader(string(loginBody)))
	if err != nil {
		t.Fatalf("build login request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("login status = %d, want 200 (body: %s)", resp.StatusCode, body)
	}
	return client
}

// TestSetWebAccessConnection_PinsAndHotRestarts: an auto-port sidecar is
// restarted onto the pinned port; the pin persists and applies live while
// the credential minted at the first start stays exactly as it was — the
// connection pin never touches the token (only minting and rotation write it).
func TestSetWebAccessConnection_PinsAndHotRestarts(t *testing.T) {
	pinned := reserveLoopbackPort(t)
	cfg, path := writeWebAccessConfig(t, "web_access:\n  enabled: true\n")
	s := newTestWebAccessService(t, cfg, path)
	mountErr := s.MountWebAccess(s.log())
	first := s.server.Load()
	if first == nil {
		t.Fatalf("sidecar did not mount: %v", mountErr)
	}
	// The first start minted the credential and persisted it immediately —
	// the same config write every other settings change performs.
	minted := mintedToken(t, path)
	if minted != first.AuthToken() {
		t.Errorf("live token differs from the persisted mint: %q vs %q", first.AuthToken(), minted)
	}

	if err := s.SetConnection("", pinned); err != nil {
		t.Fatalf("SetConnection: %v", err)
	}
	if s.server.Load() == first {
		t.Error("hot restart reused the old server instead of building a new one")
	}
	// Start binds synchronously before returning, so the new listener is
	// already accepting once the call returned.
	if !dialLoopback(pinned) {
		t.Fatalf("sidecar is not listening on the pinned port %d", pinned)
	}

	reloaded, err := config.Load(path)
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}
	// An empty bind resolves to the loopback default, which is then the
	// persisted shape; the token survived the pin untouched.
	if reloaded.WebAccess.Bind != config.DefaultWebAccessBind ||
		reloaded.WebAccess.Port != pinned || reloaded.WebAccess.Token != minted {
		t.Errorf("disk pin = (bind %q, port %d, token %q), want (%q, %d, the minted token)",
			reloaded.WebAccess.Bind, reloaded.WebAccess.Port, reloaded.WebAccess.Token,
			config.DefaultWebAccessBind, pinned)
	}

	// The minted token is the live credential: a browser login with it works.
	info := s.Snapshot()
	if !info.Running || info.ConfiguredPort != pinned || !info.TokenPinned || info.Token != minted {
		t.Errorf("info = %+v, want running, configured_port=%d, token_pinned, token=minted", info, pinned)
	}
	if info.ConfiguredBind != config.DefaultWebAccessBind || !info.BindIsLoopback {
		t.Errorf("info bind = (%q, loopback=%v), want (%q, true)",
			info.ConfiguredBind, info.BindIsLoopback, config.DefaultWebAccessBind)
	}
	ts := newWebAccessHTTPTest(t, s.server.Load())
	client := loginWebAccessClient(t, ts.URL, minted)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, ts.URL+"/api/transport", http.NoBody)
	if err != nil {
		t.Fatalf("build GET /api/transport: %v", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("authenticated GET /api/transport: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		t.Errorf("authenticated transport probe status = %d, want 200", resp.StatusCode)
	}
}

// TestWebAccessTokenMintedOnFirstStartAndStableAcrossRestart: an empty config
// token is minted at the first start and persisted immediately, so the
// credential — and therefore every browser login — survives restarts from
// the very first session on.
func TestWebAccessTokenMintedOnFirstStartAndStableAcrossRestart(t *testing.T) {
	cfg, path := writeWebAccessConfig(t, "web_access:\n  enabled: true\n")
	s := newTestWebAccessService(t, cfg, path)
	mountErr := s.MountWebAccess(s.log())
	srv := s.server.Load()
	if srv == nil {
		t.Fatalf("sidecar did not mount: %v", mountErr)
	}
	minted := mintedToken(t, path)
	if minted != srv.AuthToken() {
		t.Errorf("live token %q != the persisted mint %q", srv.AuthToken(), minted)
	}
	if info := s.Snapshot(); !info.TokenPinned || info.Token != minted {
		t.Errorf("info = %+v, want token_pinned with the minted credential", info)
	}

	// A fresh service on the same config starts with the SAME credential: no
	// per-launch re-mint, no browser re-login after an app restart.
	restarted, err := config.Load(path)
	if err != nil {
		t.Fatalf("load config for restart: %v", err)
	}
	b := newTestWebAccessService(t, restarted, path)
	restartErr := b.MountWebAccess(b.log())
	srv2 := b.server.Load()
	if srv2 == nil {
		t.Fatalf("sidecar did not mount after the restart: %v", restartErr)
	}
	if srv2.AuthToken() != minted {
		t.Errorf("credential did not survive the restart: %q, want %q", srv2.AuthToken(), minted)
	}
}

// TestWebAccessTokenMintFailureAbortsStart: the mint's immediate persist is
// part of the contract — when it cannot land, the sidecar does not start at
// all (no silent degradation into a credential that would vanish on restart).
// Adaptation note (P-11): the desktop initWebAccess adapter turned this
// failure into its own Error record ("... continuing without it") and the
// test asserted that record; the service instead RETURNS the mount error and
// the host adapter owns the fail-soft loudness — so the moved test asserts
// the returned error (the same persist-failure phrase) and that the service
// itself stays silent.
func TestWebAccessTokenMintFailureAbortsStart(t *testing.T) {
	cfg, path := writeWebAccessConfig(t, "web_access:\n  enabled: true\n")
	// The in-memory config stays valid; the FILE becomes a directory so the
	// mint's immediate config write cannot land.
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove config file: %v", err)
	}
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatalf("replace config file with a directory: %v", err)
	}

	var logSink strings.Builder
	s, seams := newWebAccessServiceWithSeams(t, cfg, path)
	log := slog.New(slog.NewTextHandler(&logSink, &slog.HandlerOptions{Level: slog.LevelError}))
	seams.logger.Store(log)

	mountErr := s.MountWebAccess(log)
	if s.server.Load() != nil {
		t.Fatal("the sidecar mounted although the minted token could not be persisted")
	}
	if mountErr == nil || !strings.Contains(mountErr.Error(), "failed to persist the generated auth token") {
		t.Errorf("mount error = %v, want the persist-failure message", mountErr)
	}
	if logSink.Len() != 0 {
		t.Errorf("the service logged %q on the mount failure — it must stay silent and let the host adapter decide loudness", logSink.String())
	}
	// The mint rolled back in memory: the next start retries the mint, not
	// a half-applied state.
	if cfg.WebAccess.Token != "" {
		t.Errorf("live config kept the rolled-back token %q", cfg.WebAccess.Token)
	}
}

// TestSetWebAccessConnection_RestartFailureRestoresPreviousPin: a pin whose
// port is already taken must not take the sidecar down — the previous pin is
// restored on disk and the sidecar comes back on it, with the failure named.
func TestSetWebAccessConnection_RestartFailureRestoresPreviousPin(t *testing.T) {
	occupied := reserveLoopbackPort(t)
	// Hold the port for the whole call: the restart onto it must fail.
	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(occupied)))
	if err != nil {
		t.Fatalf("occupy port %d: %v", occupied, err)
	}
	defer func() { _ = ln.Close() }()

	cfg, path := writeWebAccessConfig(t, "web_access:\n  enabled: true\n  token: keep-me\n")
	s := newTestWebAccessService(t, cfg, path)
	mountErr := s.MountWebAccess(s.log())
	before := s.server.Load()
	if before == nil {
		t.Fatalf("sidecar did not mount: %v", mountErr)
	}

	if err := s.SetConnection("", occupied); err == nil {
		t.Fatal("expected an error when the pinned port is already taken")
	} else if !strings.Contains(err.Error(), "previous settings were restored") {
		t.Errorf("error = %v, want the restored-previous-settings message", err)
	}

	restored := s.server.Load()
	if restored == nil {
		t.Fatal("sidecar did not come back after the rollback")
	}
	if restored == before {
		t.Error("rollback reused the stopped server instead of building a new one")
	}
	// Port 0 = auto: the restored sidecar legitimately binds a fresh free
	// port, so the assertion is "listening", not "the same port".
	portAfter := sidecarPort(t, s)
	if !dialLoopback(portAfter) {
		t.Errorf("restored sidecar is not listening on its resolved port %d", portAfter)
	}
	reloaded, err := config.Load(path)
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}
	if reloaded.WebAccess.Port != 0 || reloaded.WebAccess.Token != "keep-me" {
		t.Errorf("disk after rollback = (port %d, token %q), want (0, keep-me)",
			reloaded.WebAccess.Port, reloaded.WebAccess.Token)
	}
	if reloaded.WebAccess.Bind != config.DefaultWebAccessBind {
		t.Errorf("bind after rollback = %q, want the untouched default %q",
			reloaded.WebAccess.Bind, config.DefaultWebAccessBind)
	}
	if info := s.Snapshot(); !info.Running || !info.TokenPinned {
		t.Errorf("info after rollback = %+v, want running with token_pinned=true (the original pin)", info)
	}
}

// TestSetWebAccessConnection_ValidationRefused: an out-of-range port is
// rejected with the shared message and mutates neither disk nor runtime.
func TestSetWebAccessConnection_ValidationRefused(t *testing.T) {
	cfg, path := writeWebAccessConfig(t, "web_access:\n  enabled: true\n")
	s := newTestWebAccessService(t, cfg, path)
	mountErr := s.MountWebAccess(s.log())
	srv := s.server.Load()
	if srv == nil {
		t.Fatalf("sidecar did not mount: %v", mountErr)
	}
	statBefore, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat config: %v", err)
	}

	for _, port := range []int{-1, 65536} {
		if err := s.SetConnection("", port); err == nil {
			t.Errorf("SetConnection(%d): expected a validation error", port)
		} else if !strings.Contains(err.Error(), "out of range") {
			t.Errorf("SetConnection(%d) error = %v, want the out-of-range message", port, err)
		}
	}
	if s.server.Load() != srv {
		t.Error("a rejected validation rebuilt the sidecar")
	}
	statAfter, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat config after rejection: %v", err)
	}
	if !statBefore.ModTime().Equal(statAfter.ModTime()) {
		t.Error("a rejected validation rewrote the config file")
	}
}

// TestSetWebAccessConnection_NotRunningPersistsOnly: with the sidecar off,
// the pin lands on disk and takes effect at the next start — no sidecar and
// no credential are created by the call itself (the token mints at the first
// START, not at a connection pin).
func TestSetWebAccessConnection_NotRunningPersistsOnly(t *testing.T) {
	pinned := reserveLoopbackPort(t)
	cfg, path := writeWebAccessConfig(t, "web_access:\n  enabled: false\n")
	s := newTestWebAccessService(t, cfg, path)

	if err := s.SetConnection("", pinned); err != nil {
		t.Fatalf("SetConnection: %v", err)
	}
	if s.server.Load() != nil {
		t.Error("the call started the sidecar while web access is disabled")
	}
	if dialLoopback(pinned) {
		t.Error("something listens on the pinned port while the sidecar is off")
	}
	reloaded, err := config.Load(filepath.Join(filepath.Dir(path), "config.yaml"))
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}
	if reloaded.WebAccess.Port != pinned {
		t.Errorf("disk pin = port %d, want %d", reloaded.WebAccess.Port, pinned)
	}
	if reloaded.WebAccess.Token != "" {
		t.Errorf("disabled section grew a token on disk: %q", reloaded.WebAccess.Token)
	}
	if reloaded.WebAccess.Bind != config.DefaultWebAccessBind {
		t.Errorf("disk bind = %q, want the empty-bind default %q",
			reloaded.WebAccess.Bind, config.DefaultWebAccessBind)
	}
	info := s.Snapshot()
	if info.Running || info.ConfiguredPort != pinned || info.TokenPinned || info.Token != "" {
		t.Errorf("info = %+v, want the pin visible with no credential (never started)", info)
	}
	if info.ConfiguredBind != config.DefaultWebAccessBind || !info.BindIsLoopback {
		t.Errorf("info bind = (%q, loopback=%v), want (%q, true)",
			info.ConfiguredBind, info.BindIsLoopback, config.DefaultWebAccessBind)
	}
}

// TestSetWebAccessConnection_RequiresConfigRef mirrors the toggle's guard.
func TestSetWebAccessConnection_RequiresConfigRef(t *testing.T) {
	s := &WebAccessService{}
	if err := s.SetConnection("", 8080); err == nil {
		t.Fatal("expected an error without a config ref, got nil")
	} else if !strings.Contains(err.Error(), "config not initialized") {
		t.Errorf("error = %v, want the config-not-initialized message", err)
	}
}

// TestSetWebAccessConnection_BindPinRoundTrip: the bind is a first-class pin.
// Pinning a non-default loopback bind hot-restarts the sidecar onto it and
// persists the pin; a matching request (bind included) is a no-op; and a
// VALID bind that cannot be listened on rolls the previous pin back on
// disk and live. 192.0.2.1 (TEST-NET-1) is the durable failure bind: never
// assignable on a normal host, so the restart fails at the listener itself
// (EADDRNOTAVAIL) regardless of which binds the sidecar is willing to accept.
func TestSetWebAccessConnection_BindPinRoundTrip(t *testing.T) {
	pinned := reserveLoopbackPort(t)
	// allow_insecure_lan: the 192.0.2.1 rollback leg re-binds onto a
	// NON-loopback plaintext address — refused by the exposure gate without
	// the explicit opt-in, before the listener could fail. With the flag the
	// re-bind passes the gate and fails at the LISTENER (EADDRNOTAVAIL),
	// which is the rollback path this test exists to pin.
	cfg, path := writeWebAccessConfig(t, "web_access:\n  enabled: true\n  allow_insecure_lan: true\n")
	s := newTestWebAccessService(t, cfg, path)
	mountErr := s.MountWebAccess(s.log())
	first := s.server.Load()
	if first == nil {
		t.Fatalf("sidecar did not mount: %v", mountErr)
	}
	minted := mintedToken(t, path)

	// ── Pin a non-default loopback bind with the port: hot restart ──
	if err := s.SetConnection("::1", pinned); err != nil {
		t.Fatalf("SetConnection(::1, %d): %v", pinned, err)
	}
	if s.server.Load() == first {
		t.Error("bind pin did not hot-restart the sidecar")
	}
	if got := sidecarBind(t, s); got != "::1" {
		t.Errorf("sidecar listens on %q, want ::1", got)
	}
	if !dialBind("::1", pinned) {
		t.Errorf("sidecar is not accepting on [::1]:%d", pinned)
	}
	reloaded, err := config.Load(path)
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}
	if reloaded.WebAccess.Bind != "::1" || reloaded.WebAccess.Port != pinned || reloaded.WebAccess.Token != minted {
		t.Errorf("disk pin = (bind %q, port %d, token %q), want (::1, %d, the minted token)",
			reloaded.WebAccess.Bind, reloaded.WebAccess.Port, reloaded.WebAccess.Token, pinned)
	}
	info := s.Snapshot()
	if !info.Running || info.ConfiguredBind != "::1" || !info.BindIsLoopback || info.ConfiguredPort != pinned {
		t.Errorf("info = %+v, want running with configured_bind=::1, bind_is_loopback, configured_port=%d", info, pinned)
	}
	// A specific (non-wildcard) bind is reported as-is: the resolved ::1
	// listener renders as a bracketed IPv6 URL, not a rewritten one.
	if want := "http://[::1]:" + strconv.Itoa(pinned); info.URL != want {
		t.Errorf("info.URL = %q, want %q", info.URL, want)
	}

	// ── A matching request — bind included in the comparison — is a no-op ──
	before, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat config: %v", err)
	}
	if err := s.SetConnection("::1", pinned); err != nil {
		t.Fatalf("matching SetConnection: %v", err)
	}
	if s.server.Load() == first {
		t.Error("a matching request rebuilt the sidecar")
	}
	if s.server.Load() == nil {
		t.Fatal("sidecar vanished after a matching request")
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat config after no-op: %v", err)
	}
	if !before.ModTime().Equal(after.ModTime()) {
		t.Error("a matching request rewrote the config file")
	}

	// ── A valid but unservable bind rolls the previous pin back ──
	if err := s.SetConnection("192.0.2.1", pinned); err == nil {
		t.Fatal("expected an error for the unbindable bind")
	} else if !strings.Contains(err.Error(), "previous settings were restored") {
		t.Errorf("error = %v, want the restored-previous-settings message", err)
	}
	reloaded, err = config.Load(path)
	if err != nil {
		t.Fatalf("reload config after rollback: %v", err)
	}
	if reloaded.WebAccess.Bind != "::1" || reloaded.WebAccess.Port != pinned || reloaded.WebAccess.Token != minted {
		t.Errorf("disk after rollback = (bind %q, port %d, token %q), want (::1, %d, the minted token)",
			reloaded.WebAccess.Bind, reloaded.WebAccess.Port, reloaded.WebAccess.Token, pinned)
	}
	if got := sidecarBind(t, s); got != "::1" {
		t.Errorf("sidecar after rollback listens on %q, want ::1", got)
	}
	if got := s.server.Load().AuthToken(); got != minted {
		t.Errorf("the rolled-back live credential = %q, want the minted token", got)
	}
	if !dialBind("::1", pinned) {
		t.Errorf("restored sidecar is not accepting on [::1]:%d", pinned)
	}
}

// TestSetWebAccessConnection_InvalidBindRefused: a bind that fails the shared
// validity rule is rejected before anything is mutated — neither the disk pin
// nor the running sidecar moves (the same refusal shape as the port rule).
func TestSetWebAccessConnection_InvalidBindRefused(t *testing.T) {
	pinned := reserveLoopbackPort(t)
	cfg, path := writeWebAccessConfig(t, "web_access:\n  enabled: true\n")
	s := newTestWebAccessService(t, cfg, path)
	mountErr := s.MountWebAccess(s.log())
	srv := s.server.Load()
	if srv == nil {
		t.Fatalf("sidecar did not mount: %v", mountErr)
	}
	minted := mintedToken(t, path)
	statBefore, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat config: %v", err)
	}

	// Note: whitespace-only trims to empty = the default bind, which is
	// VALID — the invalid class is hostnames and host:port shapes.
	for _, bind := range []string{"example.com", "localhost.localdomain", "0.0.0.0:8080", "127.0.0.1:80"} {
		if err := s.SetConnection(bind, pinned); err == nil {
			t.Errorf("SetConnection(%q): expected a validation error", bind)
		} else if !strings.Contains(err.Error(), "not a valid IP literal") {
			t.Errorf("SetConnection(%q) error = %v, want the shared bind message", bind, err)
		}
	}
	if s.server.Load() != srv {
		t.Error("a rejected bind rebuilt the sidecar")
	}
	if got := sidecarBind(t, s); got != config.DefaultWebAccessBind {
		t.Errorf("sidecar still listens on %q, want the untouched default %q", got, config.DefaultWebAccessBind)
	}
	reloaded, err := config.Load(path)
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}
	if reloaded.WebAccess.Bind != config.DefaultWebAccessBind || reloaded.WebAccess.Port != 0 || reloaded.WebAccess.Token != minted {
		t.Errorf("disk after rejections = (bind %q, port %d, token %q), want the untouched pin",
			reloaded.WebAccess.Bind, reloaded.WebAccess.Port, reloaded.WebAccess.Token)
	}
	statAfter, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat config after rejections: %v", err)
	}
	if !statBefore.ModTime().Equal(statAfter.ModTime()) {
		t.Error("a rejected bind rewrote the config file")
	}
}

// TestRegenerateWebAccessTokenRotatesAndHotRestarts: the Settings rotation
// mints a fresh credential, persists it immediately, and hot-restarts the
// sidecar onto it — the old token stops authenticating, the new one works.
func TestRegenerateWebAccessTokenRotatesAndHotRestarts(t *testing.T) {
	cfg, path := writeWebAccessConfig(t, "web_access:\n  enabled: true\n")
	s := newTestWebAccessService(t, cfg, path)
	mountErr := s.MountWebAccess(s.log())
	first := s.server.Load()
	if first == nil {
		t.Fatalf("sidecar did not mount: %v", mountErr)
	}
	before := first.AuthToken()
	if before == "" {
		t.Fatal("no live credential before the rotation")
	}

	if err := s.RegenerateToken(); err != nil {
		t.Fatalf("RegenerateToken: %v", err)
	}
	second := s.server.Load()
	if second == nil {
		t.Fatal("sidecar vanished after the rotation")
	}
	if second == first {
		t.Error("rotation did not hot-restart the sidecar")
	}
	after := second.AuthToken()
	if after == before {
		t.Error("rotation minted the same token")
	}
	if len(after) != 64 {
		t.Errorf("rotated token = %q (len %d), want a 64-hex mint", after, len(after))
	}
	if tok := mintedToken(t, path); tok != after {
		t.Errorf("disk token %q != the live rotated credential %q", tok, after)
	}
	info := s.Snapshot()
	if !info.Running || !info.TokenPinned || info.Token != after {
		t.Errorf("info = %+v, want running, token_pinned, token=rotated", info)
	}

	// The rotated credential authenticates; the retired one is dead.
	ts := newWebAccessHTTPTest(t, second)
	client := loginWebAccessClient(t, ts.URL, after)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, ts.URL+"/api/transport", http.NoBody)
	if err != nil {
		t.Fatalf("build GET /api/transport: %v", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("authenticated GET /api/transport: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		t.Errorf("authenticated transport probe status = %d, want 200", resp.StatusCode)
	}
	oldBody, err := json.Marshal(map[string]string{"token": before})
	if err != nil {
		t.Fatalf("marshal old-token login: %v", err)
	}
	oldReq, err := http.NewRequestWithContext(t.Context(), http.MethodPost,
		ts.URL+"/api/login", strings.NewReader(string(oldBody)))
	if err != nil {
		t.Fatalf("build old-token login: %v", err)
	}
	oldReq.Header.Set("Content-Type", "application/json")
	oldResp, err := http.DefaultClient.Do(oldReq)
	if err != nil {
		t.Fatalf("old-token login: %v", err)
	}
	defer func() { _ = oldResp.Body.Close() }()
	if oldResp.StatusCode != http.StatusUnauthorized {
		t.Errorf("retired token login status = %d, want 401", oldResp.StatusCode)
	}
}

// TestRegenerateWebAccessTokenWhileStoppedPersistsOnly: with the sidecar off,
// the rotation is the persisted write alone — the credential is ready for the
// next start, and nothing begins listening.
func TestRegenerateWebAccessTokenWhileStoppedPersistsOnly(t *testing.T) {
	cfg, path := writeWebAccessConfig(t, "web_access:\n  enabled: false\n")
	s := newTestWebAccessService(t, cfg, path)

	if err := s.RegenerateToken(); err != nil {
		t.Fatalf("RegenerateToken: %v", err)
	}
	if s.server.Load() != nil {
		t.Error("the rotation started the sidecar while web access is disabled")
	}
	tok := mintedToken(t, path)
	info := s.Snapshot()
	if info.Running || !info.TokenPinned || info.Token != tok {
		t.Errorf("info = %+v, want the persisted credential visible while stopped", info)
	}
}

// TestRegenerateWebAccessTokenRequiresConfigRef mirrors the toggle's guard.
func TestRegenerateWebAccessTokenRequiresConfigRef(t *testing.T) {
	s := &WebAccessService{}
	if err := s.RegenerateToken(); err == nil {
		t.Fatal("expected an error without a config ref, got nil")
	} else if !strings.Contains(err.Error(), "config not initialized") {
		t.Errorf("error = %v, want the config-not-initialized message", err)
	}
}

// TestRegenerateWebAccessTokenSaveFailureKeepsPreviousToken: a rotation whose
// immediate persist cannot land is a full no-op — the live credential and the
// running sidecar stay exactly as they were (the same honesty rules as the
// connection pin's save failure).
func TestRegenerateWebAccessTokenSaveFailureKeepsPreviousToken(t *testing.T) {
	cfg, path := writeWebAccessConfig(t, "web_access:\n  enabled: true\n  token: keep-me\n")
	s := newTestWebAccessService(t, cfg, path)
	mountErr := s.MountWebAccess(s.log())
	first := s.server.Load()
	if first == nil {
		t.Fatalf("sidecar did not mount: %v", mountErr)
	}
	if got := first.AuthToken(); got != "keep-me" {
		t.Fatalf("live token = %q, want the configured keep-me", got)
	}

	// The config FILE becomes a directory: the rotation's persist cannot
	// land (the atomic write fails), and nothing else may move.
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove config file: %v", err)
	}
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatalf("replace config file with a directory: %v", err)
	}

	if err := s.RegenerateToken(); err == nil {
		t.Fatal("expected an error when the rotated token cannot persist")
	} else if !strings.Contains(err.Error(), "failed to persist web access config") {
		t.Errorf("error = %v, want the persist-failure message", err)
	}
	if s.server.Load() != first {
		t.Error("a failed persist touched the running sidecar")
	}
	if got := s.server.Load().AuthToken(); got != "keep-me" {
		t.Errorf("live token after the failed rotation = %q, want keep-me", got)
	}
}

// TestSetWebAccessTokenPersistsAndHotRestarts: the Settings token field
// persists an operator-typed credential and hot-restarts the sidecar onto
// it — the previous token stops authenticating, the trimmed typed value is
// both live and on disk.
func TestSetWebAccessTokenPersistsAndHotRestarts(t *testing.T) {
	cfg, path := writeWebAccessConfig(t, "web_access:\n  enabled: true\n")
	s := newTestWebAccessService(t, cfg, path)
	mountErr := s.MountWebAccess(s.log())
	first := s.server.Load()
	if first == nil {
		t.Fatalf("sidecar did not mount: %v", mountErr)
	}
	before := first.AuthToken()

	// Leading/trailing whitespace is a paste artifact: the RPC trims.
	if err := s.SetToken("  custom-token-123  "); err != nil {
		t.Fatalf("SetToken: %v", err)
	}
	second := s.server.Load()
	if second == nil || second == first {
		t.Fatal("setting a custom token did not hot-restart the sidecar")
	}
	if got := second.AuthToken(); got != "custom-token-123" {
		t.Errorf("live token = %q, want the trimmed custom value", got)
	}
	if tok := configTokenOnDisk(t, path); tok != "custom-token-123" {
		t.Errorf("disk token = %q, want the persisted custom value", tok)
	}
	info := s.Snapshot()
	if !info.Running || !info.TokenPinned || info.Token != "custom-token-123" {
		t.Errorf("info = %+v, want running, token_pinned, token=custom", info)
	}

	// The retired credential no longer logs in.
	ts := newWebAccessHTTPTest(t, second)
	oldBody, err := json.Marshal(map[string]string{"token": before})
	if err != nil {
		t.Fatalf("marshal old-token login: %v", err)
	}
	oldReq, err := http.NewRequestWithContext(t.Context(), http.MethodPost,
		ts.URL+"/api/login", strings.NewReader(string(oldBody)))
	if err != nil {
		t.Fatalf("build old-token login: %v", err)
	}
	oldReq.Header.Set("Content-Type", "application/json")
	oldResp, err := http.DefaultClient.Do(oldReq)
	if err != nil {
		t.Fatalf("old-token login: %v", err)
	}
	defer func() { _ = oldResp.Body.Close() }()
	if oldResp.StatusCode != http.StatusUnauthorized {
		t.Errorf("retired token login status = %d, want 401", oldResp.StatusCode)
	}
}

// TestSetWebAccessTokenNoOpOnSameToken: retyping the stored credential is a
// no-op — no persist, no restart.
func TestSetWebAccessTokenNoOpOnSameToken(t *testing.T) {
	cfg, path := writeWebAccessConfig(t, "web_access:\n  enabled: true\n  token: same-token-1\n")
	s := newTestWebAccessService(t, cfg, path)
	mountErr := s.MountWebAccess(s.log())
	first := s.server.Load()
	if first == nil {
		t.Fatalf("sidecar did not mount: %v", mountErr)
	}

	if err := s.SetToken("same-token-1"); err != nil {
		t.Fatalf("SetToken(same): %v", err)
	}
	if s.server.Load() != first {
		t.Error("an unchanged token restarted the sidecar")
	}
}

// TestSetWebAccessTokenRejectsShortAndEmpty: values below the floor are
// refused before anything moves, and the empty value — the GENERATED
// sentinel — never overwrites an existing credential.
func TestSetWebAccessTokenRejectsShortAndEmpty(t *testing.T) {
	cfg, path := writeWebAccessConfig(t, "web_access:\n  enabled: true\n  token: keep-me-alive\n")
	s := newTestWebAccessService(t, cfg, path)
	mountErr := s.MountWebAccess(s.log())
	first := s.server.Load()
	if first == nil {
		t.Fatalf("sidecar did not mount: %v", mountErr)
	}

	for _, tok := range []string{"", "short", "   ", strings.Repeat("x", 257)} {
		err := s.SetToken(tok)
		if err == nil {
			t.Fatalf("SetToken(len=%d) = nil, want error", len(tok))
		}
		if !strings.Contains(err.Error(), "web_access: token") {
			t.Errorf("SetToken(len=%d) error = %v, want the section-naming token message", len(tok), err)
		}
	}
	if s.server.Load() != first {
		t.Error("a rejected token touched the running sidecar")
	}
	if got := s.server.Load().AuthToken(); got != "keep-me-alive" {
		t.Errorf("live token after rejections = %q, want keep-me-alive", got)
	}
	if tok := configTokenOnDisk(t, path); tok != "keep-me-alive" {
		t.Errorf("disk token after rejections = %q, want keep-me-alive", tok)
	}
}

// TestSetWebAccessTokenWhileStoppedPersistsOnly: with the sidecar off the
// typed credential is the persisted write alone, ready for the next start.
func TestSetWebAccessTokenWhileStoppedPersistsOnly(t *testing.T) {
	cfg, path := writeWebAccessConfig(t, "web_access:\n  enabled: false\n")
	s := newTestWebAccessService(t, cfg, path)

	if err := s.SetToken("stopped-token-1"); err != nil {
		t.Fatalf("SetToken: %v", err)
	}
	if s.server.Load() != nil {
		t.Error("setting a token started the sidecar while web access is disabled")
	}
	info := s.Snapshot()
	if info.Running || !info.TokenPinned || info.Token != "stopped-token-1" {
		t.Errorf("info = %+v, want the persisted credential visible while stopped", info)
	}
}

// TestSetWebAccessTokenRequiresConfigRef mirrors the toggle's guard.
func TestSetWebAccessTokenRequiresConfigRef(t *testing.T) {
	s := &WebAccessService{}
	if err := s.SetToken("enough-length-1"); err == nil {
		t.Fatal("expected an error without a config ref, got nil")
	} else if !strings.Contains(err.Error(), "config not initialized") {
		t.Errorf("error = %v, want the config-not-initialized message", err)
	}
}

// TestSetWebAccessTokenSaveFailureKeepsPreviousToken: a typed credential
// whose immediate persist cannot land is a full no-op — the live credential
// and the running sidecar stay exactly as they were.
func TestSetWebAccessTokenSaveFailureKeepsPreviousToken(t *testing.T) {
	cfg, path := writeWebAccessConfig(t, "web_access:\n  enabled: true\n  token: keep-me\n")
	s := newTestWebAccessService(t, cfg, path)
	mountErr := s.MountWebAccess(s.log())
	first := s.server.Load()
	if first == nil {
		t.Fatalf("sidecar did not mount: %v", mountErr)
	}

	if err := os.Remove(path); err != nil {
		t.Fatalf("remove config file: %v", err)
	}
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatalf("replace config file with a directory: %v", err)
	}

	if err := s.SetToken("never-lands-1"); err == nil {
		t.Fatal("expected an error when the typed token cannot persist")
	} else if !strings.Contains(err.Error(), "failed to persist web access config") {
		t.Errorf("error = %v, want the persist-failure message", err)
	}
	if s.server.Load() != first {
		t.Error("a failed persist touched the running sidecar")
	}
	if got := s.server.Load().AuthToken(); got != "keep-me" {
		t.Errorf("live token after the failed save = %q, want keep-me", got)
	}
}
