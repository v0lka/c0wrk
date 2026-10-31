package desktop

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/v0lka/sp4rk/safeio"
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

// InstanceLock is the exclusively-held, process-lifetime advisory OS lock
// guarding "the one c0wrk instance" for this user (the Wails relay above is
// the enforcement): its job in main.go is to let a second process know,
// BEFORE it touches shared state, that it must stay read-only toward
// ~/.c0wrk — above all the crash-capture liveness marker, which a transient
// relay would otherwise poison. See specs/decisions/075-single-instance-lock.md
// (D3) and specs/domains/crash-logging.md. The zero value is not a usable
// lock; AcquireSingleInstanceLock constructs it.
type InstanceLock struct {
	file *os.File
}

// AcquireSingleInstanceLock opens (creating if needed) the lock file at path
// and tries to take an exclusive, non-blocking OS lock on it.
//
//   - (lock, true, nil): acquired — keep the lock reachable for the process
//     lifetime (a finalizer would release it under GC).
//   - (nil, false, nil): held by another process — this is the relay; it must
//     not mutate shared state before the Wails relay exits it.
//   - (nil, true, err): setup failed — FAILS OPEN to "first instance": a
//     wedged lock file must never brick startup, and the Wails
//     SingleInstanceLock still enforces single instance on its own.
func AcquireSingleInstanceLock(path string) (*InstanceLock, bool, error) {
	// MkdirAllReal + OpenFileNoFollow (not the symlink-following
	// MkdirAll/safeio.OpenFile): the fixed, predictable app.lock path must not
	// take its O_CREATE or flock through a planted symlink — a dangling link
	// is refused outright, a link to a regular file fails the open with ELOOP
	// (no-follow on the final component; Windows still resolves it, tempered
	// by symlink creation requiring elevated rights there), and MkdirAllReal
	// refuses a link swapped into the directory it creates. A pre-existing
	// symlinked directory component is operator intent, and the fail-open
	// contract above still applies.
	if err := safeio.MkdirAllReal(filepath.Dir(path), 0o750); err != nil {
		return nil, true, fmt.Errorf("single-instance lock: creating lock directory: %w", err)
	}
	file, err := safeio.OpenFileNoFollow(path, os.O_CREATE|os.O_RDWR, 0o640)
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

// Close releases the lock; the OS drops it on every exit path regardless,
// including the relay's os.Exit inside wails.Run.
func (l *InstanceLock) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	err := l.file.Close()
	l.file = nil
	return err
}

// WaitForInstanceLockRelease polls the advisory single-instance lock until it
// can be taken — the previous holder exited — or the timeout expires. It is
// the ordering primitive behind the GPU-fallback relaunch (issue #34); on
// success the lock is released immediately, ownership being taken later by
// the normal startup acquire. Fail-open: a setup error keeps polling to the
// timeout and reports false; the Wails lock has the final say either way.
// See specs/domains/webview-gpu-policy.md.
func WaitForInstanceLockRelease(path string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		if err := safeio.MkdirAllReal(filepath.Dir(path), 0o750); err == nil {
			if file, err := safeio.OpenFileNoFollow(path, os.O_CREATE|os.O_RDWR, 0o640); err == nil {
				held, lockErr := tryLockExclusive(file)
				_ = file.Close()
				if lockErr == nil && held {
					return true
				}
			}
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(gpuRelaunchLockPollInterval)
	}
}
