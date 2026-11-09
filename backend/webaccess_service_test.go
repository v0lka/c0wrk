// SPDX-License-Identifier: Apache-2.0
//
// webaccess_service_test.go — the transport-neutral WebAccessService
// lifecycle (ADR-101): the hot-restart toggle, the bounded shutdown stop,
// and the experimental master gate. The tests moved here from the desktop
// package with their code (P-11): the service owns the sidecar's whole
// lifecycle, so its tests drive it directly through Configure + the
// exported control operations; the desktop adapter (initWebAccess /
// stopWebAccess and its log records) keeps its own tests in
// desktop/webaccess_test.go.
//
// Every listener-binding test uses a RESERVED loopback port (bound and
// released up front) or port 0 (auto), so the suite never races itself or a
// foreign listener; "nothing listens" is asserted by an actual dial, not by
// absence of a handle.

package backend

import (
	"bytes"
	"context"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/internal/webaccess"
)

// writeWebAccessConfig writes a loadable config.yaml (the smallest document
// the loader accepts) with an optional web_access block, loads it through the
// real pipeline, and returns the live config plus its path. The experimental
// master gate (web access is an experimental feature, ADR-101) is OPEN —
// these tests exercise the sidecar's own lifecycle, not the gate.
func writeWebAccessConfig(t *testing.T, webAccess string) (cfg *config.Config, path string) {
	t.Helper()
	return writeWebAccessConfigDoc(t, "experimental:\n  enabled: true\n", webAccess)
}

// writeWebAccessConfigGate is writeWebAccessConfig with an explicit
// experimental-section body (empty = the section is ABSENT — the loader's
// default false, the closed gate), for tests of the gate itself.
func writeWebAccessConfigGate(t *testing.T, experimental, webAccess string) (cfg *config.Config, path string) {
	t.Helper()
	return writeWebAccessConfigDoc(t, experimental, webAccess)
}

func writeWebAccessConfigDoc(t *testing.T, experimental, webAccess string) (cfg *config.Config, path string) {
	t.Helper()
	doc := `
llm:
  default_model: claude-3-haiku
  anthropic:
    api_key: "test-key"
    models:
      - claude-3-haiku
`
	if experimental != "" {
		doc += "\n" + experimental + "\n"
	}
	if webAccess != "" {
		doc += "\n" + webAccess + "\n"
	}
	path = filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(doc), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	loaded, err := config.Load(path)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	cfg = loaded
	return cfg, path
}

// reserveLoopbackPort binds 127.0.0.1:0, closes the listener immediately and
// returns its port. The port is reserved only against concurrent tests in
// this process — the assertion dials decide listening or not.
func reserveLoopbackPort(t *testing.T) int {
	t.Helper()
	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve loopback port: %v", err)
	}
	addr, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("reservation address %v is not a TCP address", ln.Addr())
	}
	port := addr.Port
	if err := ln.Close(); err != nil {
		t.Fatalf("close reservation listener: %v", err)
	}
	return port
}

