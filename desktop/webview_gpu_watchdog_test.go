package desktop

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/v0lka/c0wrk/backend/session"
)

// TestWebviewGpuWatchdogQuitBypassesCloseGuard pins the relaunch quit's
// interaction with the exit guard: the quit the watchdog issues after a
// successful relaunch must NOT be intercepted by ShouldPreventClose — the
// confirmation modal lives in the grey webview being replaced, so an
// intercepted fallback quit would strand an unanswerable dialog and cancel
// the auto-recovery. startWebviewGpuWatchdog wires the quit via
// wireGpuWatchdogQuit; the wiring is pinned through that split directly,
// because the watchdog's started flag (which kept start() from spawning the
// bounded scanner goroutine in this test) is backend-internal since the
// P-13 move — Start() itself is covered by the backend watchdog tests.
func TestWebviewGpuWatchdogQuitBypassesCloseGuard(t *testing.T) {
	f := newExitGuardFixture([]session.ActiveSessionInfo{{ID: "sess-1"}})
	// Intercept the instance logger: the paths under test announce their
	// decisions at Info, and that output must not leak into the test run
	// (asserted below instead).
	var logs bytes.Buffer
	f.app.logger = slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))

	// Without the bypass the guard intercepts (and would render the modal
	// into the webview the watchdog is about to replace).
	if !f.app.ShouldPreventClose(context.Background()) {
		t.Fatal("expected the guard to intercept a plain quit while a session is active")
	}

	w := &webviewGpuWatchdog{}
	f.app.webviewGpuWatchdog = w
	f.app.wireGpuWatchdogQuit(w)
	if w.QuitFn() == nil {
		t.Fatal("wireGpuWatchdogQuit did not wire the quit path")
	}

	w.QuitFn()()
	if !f.app.exitConfirmed.Load() {
		t.Error("expected the fallback quit to arm the close-guard bypass")
	}
	if f.quitCalls != 1 {
		t.Errorf("expected exactly one quit call, got %d", f.quitCalls)
	}
	if !strings.Contains(logs.String(), "webview GPU fallback: quitting for the relaunched instance") {
		t.Errorf("expected the fallback-quit Info record, got:\n%s", logs.String())
	}
	if f.app.ShouldPreventClose(context.Background()) {
		t.Error("expected the fallback quit to pass the close guard")
	}
	if len(f.emitted) != 1 {
		t.Errorf("expected no additional exit_requested events, got %v", f.emitted)
	}
}

// TestGpuFallbackQuitNoContextDoesNotArmBypass pins the guard-rail of the
// bypass invariant: without a live Wails context there is no quit, so the
// bypass flag must stay unarmed (a stale armed flag would silently disable
// the close guard for the rest of the process).
func TestGpuFallbackQuitNoContextDoesNotArmBypass(t *testing.T) {
	app := NewApp()
	quitCalls := 0
	app.quitFn = func(context.Context) { quitCalls++ }
	// The guard-rail path announces its refusal at Error — intercepted here
	// and asserted, never leaked into the test output.
	var logs bytes.Buffer
	app.logger = slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))

	app.gpuFallbackQuit()

	if app.exitConfirmed.Load() {
		t.Error("the bypass flag must not be armed without a Wails context")
	}
	if quitCalls != 0 {
		t.Errorf("expected no quit call, got %d", quitCalls)
	}
	if n := strings.Count(logs.String(), "level=ERROR"); n != 1 {
		t.Errorf("expected exactly one ERROR record, got %d:\n%s", n, logs.String())
	}
	if !strings.Contains(logs.String(), "webview GPU fallback: no Wails context; cannot quit for the relaunch") {
		t.Errorf("expected the no-context refusal message, got:\n%s", logs.String())
	}
}
