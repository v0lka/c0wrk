package desktop

import (
	"context"
	"errors"
	"time"

	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/v0lka/c0wrk/backend/session"
	"github.com/v0lka/c0wrk/core/updater"
)

// EventAppExitRequested is the global event emitted when a quit attempt is
// intercepted because sessions have live work; the frontend answers through
// the ConfirmExit RPC (no response event — the decision must reach the
// process owning the exit-confirmed flag).
// See specs/contracts/event-catalog.md (app:exit_requested).
const EventAppExitRequested = "app:exit_requested"

// exitGuardPayload is the payload of app:exit_requested; snake_case keys,
// mirroring the other backend → frontend payloads.
type exitGuardPayload struct {
	Sessions []session.ActiveSessionInfo `json:"sessions"`
	// UpdatePending: the intercepted quit was issued by ApplyUpdate (a
	// self-update restart in progress), so the modal presents restart context.
	UpdatePending bool `json:"update_pending"`
}

// updateQuitSlack covers the staged updater's 500ms PID poll granularity plus
// minor scheduling delay.
const updateQuitSlack = 10 * time.Second

// markUpdateQuit records that the updater-driven quit was issued. Time-based
// on purpose: a cancelled interception self-expires once the staged updater
// gives up waiting, so a much later quit is never presented as an update
// restart. See specs/contracts/desktop-frontend.md (Close guard).
func (a *App) markUpdateQuit() {
	a.updateQuitAt.Store(time.Now())
}

// updateQuitPending reports whether an updater-driven quit is still within
// the staged updater's parent-wait window (plus slack); past it, a parent
// quit no longer completes the update and must not claim update context.
func (a *App) updateQuitPending() bool {
	v, ok := a.updateQuitAt.Load().(time.Time)
	if !ok || v.IsZero() {
		return false
	}
	return time.Since(v) <= updater.StagedUpdaterShutdownWait+updateQuitSlack
}

// ShouldPreventClose implements the Wails OnBeforeClose hook: every quit path
// (close button, Cmd+Q / OS quit, runtime.Quit) funnels through it, so this
// one hook guards the whole quit surface — including the confirmed-quit
// re-entry, allowed through by the a.exitConfirmed flag, and the webview-GPU
// watchdog's post-relaunch quit (the sessions it would protect are
// auto-resumed in the relaunched instance, and the dialog would render into
// the grey webview being replaced). An intercepted attempt tears nothing
// down: it emits app:exit_requested (with the active-session list) and
// returns prevent=true; "quit anyway" calls ConfirmExit, so the regular
// Shutdown path still runs.
// See specs/contracts/desktop-frontend.md (Close guard).
func (a *App) ShouldPreventClose(_ context.Context) bool {
	// Geometry must be snapshotted while the window is alive — this hook runs
	// on every quit path before Wails destroys it (see captureWindowGeometry);
	// on every pass, so a resize made under the open modal is still reflected.
	a.captureWindowGeometry()

	if a.exitConfirmed.Load() {
		return false
	}

	active := a.lookupActiveSessions()
	if len(active) == 0 {
		return false
	}

	a.log().Info("quit intercepted: sessions have live work", "count", len(active))

	// The confirmation modal lives in the webview: reveal the window before
	// delivering the request (a.emit tolerates a nil ctx by dropping the
	// event, but that window cannot have active sessions).
	if a.wailsCtx() != nil {
		a.showWindow(a.wailsCtx())
	}
	a.emit(EventAppExitRequested, exitGuardPayload{
		Sessions:      active,
		UpdatePending: a.updateQuitPending(),
	})
	return true
}

// lookupActiveSessions returns the sessions with live background work for the
// close guard. Tests inject a.activeSessionsFn; production reads the backend
// application. Before Startup wires the application there is nothing to lose
// — and preventing close then would strand the user with no dialog to answer.
func (a *App) lookupActiveSessions() []session.ActiveSessionInfo {
	if a.activeSessionsFn != nil {
		return a.activeSessionsFn()
	}
	app := a.application()
	if app == nil {
		return nil
	}
	return app.Manager().ActiveSessions()
}

// ConfirmExit is the frontend-callable counterpart to an intercepted quit: it
// arms the exit-confirmed bypass and triggers a graceful Wails quit, which
// re-enters OnBeforeClose (allowed through by the flag) and runs the normal
// Shutdown sequence. See specs/contracts/desktop-frontend.md (ConfirmExit).
func (a *App) ConfirmExit() error {
	if a.wailsCtx() == nil {
		return errors.New("ConfirmExit: application context is not initialized")
	}
	a.exitConfirmed.Store(true)
	a.log().Info("quit confirmed by user despite active sessions")
	if a.quitFn != nil {
		a.quitFn(a.wailsCtx())
		return nil
	}
	wailsRuntime.Quit(a.wailsCtx())
	return nil
}