// dialLoopback reports whether anything accepts TCP on 127.0.0.1:port within
// a bounded wait (false = connection refused / unreachable = nothing there).
func dialLoopback(port int) bool {
	dialer := &net.Dialer{Timeout: 500 * time.Millisecond}
	conn, err := dialer.DialContext(context.Background(), "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// webAccessServiceSeams exposes the hook seams the tests observe: the
// swappable instance logger (resolved through the hook on every service
// call) and the side event tap the service installs and detaches.
type webAccessServiceSeams struct {
	logger atomic.Pointer[slog.Logger]
	tap    atomic.Pointer[func(name string, args []interface{})]
}

// newWebAccessServiceWithSeams builds a service whose hooks mirror the
// desktop wiring (config ref, logger, FrontendAPI seed) with test seams for
// the rest: a quiet Error-level buffer logger (no test-output noise from the
// sidecar's Info records), an empty ExtraDispatch surface, no inbound
// dispatcher, no CSP middleware, and a tap spy. The embedded FrontendAPI is
// the zero-value seed — exactly what production mounts before Init, and all
// the reflection dispatcher needs to resolve the surface. Cleanup stops the
// service.
func newWebAccessServiceWithSeams(t *testing.T, cfg *config.Config, path string) (*WebAccessService, *webAccessServiceSeams) {
	t.Helper()
	seams := &webAccessServiceSeams{}
	seams.logger.Store(slog.New(slog.NewTextHandler(&strings.Builder{}, &slog.HandlerOptions{Level: slog.LevelError})))
	s := &WebAccessService{}
	s.Configure(WebAccessHooks{
		Config:        func() (*config.Config, string) { return cfg, path },
		Logger:        func() *slog.Logger { return seams.logger.Load() },
		API:           func() *FrontendAPI { return &FrontendAPI{} },
		ExtraDispatch: func() any { return nil },
		WSInbound:     func(*slog.Logger) func(*webaccess.WSConn, []byte) { return nil },
		CSP:           func() func(http.Handler) http.Handler { return nil },
		SetEventTap: func(tap func(name string, args []interface{})) {
			// Mirror the desktop setEventTap adapter: nil clears the tap
			// pointer rather than storing a pointer to a nil func.
			if tap == nil {
				seams.tap.Store(nil)
				return
			}
			seams.tap.Store(&tap)
		},
	})
	t.Cleanup(func() {
		_ = s.StopForShutdown(context.Background())
	})
	return s, seams
}

// newTestWebAccessService is the plain-service constructor most tests use.
func newTestWebAccessService(t *testing.T, cfg *config.Config, path string) *WebAccessService {
	t.Helper()
	s, _ := newWebAccessServiceWithSeams(t, cfg, path)
	return s
}

// diagnosticExpectation describes one deliberate failure-path receipt —
// adapted from the desktop diagnostics capture (the service resolves its
// logger through the hook on every call, so injection swaps the seam).
type diagnosticExpectation struct {
	level   slog.Level
	message string
	attrs   map[string]string
}

type diagnosticCapture struct {
	slog.Handler
	mu      sync.Mutex
	records []diagnosticExpectation
}

func (h *diagnosticCapture) Enabled(ctx context.Context, level slog.Level) bool {
	return level >= slog.LevelWarn || h.Handler.Enabled(ctx, level)
}

func (h *diagnosticCapture) Handle(ctx context.Context, r slog.Record) error {
	if r.Level < slog.LevelWarn {
		return h.Handler.Handle(ctx, r)
	}
	attrs := make(map[string]string)
	r.Attrs(func(a slog.Attr) bool { attrs[a.Key] = a.Value.Resolve().String(); return true })
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, diagnosticExpectation{r.Level, r.Message, attrs})
	return nil
}

// expectedWebAccessDiagnostics is injected only into the test exercising the
// receipt. Cleanup rejects additional or missing diagnostics rather than
// hiding them.
func expectedWebAccessDiagnostics(t *testing.T, want ...diagnosticExpectation) *slog.Logger {
	t.Helper()
	h := &diagnosticCapture{Handler: slog.Default().Handler()}
	t.Cleanup(func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if len(h.records) != len(want) {
			t.Errorf("diagnostics(%s) count = %d, want %d; got %+v", t.Name(), len(h.records), len(want), h.records)
		}
		for i := 0; i < len(h.records) && i < len(want); i++ {
			if !reflect.DeepEqual(h.records[i], want[i]) {
				t.Errorf("diagnostics(%s)[%d] = %+v, want %+v", t.Name(), i, h.records[i], want[i])
			}
		}
	})
	return slog.New(h)
}

// injectWebAccessExpectedDiagnostics swaps the service's instance logger for
// the capturing one (the desktop injectExpectedDiagnostics equivalent: the
// service resolves hooks.Logger per call, so a pre-call swap observes
// exactly the records the service itself emits). Registered after the
// constructor's stop cleanup, so the logger is restored before the cleanup
// stop runs.
func injectWebAccessExpectedDiagnostics(t *testing.T, seams *webAccessServiceSeams, want ...diagnosticExpectation) {
	t.Helper()
	old := seams.logger.Load()
	seams.logger.Store(expectedWebAccessDiagnostics(t, want...))
	t.Cleanup(func() { seams.logger.Store(old) })
}

