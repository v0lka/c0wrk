package desktop

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/v0lka/sp4rk/safeio"
	"github.com/wailsapp/wails/v2/pkg/options"
)

// SingleInstanceLockID is the Wails SingleInstanceLock UniqueId — the single
// identity all c0wrk processes agree on for "the one running instance". It is
// deliberately the macOS bundle identifier (com.wails.{{.Name}} from
// build/darwin/Info.plist with wails.json name=c0wrk-desktop): LaunchServices
// resolves a notification-banner activation to the bundle, so keying the
// single-instance identity to the same string keeps the two notions of "this
// app" aligned. Platform derivations of the id (lockfile name on darwin,
// session-bus name on Linux, mutex/window class on Windows) are built by the
// Wails frontends; keep this in sync with the bundle id if it ever changes.
//
// See ADR-075 (single-instance lock) and issue #98.
const SingleInstanceLockID = "com.wails.c0wrk-desktop"

// SingleInstanceOptions builds the Wails v2 SingleInstanceLock option wired
// into options.App by main.go. The option is what turns a LaunchServices-
// spawned second process (macOS notification-banner click against a stale or
// rebuilt bundle registration — issue #98's failure mode) into a relay: the
// second process detects the first instance's platform lock, forwards its
// launch data, and exits at the very start of the Wails frontend Run — before
// a window is created, before OnStartup opens the database, before any
// watcher or PTY exists. The first instance receives the data through
// App.handleSecondInstanceLaunch and focuses its window.
//
// The handler is deliberately an UNEXPORTED method wrapped here: Wails binds
// every exported method of the structs in options.App.Bind to the frontend,
// exempting only the four lifecycle hooks (OnStartup/OnShutdown/OnDomReady/
// OnBeforeClose), and a frontend-callable "focus the window" binding is
// surface nobody needs.
func SingleInstanceOptions(app *App) *options.SingleInstanceLock {
	return &options.SingleInstanceLock{
		UniqueId:               SingleInstanceLockID,
		OnSecondInstanceLaunch: app.handleSecondInstanceLaunch,
	}
}

// handleSecondInstanceLaunch is the OnSecondInstanceLaunch callback: a second
// c0wrk process started (on macOS typically LaunchServices activating the app
// from a delivered notification banner whose bundle registration no longer
// matches the running process — a rebuilt .app, an updater-swapped install
// tree, or a stale LaunchServices registration) and is exiting after handing
// us its launch data. The only thing this path can do for the user is bring
// the running window forward: the notification's routing context (session/
// project ids) is delivered to whichever process macOS attributes the click
// to, and on this path that was the second process, which never reaches
// notification-center initialization — so the session navigation of the
// normal click path (EventNotificationClicked → useNotificationClicks) is not
// recoverable here. Focus-only is the designed behavior; see ADR-075.
//
// Wails starts the second-instance processor goroutine in NewFrontend, before
// OnStartup binds a.ctx — a relay that arrives during this instance's own
// startup finds ctx nil. That is harmless rather than a bug to fix: startup
// is still mid-flight, the window is revealed by the startup phases anyway,
// and there is nothing to steal focus from yet.
func (a *App) handleSecondInstanceLaunch(data options.SecondInstanceData) {
	a.log().Info("second instance launch relayed; focusing existing window",
		"args", data.Args,
		"working_directory", data.WorkingDirectory)
	if a.ctx == nil {
		a.log().Debug("second-instance relay arrived before Startup; focus skipped")
		return
	}
	// Same reveal-AND-raise the notification-click callback uses — every
	// focus path funnels through showWindow by design.
	a.showWindow(a.ctx)
}

// InstanceLock is the exclusively-held, process-lifetime OS lock guarding
// "the one c0wrk instance" for this user. It is an advisory cross-process
// gate only (enforcement is the Wails SingleInstanceLock relay above); its
// job in main.go is to let a second process know, BEFORE it touches any
// shared state, that it must stay read-only toward ~/.c0wrk — most
// importantly the crash-capture liveness marker, whose stash/overwrite by a
// transient relay process would poison the next start's unclean-shutdown
// forensics (crashlog.RemoveMarker is pid-aware and leaves foreign markers
// in place, so an orphaned relay marker survives as a false "did not shut
// down cleanly" warning). See ADR-075 and specs/domains/crash-logging.md.
//
// The zero value is not a usable lock; AcquireSingleInstanceLock constructs
// it. Close is nil-tolerant so main can defer it unconditionally.
type InstanceLock struct {
	file *os.File
}

// AcquireSingleInstanceLock opens (creating if needed) the lock file at path
// and tries to take an exclusive, non-blocking OS lock on it.
//
// Return values:
//   - (lock, true, nil): the lock was acquired — this process is the first
//     instance. The caller MUST keep the returned lock reachable for the
//     process lifetime (an os.File finalizer would close the fd and silently
//     release the lock under GC); main.go defers Close for exactly that.
//   - (nil, false, nil): the lock is held by another process — this process
//     is a second instance and must not mutate shared state (crash capture,
//     markers) before the Wails relay exits it.
//   - (nil, true, err): the lock could not be set up (unopenable path, IO
//     error). This FAILS OPEN to "first instance": a wedged lock file must
//     never brick app startup, and the Wails SingleInstanceLock still
//     enforces single instance on its own. The caller should log the error.
func AcquireSingleInstanceLock(path string) (*InstanceLock, bool, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, true, fmt.Errorf("single-instance lock: creating lock directory: %w", err)
	}
	file, err := safeio.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o640)
	if err != nil {
		return nil, true, fmt.Errorf("single-instance lock: opening lock file: %w", err)
	}
	held, err := tryLockExclusive(file)
	if err != nil {
		_ = file.Close()
		return nil, true, fmt.Errorf("single-instance lock: locking lock file: %w", err)
	}
	if !held {
		_ = file.Close()
		return nil, false, nil
	}
	return &InstanceLock{file: file}, true, nil
}

// Close releases the lock (the fd close drops the flock / byte-range lock
// and is also what the OS does for any exit path, including the second
// instance's os.Exit inside wails.Run). Nil-tolerant and idempotent.
func (l *InstanceLock) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	err := l.file.Close()
	l.file = nil
	return err
}
