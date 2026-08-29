package desktop

import (
	"context"
	"testing"
	"time"

	"github.com/v0lka/c0wrk/backend/session"
	"github.com/v0lka/c0wrk/core/updater"
)

// exitGuardFixture wires an App with the close-guard test hooks: a captured
// event sink (a.wailsEmit), an injectable active-session list, and a captured
// quit call (a.quitFn). Production paths (wailsRuntime calls) stay untouched
// because the hooks take precedence.
type exitGuardFixture struct {
	app       *App
	active    []session.ActiveSessionInfo
	emitted   []string
	payloads  []exitGuardPayload
	quitCalls int
}

func newExitGuardFixture(active []session.ActiveSessionInfo) *exitGuardFixture {
	f := &exitGuardFixture{active: active}
	f.app = NewApp()
	// A non-nil ctx stands in for the Wails context bound in Startup; the
	// guard's wailsRuntime calls are bypassed by the hooks below.
	f.app.ctx = context.Background()
	f.app.activeSessionsFn = func() []session.ActiveSessionInfo { return f.active }
	f.app.quitFn = func(context.Context) { f.quitCalls++ }
	f.app.windowShowFn = func(context.Context) {}
	f.app.wailsEmit = func(eventName string, optionalData ...any) {
		f.emitted = append(f.emitted, eventName)
		if len(optionalData) > 0 {
			if p, ok := optionalData[0].(exitGuardPayload); ok {
				f.payloads = append(f.payloads, p)
			}
		}
	}
	return f
}

// TestShouldPreventClose_NoActiveSessions verifies the guard stays
// transparent when no session has live work — the common quit must not be
// interrupted, and no event may be emitted.
func TestShouldPreventClose_NoActiveSessions(t *testing.T) {
	f := newExitGuardFixture(nil)

	if f.app.ShouldPreventClose(context.Background()) {
		t.Error("expected close to be allowed with no active sessions")
	}
	if len(f.emitted) != 0 {
		t.Errorf("expected no events, got %v", f.emitted)
	}
}

// TestShouldPreventClose_ActiveSessions verifies the interception path: the
// hook prevents the close and emits exactly one app:exit_requested carrying
// the active-session list. windowShowFn is a no-op stand-in for the real
// WindowShow (which requires a live Wails runtime).
func TestShouldPreventClose_ActiveSessions(t *testing.T) {
	f := newExitGuardFixture([]session.ActiveSessionInfo{
		{ID: "sess-1", Name: "Refactor"},
	})

	if !f.app.ShouldPreventClose(context.Background()) {
		t.Error("expected close to be prevented with an active session")
	}
	if len(f.emitted) != 1 || f.emitted[0] != EventAppExitRequested {
		t.Fatalf("expected one %s event, got %v", EventAppExitRequested, f.emitted)
	}
	if len(f.payloads) != 1 || len(f.payloads[0].Sessions) != 1 {
		t.Fatalf("expected the active-session list in the payload, got %+v", f.payloads)
	}
	if got := f.payloads[0].Sessions[0]; got.ID != "sess-1" || got.Name != "Refactor" {
		t.Errorf("payload session mismatch: %+v", got)
	}

	// A repeated quit attempt while the dialog is unanswered must intercept
	// again and re-emit, so the frontend can (re)show the modal.
	if !f.app.ShouldPreventClose(context.Background()) {
		t.Error("expected repeated close attempt to be prevented again")
	}
	if len(f.emitted) != 2 {
		t.Errorf("expected the request to be re-emitted, got %d events", len(f.emitted))
	}
}

// TestShouldPreventClose_ExitConfirmedBypass verifies the confirmed-quit
// bypass: after ConfirmExit arms the flag, the hook must let the (re-entered)
// quit through without emitting anything.
func TestShouldPreventClose_ExitConfirmedBypass(t *testing.T) {
	f := newExitGuardFixture([]session.ActiveSessionInfo{{ID: "sess-1"}})

	if !f.app.ShouldPreventClose(context.Background()) {
		t.Fatal("expected the initial quit to be intercepted")
	}
	if err := f.app.ConfirmExit(); err != nil {
		t.Fatalf("ConfirmExit failed: %v", err)
	}
	if f.quitCalls != 1 {
		t.Errorf("expected exactly one quit call, got %d", f.quitCalls)
	}
	if f.app.ShouldPreventClose(context.Background()) {
		t.Error("expected the confirmed quit to be allowed through")
	}
	if len(f.emitted) != 1 {
		t.Errorf("expected no additional events after confirmation, got %v", f.emitted)
	}
}

// TestConfirmExit_NoContext verifies the early-startup failure path: without
// a Wails context there is nothing to quit, and the method must return an
// error instead of arming the bypass flag.
func TestConfirmExit_NoContext(t *testing.T) {
	app := NewApp()
	app.quitFn = func(context.Context) {}

	if err := app.ConfirmExit(); err == nil {
		t.Error("expected an error when the application context is not initialized")
	}
	if app.exitConfirmed.Load() {
		t.Error("the bypass flag must not be armed on failure")
	}
}

// TestShouldPreventClose_UpdatePendingContext verifies the update context in
// the payload: an updater-driven quit (quitApp armed in startup.go) reports
// update_pending=true, a plain quit reports false. The user-facing contract:
// only a quit that still belongs to a live staged updater may be presented as
// a restart.
func TestShouldPreventClose_UpdatePendingContext(t *testing.T) {
	active := []session.ActiveSessionInfo{{ID: "sess-1"}}

	// Plain quit: never armed → no update context.
	f := newExitGuardFixture(active)
	f.app.ShouldPreventClose(context.Background())
	if f.payloads[0].UpdatePending {
		t.Error("a plain quit must not carry update_pending=true")
	}

	// Updater-driven quit: armed just before the quit → update context.
	f2 := newExitGuardFixture(active)
	f2.app.markUpdateQuit()
	f2.app.ShouldPreventClose(context.Background())
	if !f2.payloads[0].UpdatePending {
		t.Error("a freshly armed update quit must carry update_pending=true")
	}

	// Cancelled long ago: the staged updater's wait window has lapsed, so a
	// quit no longer completes the update and must not claim restart context.
	f3 := newExitGuardFixture(active)
	f3.app.updateQuitAt.Store(time.Now().Add(-(updater.StagedUpdaterShutdownWait + updateQuitSlack + time.Second)))
	f3.app.ShouldPreventClose(context.Background())
	if f3.payloads[0].UpdatePending {
		t.Error("an expired update-quit marker must not carry update_pending=true")
	}
}