func TestSetWebAccessEnabled_HotRestartPersists(t *testing.T) {
	port := reserveLoopbackPort(t)
	cfg, path := writeWebAccessConfig(t, `
web_access:
  enabled: true
  port: `+strconv.Itoa(port)+`
`)
	s, seams := newWebAccessServiceWithSeams(t, cfg, path)
	if err := s.MountWebAccess(s.log()); err != nil {
		t.Fatalf("mount: %v", err)
	}
	first := s.server.Load()
	if first == nil {
		t.Fatal("sidecar did not mount")
	}

	// ── Disable: the listener dies, the file flips, the tap detaches ──
	if err := s.SetEnabled(false); err != nil {
		t.Fatalf("SetEnabled(false): %v", err)
	}
	if srv := s.server.Load(); srv != nil {
		t.Errorf("handle still set after disable: %v", srv)
	}
	if seams.tap.Load() != nil {
		t.Error("event tap still set after disable")
	}
	if dialLoopback(port) {
		t.Errorf("port %d still accepting after disable", port)
	}
	if reloaded, err := config.Load(path); err != nil {
		t.Fatalf("reload config after disable: %v", err)
	} else if reloaded.WebAccess.Enabled {
		t.Error("web_access.enabled is still true on disk after disable")
	}
	if snap := s.Snapshot(); snap.Running || snap.Enabled {
		t.Errorf("snapshot after disable = %+v, want enabled=false running=false", snap)
	}

	// ── Enable again: hot restart on the same configured port ──
	if err := s.SetEnabled(true); err != nil {
		t.Fatalf("SetEnabled(true): %v", err)
	}
	second := s.server.Load()
	if second == nil {
		t.Fatal("sidecar did not restart on enable")
	}
	if second == first {
		t.Error("restart reused the stopped server instead of building a new one")
	}
	if seams.tap.Load() == nil {
		t.Error("event tap not re-registered on enable")
	}
	if !dialLoopback(port) {
		t.Errorf("restarted sidecar is not listening on the configured port %d", port)
	}
	if reloaded, err := config.Load(path); err != nil {
		t.Fatalf("reload config after enable: %v", err)
	} else if !reloaded.WebAccess.Enabled {
		t.Error("web_access.enabled is false on disk after enable")
	}
	snap := s.Snapshot()
	if !snap.Running || !snap.Enabled || snap.Port != port {
		t.Errorf("snapshot after re-enable = %+v, want running on port %d", snap, port)
	}

	// ── Matching request: a no-op that neither restarts nor rewrites ──
	before, _ := os.Stat(path)
	if err := s.SetEnabled(true); err != nil {
		t.Fatalf("matching SetEnabled(true): %v", err)
	}
	if s.server.Load() != second {
		t.Error("matching enable rebuilt the sidecar")
	}
	after, _ := os.Stat(path)
	if !before.ModTime().Equal(after.ModTime()) {
		t.Error("matching enable rewrote the config file")
	}
}

func TestSetWebAccessEnabled_RequiresConfigRef(t *testing.T) {
	s := &WebAccessService{} // zero value: hooks unset — the pre-Init desktop face
	if err := s.SetEnabled(true); err == nil {
		t.Fatal("expected an error without a config ref, got nil")
	} else if !strings.Contains(err.Error(), "config not initialized") {
		t.Errorf("error = %v, want the config-not-initialized message", err)
	}
}

