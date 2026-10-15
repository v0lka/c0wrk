package desktop

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"

	"github.com/v0lka/sp4rk/safeio"
	"github.com/wailsapp/wails/v2/pkg/options"
)

// singleInstanceLockIDSuffixHexLen is the length of the hex(sha256) suffix
// SingleInstanceLockIDFor appends to the prefix: 48 bits of the canonical
// agent-directory path — collision-resistant for every realistic parallel
// c0wrk profile set while keeping the derived platform names (D-Bus bus
// name, darwin lockfile, Windows mutex/window class) compact.
const singleInstanceLockIDSuffixHexLen = 12

// SingleInstanceLockID is the stable PREFIX of the Wails SingleInstanceLock
// UniqueId — the identity all c0wrk processes agree on for "the one running
// instance". The full identity is derived by SingleInstanceLockIDFor as
// SingleInstanceLockID + "_" + hex(sha256(canonical agent dir))[:12], so it
// stays c0wrk-scoped and greppable while binding to the user's concrete
// agent directory (~/.c0wrk by way of config.AgentDir).
//
// ADR-075 originally keyed the identity to the macOS bundle identifier
// (com.wails.{{.Name}} from build/darwin/Info.plist with wails.json
// name=c0wrk-desktop) so that LaunchServices resolving a notification-banner
// activation to the bundle and the single-instance lock naming "this app"
// were one and the same string. That alignment is now DELIBERATELY SEVERED:
// the lock identity is per-agent-directory, so the constant is only the
// human-readable prefix and nothing resolves it as a bundle id anymore. The
// trade is accepted because the Wails relay never depended on the bundle id
// matching — it keys on the UniqueId alone — while a per-dir identity keeps
// parallel agent directories (separate user homes, a relocated ~/.c0wrk) from
// sharing one lock, and the home-dir-derived hash keeps the per-user platform
// namespaces collision-free (see SingleInstanceLockIDFor for the per-platform
// rationale).
//
// See ADR-075 (single-instance lock) and issue #98.
const SingleInstanceLockID = "com.wails.c0wrk-desktop"

// SingleInstanceLockIDFor derives the full Wails SingleInstanceLock UniqueId
// for the agent directory agentDir:
//
//	SingleInstanceLockID + "_" + hex(sha256(canonical agentDir))[:12]
//
// Canonicalization is lexical first, filesystem second: filepath.Clean, then
// filepath.Abs (which Cleans again), then filepath.EvalSymlinks — the last
// degrading softly to the Clean+Abs form when it cannot resolve (a
// not-yet-created agent dir, a dangling link, an unreachable working
// directory for the relative fallback). Deliberately NO user.Current/passwd
// lookup: the home directory is already embedded in config.AgentDir, and
// re-deriving it here would create a second source of truth that can drift
// from every other path helper.
//
// The contract that makes the lock work is determinism: the same agentDir
// must always yield the same id (darwin is fail-CLOSED on a lock miss — a
// drifting id would let a second process boot a full app over the first
// one's state instead of relaying), and different agent dirs must never
// share an id (the lock would wrongly merge two independent profiles).
// That contract imposes an ordering invariant on main.go: derive the id
// (via SingleInstanceOptions) only AFTER AcquireSingleInstanceLock — its
// safeio.MkdirAllReal guarantees agentDir exists, which is what makes
// EvalSymlinks resolve the same way on this launch as on every later one.
// A first run computing the id before the directory exists would fail
// EvalSymlinks and silently pin the unresolved form, while every later run
// would resolve it — two identities for one directory across restarts.
//
// The hash suffix is sized for, and safe in, each platform's derivation of
// the id (Wails v2.16 internal/frontend/desktop/<platform>/single_instance.go):
//   - Linux is fail-OPEN: Wails swallows every D-Bus connect/error and just
//     runs on, so a broken session bus degrades single instance to "maybe
//     two instances" without bricking startup — but an INVALID bus name
//     would kill the relay on every launch, permanently. Wails builds
//     "org." + underscore(id) + ".SingleInstance" (dash/dot → underscore);
//     the suffix appends [0-9a-f] after a '_' inside a single dot-element,
//     so every element stays letter-led and [A-Za-z0-9_]-only — a valid bus
//     name and object path for any agent dir.
//   - darwin is fail-CLOSED: the process that cannot create the lockfile
//     relays and exits, so the id doubles as the lockfile name
//     "<native temp dir>/<id>.lock" (a per-user NSTemporaryDirectory path).
//     Hex keeps it a safe filename; determinism (above) is what keeps the
//     fail-closed semantics honest across launches.
//   - Windows feeds the id into the Win32 object namespace: mutex
//     "wails-app-<id>sim" (session-local, no Global\ prefix) plus window
//     class/name "<id>-sic"/"<id>-siw" for the WM_COPYDATA relay. Hex is
//     namespace-safe, and the home-dir-derived hash keeps parallel user
//     sessions distinguishable even if the names are ever moved to a shared
//     namespace.
//
// See ADR-075 (single-instance lock) and issue #98.
func SingleInstanceLockIDFor(agentDir string) string {
	canonical := filepath.Clean(agentDir)
	if abs, err := filepath.Abs(canonical); err == nil {
		canonical = abs
	}
	if resolved, err := filepath.EvalSymlinks(canonical); err == nil {
		canonical = resolved
	}
	sum := sha256.Sum256([]byte(canonical))
	suffix := hex.EncodeToString(sum[:])[:singleInstanceLockIDSuffixHexLen]
	return SingleInstanceLockID + "_" + suffix
}

