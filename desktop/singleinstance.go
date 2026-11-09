package desktop

import (
	"github.com/wailsapp/wails/v2/pkg/options"
)

// SingleInstanceLockID is the Wails SingleInstanceLock UniqueId — the one
// identity all c0wrk processes agree on for "the running instance",
// deliberately the macOS bundle identifier: LaunchServices resolves a banner
// activation to the bundle, so the two notions of "this app" stay aligned.
// Keep in sync with the bundle id. See specs/decisions/075-single-instance-lock.md.
const SingleInstanceLockID = "com.wails.c0wrk-desktop"

// SingleInstanceOptions builds the Wails v2 SingleInstanceLock option wired
// into options.App by main.go: a second process relays its launch data to the
// first instance and exits before a window, the database, or any watcher
// exists. The handler is deliberately an UNEXPORTED method — Wails binds every
// exported method of the bound structs to the frontend, and a
// frontend-callable "focus the window" binding is unwanted surface.
// See specs/decisions/075-single-instance-lock.md (D1, D2).
func SingleInstanceOptions(app *App) *options.SingleInstanceLock {
	return &options.SingleInstanceLock{
		UniqueId:               SingleInstanceLockID,
		OnSecondInstanceLaunch: app.handleSecondInstanceLaunch,
	}
}

// handleSecondInstanceLaunch is the OnSecondInstanceLaunch callback: a second
// c0wrk process started and exited after handing us its launch data (on macOS
// typically a LaunchServices activation against a stale bundle registration).
// The only thing this path can do is focus the running window — the
// notification's routing context went to the second process, which never
// initializes a notification center, so session navigation is unrecoverable
// here. Focus-only is the designed behavior.
// See specs/decisions/075-single-instance-lock.md (D2).
//
// A relay arriving before Startup finds a nil ctx and is a logged no-op:
// startup is mid-flight, the startup phases reveal the window anyway, and
// there is nothing to steal focus from yet.
func (a *App) handleSecondInstanceLaunch(data options.SecondInstanceData) {
	a.log().Info("second instance launch relayed; focusing existing window",
		"args", data.Args,
		"working_directory", data.WorkingDirectory)
	if a.wailsCtx() == nil {
		a.log().Debug("second-instance relay arrived before Startup; focus skipped")
		return
	}
	a.showWindow(a.wailsCtx())
}