// TestMountWebAccess_PartialHooks documents the daemon-host configuration
// (the WebAccessHooks doc: "different — or nil — hooks"). The GUI-only
// hooks (ExtraDispatch/WSInbound/CSP/SetEventTap) are optional seams: a
// host that leaves them nil mounts, runs and stops — the missing surface
// degrades, the process does not (these four dereferences panicked once
// during the P-11 extraction). Dispatch, by contrast, is the sidecar's
// required core: without the API hook the mount fails closed with an
// error, never a panic.
func TestMountWebAccess_PartialHooks(t *testing.T) {
	port := reserveLoopbackPort(t)
	cfg, path := writeWebAccessConfig(t, `
web_access:
  enabled: true
  port: `+strconv.Itoa(port)+`
`)

	// (1) Config + Logger only: fail closed at the required Dispatch.
	bare := &WebAccessService{}
	quiet := slog.New(slog.NewTextHandler(&strings.Builder{}, &slog.HandlerOptions{Level: slog.LevelError}))
	bare.Configure(WebAccessHooks{
		Config: func() (*config.Config, string) { return cfg, path },
		Logger: func() *slog.Logger { return quiet },
	})
	if err := bare.MountWebAccess(bare.log()); err == nil {
		t.Fatal("expected a fail-closed mount error without the API hook, got nil")
	} else if !strings.Contains(err.Error(), "Dispatch") {
		t.Errorf("error = %v, want the missing-dispatch refusal", err)
	}
	if srv := bare.server.Load(); srv != nil {
		t.Error("sidecar handle published despite the failed mount")
	}

	// (2) Config + Logger + API, GUI-only hooks nil: full lifecycle works.
	seams := &webAccessServiceSeams{}
	seams.logger.Store(quiet)
	s := &WebAccessService{}
	s.Configure(WebAccessHooks{
		Config: func() (*config.Config, string) { return cfg, path },
		Logger: func() *slog.Logger { return seams.logger.Load() },
		API:    func() *FrontendAPI { return &FrontendAPI{} },
	})
	t.Cleanup(func() {
		if err := s.StopForShutdown(context.Background()); err != nil {
			t.Errorf("stop with partial hooks: %v", err)
		}
	})

	if err := s.MountWebAccess(s.log()); err != nil {
		t.Fatalf("mount with partial hooks: %v", err)
	}
	if s.server.Load() == nil {
		t.Fatal("sidecar did not mount with partial hooks")
	}
	if !dialLoopback(port) {
		t.Errorf("sidecar is not listening on %d with partial hooks", port)
	}
	if snap := s.Snapshot(); !snap.Running {
		t.Errorf("snapshot with partial hooks = %+v, want running", snap)
	}
}

func TestStopWebAccess_BoundedEvenWithExpiredDeadline(t *testing.T) {
	port := reserveLoopbackPort(t)
	cfg, path := writeWebAccessConfig(t, `
web_access:
  enabled: true
  port: `+strconv.Itoa(port)+`
`)
	s, seams := newWebAccessServiceWithSeams(t, cfg, path)
	if err := s.MountWebAccess(s.log()); err != nil {
		t.Fatalf("mount: %v", err)
	}
	if s.server.Load() == nil {
		t.Fatal("sidecar did not mount")
	}
	if !dialLoopback(port) {
		t.Fatalf("sidecar is not listening on %d before the stop", port)
	}

	// (1) The locked teardown must respect an EXPIRED caller deadline:
	// return promptly (never hang on the graceful phase) while still freeing
	// the port. Whether the drain beats the deadline or loses to it is
	// http.Server's call — with zero connections both outcomes are legal —
	// so only the BOUND and the teardown are asserted here. The busy case
	// (a live WebSocket forcing the deadline to win and the force close to
	// fire) is pinned deterministically by TestStopWithExpiredDeadlineForceCloses
	// in internal/webaccess.
	//
	// A past DEADLINE (not a bare cancel): Stop only honors a caller budget
	// when the context actually carries one. The wrapped budget inside the
	// Shutdown wrapper derives the child deadline from its parent, so this is
	// exactly the shape a watchdog expiry would deliver.
	expired, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()

	s.mu.Lock()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = s.stopWebAccessLocked(expired)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("locked stop did not return within 5s of an expired deadline")
	}
	// (2) The StopForShutdown wrapper on a healthy context: the quiet path.
	// Zero expected diagnostics — a stop that took must not log anything.
	s.mu.Unlock()

	if srv := s.server.Load(); srv != nil {
		t.Errorf("sidecar handle still set after the stop: %v", srv)
	}
	if seams.tap.Load() != nil {
		t.Error("event tap still set after the stop")
	}
	if dialLoopback(port) {
		t.Errorf("port %d is still accepting after the bounded stop", port)
	}

	// Restart on the same configured port, then drive the full wrapper.
	s.mu.Lock()
	if _, err := s.startWebAccessLocked(s.log()); err != nil {
		s.mu.Unlock()
		t.Fatalf("restart after the bounded stop: %v", err)
	}
	s.mu.Unlock()
	if !dialLoopback(port) {
		t.Fatalf("restarted sidecar is not listening on %d", port)
	}
	injectWebAccessExpectedDiagnostics(t, seams)
	// StopForShutdown takes mu itself — call it unlocked.
	if err := s.StopForShutdown(context.Background()); err != nil {
		t.Errorf("StopForShutdown on a healthy context: %v", err)
	}

	// Idempotent: a second stop on the cleared handle is a no-op, not a panic.
	if err := s.StopForShutdown(context.Background()); err != nil {
		t.Errorf("idempotent StopForShutdown: %v", err)
	}
	if srv := s.server.Load(); srv != nil {
		t.Errorf("sidecar handle set after the wrapper stop: %v", srv)
	}
}