// SingleInstanceOptions builds the Wails v2 SingleInstanceLock option wired
// into options.App by main.go. The UniqueId is derived from the agent
// directory via SingleInstanceLockIDFor — the caller must pass it only after
// AcquireSingleInstanceLock has guaranteed the directory exists (the
// ordering invariant documented there; main.go's lock acquisition precedes
// the wails.Run options literal). The option is what turns a LaunchServices-
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
func SingleInstanceOptions(app *App, agentDir string) *options.SingleInstanceLock {
	return &options.SingleInstanceLock{
		UniqueId:               SingleInstanceLockIDFor(agentDir),
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
// OnStartup binds a.wailsCtx() — a relay that arrives during this instance's own
// startup finds ctx nil. That is harmless rather than a bug to fix: startup
// is still mid-flight, the window is revealed by the startup phases anyway,
// and there is nothing to steal focus from yet.
func (a *App) handleSecondInstanceLaunch(data options.SecondInstanceData) {
	a.log().Info("second instance launch relayed; focusing existing window",
		"args", data.Args,
		"working_directory", data.WorkingDirectory)
	if a.wailsCtx() == nil {
		a.log().Debug("second-instance relay arrived before Startup; focus skipped")
		return
	}
	// Same reveal-AND-raise the notification-click callback uses — every
	// focus path funnels through showWindow by design.
	a.showWindow(a.wailsCtx())
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
	// MkdirAllReal + OpenFileNoFollow (instead of the symlink-following
	// MkdirAll/safeio.OpenFile): the fixed, predictable app.lock file must not
	// have its O_CREATE (nor the process-lifetime flock) land through a
	// planted symlink — a dangling link is refused outright, a link to an
	// existing regular file fails the open with ELOOP (no-follow on the final
	// component — unix; on Windows the safeio parity note applies: the final
	// symlink is still resolved, tempered by Windows requiring elevated or
	// developer-mode rights to create symlinks), and MkdirAllReal refuses a
	// link swapped into the lock
	// directory it creates. A pre-existing symlinked directory component is
	// resolved as operator intent, and the caller's existing fail-open
	// contract still applies: the run degrades to "first instance" and the
	// Wails SingleInstanceLock still enforces single instance on its own.
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
