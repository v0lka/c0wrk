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
// intercepted because sessions have live work. Payload:
// {"sessions": [{id, name, compacting}], "update_pending": bool}. The
// frontend answers through the ConfirmExit RPC — there is no response event,
// because the decision must reach the process that owns the exit-confirmed
// flag. See specs/contracts/event-catalog.md (Global Events).
const EventAppExitRequested = "app:exit_requested"

// exitGuardPayload is the payload of app:exit_requested. JSON keys are
// snake_case, mirroring the other backend → frontend event payloads.
type exitGuardPayload struct {
	Sessions []session.ActiveSessionInfo `json:"sessions"`
	// UpdatePending reports that the intercepted quit was issued by
	// ApplyUpdate (a self-update restart in progress), so the confirmation
	// modal presents restart context instead of a plain quit.
	UpdatePending bool `json:"update_pending"`
}

// updateQuitSlack extends the window during which an armed update-quit
// marker still counts as pending, covering the staged updater's 500ms PID
// poll granularity plus minor scheduling delay.
const updateQuitSlack = 10 * time.Second

// markUpdateQuit records that the updater-driven quit was issued (see the
// QuitApp closure in startup.go). Time-based on purpose: if the user cancels
// the interception, the marker self-expires once the staged updater has given
// up waiting, so a much later quit is never presented as an update restart.
func (a *App) markUpdateQuit() {
	a.updateQuitAt.Store(time.Now())
}

// updateQuitPending reports whether an updater-driven quit is still live:
// armed and within the staged updater's parent-wait window (plus slack).
// After the window a parent quit no longer completes the update, so the
// close guard must not claim update context anymore.
func (a *App) updateQuitPending() bool {
	v, ok := a.updateQuitAt.Load().(time.Time)
	if !ok || v.IsZero() {
		return false
	}
	return time.Since(v) <= updater.StagedUpdaterShutdownWait+updateQuitSlack
}

// ShouldPreventClose implements the Wails OnBeforeClose hook: it decides
// whether a quit attempt must be intercepted while sessions have live work.
// On every platform the close button, Cmd+Q / the OS quit path, and
// runtime.Quit all funnel through OnBeforeClose — so this single hook guards
// the whole quit surface, but it also means the confirmed quit re-enters it:
// ConfirmExit arms a.exitConfirmed first, and this hook lets that attempt
// through instead of intercepting it again.
//
// An intercepted attempt tears nothing down. The hook emits
// app:exit_requested (with the active-session list) and returns
// prevent=true; the frontend shows a confirmation modal, and a user "quit
// anyway" calls ConfirmExit to quit for real, so the regular Shutdown path
// (window bounds, pending-action drain, backend cleanup) still runs.
func (a *App) ShouldPreventClose(_ context.Context) bool {
	if a.exitConfirmed.Load() {
		return false
	}

	active := a.lookupActiveSessions()
	if len(active) == 0 {
		return false
	}

	a.log().Info("quit intercepted: sessions have live work", "count", len(active))

	// The confirmation modal lives in the webview. Make sure the window is
	// visible — the quit may arrive while it is hidden — then deliver the
	// request. a.emit tolerates a nil ctx (Startup not run yet) by dropping
	// the event with a warning, but that window cannot have active sessions.
	if a.ctx != nil {
		a.showWindow(a.ctx)
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
	if a.app == nil {
		return nil
	}
	return a.app.Manager().ActiveSessions()
}

// ConfirmExit is the frontend-callable counterpart to an intercepted quit:
// the user confirmed quitting despite active sessions. It arms the
// exit-confirmed bypass and triggers a graceful Wails quit, which re-enters
// OnBeforeClose (allowed through by the flag) and then runs the normal
// Shutdown sequence.
func (a *App) ConfirmExit() error {
	if a.ctx == nil {
		return errors.New("ConfirmExit: application context is not initialized")
	}
	a.exitConfirmed.Store(true)
	a.log().Info("quit confirmed by user despite active sessions")
	if a.quitFn != nil {
		a.quitFn(a.ctx)
		return nil
	}
	wailsRuntime.Quit(a.ctx)
	return nil
}