// TestApplyExperimentalWebAccess_LiveFollowStartAndStop drives the service's
// live-follow hook through a full open/close cycle on a config whose startup
// mount was skipped (gate closed, section on): opening the gate mounts
// mid-session, closing it stops the sidecar, and both no-op combinations
// stay inert.
func TestApplyExperimentalWebAccess_LiveFollowStartAndStop(t *testing.T) {
	port := reserveLoopbackPort(t)
	cfg, path := writeWebAccessConfigGate(t, "experimental:\n  enabled: false\n", "web_access:\n  enabled: true\n  port: "+strconv.Itoa(port)+"\n")
	s, seams := newWebAccessServiceWithSeams(t, cfg, path)

	// Startup: gate closed → the mount predicate refuses — the desktop
	// initWebAccess adapter consults the same predicate and skips the mount,
	// so a fresh service starts unmounted.
	if WebAccessMountAllowed(cfg, cfg.Experimental.Enabled) {
		t.Fatal("precondition: the closed-gate mount predicate must refuse this config")
	}
	if s.server.Load() != nil {
		t.Fatal("precondition: the closed-gate startup mount should have skipped")
	}
	if dialLoopback(port) {
		t.Fatal("precondition: nothing should listen before the hook opens the gate")
	}

	// Open: hook mounts mid-session (a pinned port must be taken exactly
	// once — a double bind would fail the start and log an Error).
	s.ApplyExperimental(true)
	if s.server.Load() == nil {
		t.Fatal("gate opened: sidecar handle is nil — the hook did not mount")
	}
	if seams.tap.Load() == nil {
		t.Error("gate opened: event tap is nil — browser clients would receive no events")
	}
	if !dialLoopback(port) {
		t.Errorf("nothing accepts on the pinned port %d after the hook opened the gate", port)
	}

	// Idempotent open: a repeated open over the live sidecar is a no-op —
	// the same handle, still one listener on the same port.
	again := s.server.Load()
	s.ApplyExperimental(true)
	if s.server.Load() != again {
		t.Error("repeated open swapped the live sidecar handle — startWebAccessLocked is not idempotent on a live server")
	}
	if !dialLoopback(port) {
		t.Errorf("listener lost after an idempotent repeated open (port %d)", port)
	}

	// Close: hook stops the running sidecar.
	s.ApplyExperimental(false)
	if srv := s.server.Load(); srv != nil {
		t.Fatalf("gate closed: sidecar handle = %v, want nil", srv)
	}
	if seams.tap.Load() != nil {
		t.Error("gate closed: event tap is still set — events would fan out to a dead server")
	}
	if dialLoopback(port) {
		t.Errorf("something still listens on port %d after the hook closed the gate", port)
	}

	// Inert close: closing an already-closed gate is a no-op (no stop of a
	// nil sidecar, no log noise beyond the Debug level of the test logger).
	s.ApplyExperimental(false)
	if s.server.Load() != nil {
		t.Error("gate closed twice: sidecar handle reappeared")
	}
}

// TestApplyExperimentalWebAccess_SectionOffMountsNothing pins the section's
// own switch as an independent gate: an OPEN experimental gate whose
// web_access.enabled is false must not start the sidecar.
func TestApplyExperimentalWebAccess_SectionOffMountsNothing(t *testing.T) {
	port := reserveLoopbackPort(t)
	cfg, path := writeWebAccessConfigGate(t, "experimental:\n  enabled: false\n", "web_access:\n  enabled: false\n  port: "+strconv.Itoa(port)+"\n")
	s := newTestWebAccessService(t, cfg, path)

	s.ApplyExperimental(true)

	if s.server.Load() != nil {
		t.Fatal("section off: sidecar handle is set — the hook started the sidecar against its own disabled switch")
	}
	if dialLoopback(port) {
		t.Errorf("something listens on reserved port %d with web_access.enabled=false", port)
	}
}

// TestSetWebAccessEnabled_ExperimentalGateClosedPersistsButMountsNothing pins
// the third mount decision (ADR-101 decision 13): the section toggle persists
// operator intent with the master gate closed, but starts no sidecar — a
// closed gate cannot be bypassed by a programmatic caller, a config-restore
// path or a webview-console invocation. The skip is quiet (Debug), the
// disable toggle behind the closed gate is a clean no-op, and opening the
// gate afterwards mounts the persisted-on section through the live-follow
// hook.
func TestSetWebAccessEnabled_ExperimentalGateClosedPersistsButMountsNothing(t *testing.T) {
	port := reserveLoopbackPort(t)
	cfg, path := writeWebAccessConfigGate(t, "experimental:\n  enabled: false\n", "web_access:\n  enabled: false\n  port: "+strconv.Itoa(port)+"\n")

	// A Debug-level buffer (unlike the constructor's Error-only logger) so
	// the quiet-skip record itself is observable and any ERROR/WARN
	// diagnostic fails the test. Swapped before the first service call, so
	// the service never resolves the quiet logger.
	var buf bytes.Buffer
	s, seams := newWebAccessServiceWithSeams(t, cfg, path)
	seams.logger.Store(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))

	// Toggle on behind the closed gate: persisted, unmounted, quiet.
	if err := s.SetEnabled(true); err != nil {
		t.Fatalf("SetEnabled(true) behind the closed gate: %v", err)
	}
	if s.server.Load() != nil {
		t.Fatal("closed experimental gate: the toggle mounted the sidecar — gate bypass")
	}
	if seams.tap.Load() != nil {
		t.Error("closed experimental gate: the toggle set the event tap")
	}
	if dialLoopback(port) {
		t.Errorf("something listens on reserved port %d behind the closed gate", port)
	}
	persisted, err := config.Load(path)
	if err != nil {
		t.Fatalf("config.Load after the gated toggle: %v", err)
	}
	if !persisted.WebAccess.Enabled {
		t.Error("web_access.enabled was not persisted by the gated toggle — operator intent lost")
	}

	// The disable toggle behind the closed gate is a clean no-op.
	if err := s.SetEnabled(false); err != nil {
		t.Fatalf("SetEnabled(false) behind the closed gate: %v", err)
	}
	if s.server.Load() != nil {
		t.Fatal("disable toggle behind the closed gate left a sidecar handle")
	}
	persisted, err = config.Load(path)
	if err != nil {
		t.Fatalf("config.Load after the gated disable: %v", err)
	}
	if persisted.WebAccess.Enabled {
		t.Error("web_access.enabled still on after the gated disable")
	}

	// Re-enable, then open the gate: the live-follow hook mounts the
	// persisted-on section mid-session.
	if err := s.SetEnabled(true); err != nil {
		t.Fatalf("re-enable behind the closed gate: %v", err)
	}
	s.ApplyExperimental(true)
	if s.server.Load() == nil {
		t.Fatal("gate opened after the gated toggle: sidecar handle is nil — the hook did not mount")
	}
	if !dialLoopback(port) {
		t.Errorf("nothing accepts on the pinned port %d after the gate opened", port)
	}

	// The disable toggle still stops the live sidecar (gate now open).
	if err := s.SetEnabled(false); err != nil {
		t.Fatalf("SetEnabled(false) with the gate open: %v", err)
	}
	if s.server.Load() != nil {
		t.Fatal("disable toggle left the sidecar running")
	}
	if dialLoopback(port) {
		t.Errorf("something still listens on port %d after the disable toggle", port)
	}

	logs := buf.String()
	if strings.Contains(logs, "level=ERROR") || strings.Contains(logs, "level=WARN") {
		t.Errorf("the gated toggle must stay quiet, got:\n%s", logs)
	}
	if !strings.Contains(logs, "web access enabled while the experimental gate is closed") {
		t.Errorf("the gated toggle must leave its Debug skip record, got:\n%s", logs)
	}
}
