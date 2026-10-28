package desktop

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/v0lka/c0wrk/backend"
	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/backend/logger"
	"github.com/v0lka/c0wrk/backend/session"
	"github.com/v0lka/c0wrk/core/markitdown"
	"github.com/v0lka/c0wrk/internal/webaccess"
)

// App holds the Wails application state. Frontend API methods live on the
// embedded *backend.FrontendAPI (specs/architecture/layers.md); App itself
// retains only lifecycle management, the native dialogs, and Wails event
// plumbing.
type App struct {
	ctx context.Context
	*backend.FrontendAPI

	// ── Cross-goroutine lifecycle state ─────────────────────────────────
	//
	// ctx, FrontendAPI (above), app, logger, db, sessionLogger and
	// shutdownHardDeadline are written by the OnStartup goroutine and read
	// concurrently by: the second-instance relay (spawned before the
	// OnStartup goroutine even exists, so a relay during this instance's own
	// startup reads with no happens-before edge to the writes), the Wails
	// binding-dispatch goroutines, and the close/quit path (which can fire
	// while a first-run Startup is still mid-flight).
	//
	// stateMu is the happens-before edge: writes go through the set* helpers,
	// reads outside the OnStartup goroutine go through the matching
	// accessors. The accessors are leaf helpers — no caller code under the
	// lock, so the critical sections are single field loads/stores and
	// cannot deadlock. OnStartup-goroutine reads need no lock (program
	// order) but use the accessors anyway.
	stateMu sync.RWMutex

	// app is the central ViewModel (owns builder, manager, persister), kept
	// for Startup/Shutdown orchestration. Guarded by stateMu — see above.
	app *backend.Application

	// logger used during Startup before FrontendAPI is constructed.
	// Guarded by stateMu — see the block comment above.
	logger *slog.Logger

	// wailsLogger bridges Wails internal messages to a persistent wails.log
	// file, duplicated to the session log once that logger is ready.
	wailsLogger *wailsLogAdapter

	// webviewGpuWatchdog is the (Linux-only, possibly nil) DMABUF-failure
	// watchdog armed in main and started from DomReady
	// (specs/domains/webview-gpu-policy.md). Published strictly before the
	// Wails UI loop begins, so a plain field is safely published (same
	// ordering contract as wailsLogger).
	webviewGpuWatchdog *webviewGpuWatchdog

	db *sql.DB // shared SQLite connection; opened in Startup, closed in Shutdown. Guarded by stateMu.

	// sessionLogger is stored so Shutdown can close it on early Startup exits
	// (e.g. tool installation failing before FrontendAPI is wired). Guarded
	// by stateMu.
	sessionLogger *logger.SessionLogger

	// Wails event-listener infrastructure (used only in startup.go listeners)
	pendingConfirmations   sync.Map
	pendingAskUser         sync.Map
	pendingStepLimit       sync.Map
	pendingPlanApprovals   sync.Map
	pendingGoalProposals   sync.Map
	pendingGoalSuggestions sync.Map

	// judgeWG tracks in-flight runJudgeEvaluation goroutines so Shutdown can
	// wait for them before tearing down the backend application.
	judgeWG sync.WaitGroup

	// judgeInFlight holds the confirm_ids with a live runJudgeEvaluation
	// goroutine, bounding judge spawning to one evaluation per confirmation —
	// a repeat frame for an in-flight evaluation is dropped instead of
	// starting a second full-budget LLM call that could only lose the
	// first-responder race. Entries live exactly as long as their goroutine;
	// judgeWG remains the shutdown barrier.
	judgeInFlight sync.Map

	// wailsEmit, when non-nil, replaces wailsRuntime.EventsEmit — the test
	// seam for the phase helpers (W-19/W-23). Production keeps it nil.
	wailsEmit func(eventName string, optionalData ...any)

	// eventTap, when non-nil, observes every a.emit call alongside the Wails
	// dispatch — the web-agnostic seam a side transport mounts to receive
	// exactly the events the webview receives (specs/domains/web-access.md);
	// a nil tap leaves emit unchanged. The tap must never block, alter, or
	// fail the emit. Atomic pointer on purpose: the web-access mount
	// registers — and hot-restart swaps — the tap after event producers are
	// live, while RPC goroutines already read it; the Load/Store pair is the
	// happens-before edge. Set it through setEventTap.
	eventTap atomic.Pointer[func(name string, args []interface{})]

	// ── Web access sidecar (internal/webaccess, ADR-101) ─────────────────
	// Contract: specs/domains/web-access.md.
	//
	// webAccess is the live sidecar; nil while not running. Atomic pointer:
	// Startup mounts it, Shutdown and SetWebAccessEnabled stop it,
	// GetWebAccessInfo reads it — different goroutines. webAccessMu
	// serializes the lifecycle TRANSITIONS (startup mount, Shutdown stop,
	// SetWebAccessEnabled toggle) so two of them can never interleave into a
	// half-torn-down server; handle reads ride the atomic pointer.
	webAccess   atomic.Pointer[webaccess.Server]
	webAccessMu sync.Mutex

	// webAccessStatic is the SPA asset tree the sidecar serves at /, wired
	// from main.go strictly before wails.Run begins (the
	// webviewGpuWatchdog publishing contract), so a plain field is safely
	// published. Nil (tests, a stripped embed) degrades the sidecar to
	// API+WS only: the static route 404s.
	webAccessStatic fs.FS

	// liveConfig and liveConfigPath are the desktop's handle on the live
	// config object and its on-disk path, captured in buildFrontendAPI.
	// liveConfig is the SAME pointer FrontendAPI.Init seeds — the shared
	// live config the backend mutates in place under its configMu. Desktop
	// never touches backend-owned sections through it; it exists solely so
	// the desktop-owned web_access section can be flipped and persisted
	// without a backend edit. The persist marshals and writes the WHOLE
	// config file while holding only webAccessMu (desktop cannot take
	// configMu — the zero-backend-edit constraint), so a concurrent backend
	// save can land inside that window and the two atomic renames are
	// last-writer-wins: an older desktop snapshot may drop a just-persisted
	// backend mutation on disk (memory is unaffected; the next backend save
	// heals the file). Rare (operator-paced toggles), self-healing, and
	// accepted. Guarded by stateMu.
	liveConfig     *config.Config
	liveConfigPath string

	// batcherPtr holds the lazily-created event batcher between the
	// session-event producers and the raw transport (a.emit) — one
	// c0wrk:events:batch envelope per ~16ms flush
	// (specs/contracts/event-catalog.md); batcherOnce guards the one-time
	// construction. See event_batcher.go.
	batcherOnce sync.Once
	batcherPtr  atomic.Pointer[EventBatcher]

	// reloadAppFn, when non-nil, replaces wailsRuntime.WindowReloadApp in
	// (*App).reloadFrontend — the test seam for the deferred wake reload.
	// Production keeps it nil.
	reloadAppFn func(ctx context.Context)

	// toolsBinPath is the managed tools bin directory (e.g. ~/.c0wrk/tools/bin/),
	// set during Phase 2 and prepended to PATH so exec.CommandContext calls
	// resolve managed binaries (rg, uv, markitdown).
	toolsBinPath string

	// exitConfirmed bypasses the close guard (ShouldPreventClose) once the
	// user confirmed quitting despite active sessions; Wails routes every
	// quit path through OnBeforeClose, so the confirmed quit must not be
	// intercepted a second time (specs/contracts/desktop-frontend.md, close
	// guard). Armed only immediately before an imminent, uncancellable quit
	// (ConfirmExit and gpuFallbackQuit store it in the same breath as the
	// quit), so an armed flag never outlives the process it was armed for.
	// Any change that makes quit cancellable after arming must reset this
	// flag on cancellation.
	exitConfirmed atomic.Bool

	// updateQuitAt records when the updater-driven quit (ApplyUpdate →
	// quitApp) was issued, flagging an intercepted quit as update_pending so
	// the modal can present restart context. A timestamp, not a sticky flag,
	// for honesty about cancellation: once the parent misses
	// StagedUpdaterShutdownWait the quit no longer completes the update and
	// must not be presented as one. Zero = never armed. Atomic: quitApp runs
	// on a Wails RPC goroutine, OnBeforeClose on the main thread.
	updateQuitAt atomic.Value // time.Time

	// activeSessionsFn, when non-nil, replaces the backend active-session
	// lookup in the close guard; quitFn replaces wailsRuntime.Quit in
	// ConfirmExit — test seams, nil in production.
	activeSessionsFn func() []session.ActiveSessionInfo
	quitFn           func(ctx context.Context)

	// shutdownHardDeadline is the hard budget for the WHOLE Shutdown
	// teardown, set from config (shutdown.hardDeadline) during Startup; zero
	// falls back to defaultShutdownHardDeadline. Enforced by the shutdown
	// watchdog (specs/domains/crash-logging.md): on expiry the process logs
	// at Error and exits. Guarded by stateMu — see above.
	shutdownHardDeadline time.Duration

	// launchStderrObserved records whether this launch has a usable stderr
	// capture (main.go, from the crashlog.Install outcome), so the Settings
	// running-policy snapshot derives from the same fact the wails.Run
	// options consumed (specs/domains/webview-gpu-policy.md). Zero value =
	// unobserved: the safe direction. Guarded by stateMu — see above.
	launchStderrObserved bool

	// shutdownExitFn, when non-nil, replaces the forced exit in the shutdown
	// watchdog's expiry path so tests observe it without dying; nil in
	// production (crashlog.ForceExit(0): marker removal + banner + os.Exit).
	shutdownExitFn func(code int)

	// windowGeomCache snapshots the window geometry captured from the LIVE
	// window by the close guard. On Linux Wails destroys the window before
	// the OnShutdown hook runs, so Shutdown persists this snapshot — a
	// post-destroy read returns the creation default and would clobber the
	// value PersistWindowBounds saved on resize. Atomic pointer: captured on
	// the close-handler goroutine, consumed by Shutdown on the main
	// goroutine.
	windowGeomCache atomic.Pointer[WindowBounds]

	// windowGetSizeFn / windowIsMaximisedFn replace the wailsRuntime window
	// queries in the geometry capture/persist paths — the per-call seam
	// pattern of windowRaiseFn et al. (the runtime calls fatal on a context
	// no live runtime owns). Production keeps them nil.
	windowGetSizeFn     func(ctx context.Context) (int, int)
	windowIsMaximisedFn func(ctx context.Context) bool

	// embeddedLLMStopFn, when non-nil, replaces the backend embedded-LLM
	// stop (FrontendAPILifecycle.StopEmbeddedLLM — specs/domains/embedded-llm.md)
	// in stopEmbeddedLLM — the test seam; production keeps it nil.
	embeddedLLMStopFn func(ctx context.Context) error

	// windowShowFn, when non-nil, replaces wailsRuntime.WindowShow in
	// showWindow — and therefore every path that reveals the window (the
	// startup phases, OnDomReady, the close guard) — so tests drive them
	// without a live Wails runtime, which panics on a foreign context.
	// Production keeps it nil.
	windowShowFn func(ctx context.Context)

	// windowRaiseFn / windowUnminimiseFn / windowIsMinimisedFn are the
	// per-call seams behind showWindow's platform branches (WindowShow /
	// WindowUnminimise / WindowIsMinimised). Same purpose as windowShowFn:
	// tests drive the platform activation matrix without a live runtime;
	// production keeps them nil.
	windowRaiseFn       func(ctx context.Context)
	windowUnminimiseFn  func(ctx context.Context)
	windowIsMinimisedFn func(ctx context.Context) bool

	// x11PagerActivateFn, when non-nil, replaces the Linux EWMH pager-source
	// activation attempt in showWindow's linux branch (production:
	// x11ActivateOwnWindow) — the test seam; production keeps it nil.
	x11PagerActivateFn func() bool

	// ── System notifications (notifications.go) ────────────────────────────
	//
	// Test seams for the Wails notification runtime calls (the wailsEmit /
	// windowShowFn precedent): each replaces exactly one wailsRuntime
	// package function, which fatals on a context no live runtime owns.
	// Production keeps every one of them nil.

	// notificationsInitMu serializes InitNotifications: a Wails RPC goroutine
	// and an early frontend init must not interleave the initialize/register
	// steps. notificationsInitialized memoizes the successful init so a second
	// call never registers a second OnNotificationResponse callback (the Wails
	// callback slot is a single package-level variable, but replacing it would
	// also churn goroutine state for nothing). A FAILED init is not memoized —
	// the next call retries (a Linux session bus can appear later).
	notificationsInitMu      sync.Mutex
	notificationsInitialized atomic.Bool

	// onNotificationResponseFn replaces wailsRuntime.OnNotificationResponse.
	onNotificationResponseFn func(ctx context.Context, cb func(result wailsRuntime.NotificationResult))
	// notificationsInitFn replaces wailsRuntime.InitializeNotifications.
	notificationsInitFn func(ctx context.Context) error
	// notificationsAuthFn replaces wailsRuntime.RequestNotificationAuthorization.
	notificationsAuthFn func(ctx context.Context) (bool, error)
	// notificationsAuthCheckFn replaces wailsRuntime.CheckNotificationAuthorization
	// (the non-prompting read the Settings permission hint needs).
	notificationsAuthCheckFn func(ctx context.Context) (bool, error)
	// notificationsSendFn replaces wailsRuntime.SendNotification.
	notificationsSendFn func(ctx context.Context, options wailsRuntime.NotificationOptions) error
	// notificationsCleanupFn replaces wailsRuntime.CleanupNotifications.
	notificationsCleanupFn func(ctx context.Context)
	// notificationsSendViaWailsFn replaces the Linux dial-failure fallback
	// behind wailsRuntime.SendNotification — the test seam; production keeps
	// it nil.
	notificationsSendViaWailsFn func(ctx context.Context, options wailsRuntime.NotificationOptions) error
}

// NewApp seeds FrontendAPI as a non-nil zero-value shell so early frontend
// RPC calls (before Startup finishes) hit the per-method nil guards instead
// of panicking; Startup INITIALIZES that seed IN PLACE (buildFrontendAPI →
// Init). The pointer is published exactly once here and never reassigned, so
// the Wails binding dispatch's per-call dereference can never race a swap
// (review finding #52).
func NewApp() *App {
	return &App{
		FrontendAPI: &backend.FrontendAPI{},
	}
}

// SetWailsLogger stores the Wails log adapter so that the session logger
// can be wired as a delegate once it is initialized in Startup.
func (a *App) SetWailsLogger(wl *wailsLogAdapter) {
	a.wailsLogger = wl
}

// SetWebviewGpuWatchdog stores the (possibly nil) DMABUF-failure watchdog
// armed in main before wails.Run
// (specs/domains/webview-gpu-policy.md); nil off Linux and whenever
// detection does not apply.
func (a *App) SetWebviewGpuWatchdog(w *webviewGpuWatchdog) {
	a.webviewGpuWatchdog = w
}

// SetWebviewGpuStderrObserved records whether this launch's stderr capture is
// usable (main.go, from the crashlog.Install outcome), so the Settings
// running-policy snapshot derives from the same input as the wails.Run
// options (specs/domains/webview-gpu-policy.md). Exported deliberately —
// internal startup plumbing rides the Wails binding surface by the same
// precedent as SetWebviewGpuWatchdog; the value is a boolean main computes.
func (a *App) SetWebviewGpuStderrObserved(observed bool) {
	a.setLaunchStderrObserved(observed)
}

// startWebviewGpuWatchdog launches the bounded startup scan from DomReady —
// the DMABUF failure prints at the webview's first paint, so from here on
// this run's stderr capture contains everything the scan needs. The quit
// path is wired to the live Wails context here, where it exists.
func (a *App) startWebviewGpuWatchdog() {
	w := a.webviewGpuWatchdog
	if w == nil {
		return
	}
	if w.quitFn == nil {
		if a.wailsCtx() != nil {
			w.quitFn = a.gpuFallbackQuit
		}
	}
	w.start()
}

// gpuFallbackQuit is the webview-GPU watchdog's post-relaunch quit; it arms
// the close-guard bypass in the same breath as the quit, mirroring
// ConfirmExit (specs/domains/webview-gpu-policy.md) — the dying webview
// cannot answer the confirmation modal.
func (a *App) gpuFallbackQuit() {
	if a.wailsCtx() == nil {
		// No live runtime: nothing to quit through, and arming the bypass
		// now would leave a stale armed flag (see the exitConfirmed
		// invariant).
		a.log().Error("webview GPU fallback: no Wails context; cannot quit for the relaunch")
		return
	}
	a.exitConfirmed.Store(true)
	a.log().Info("webview GPU fallback: quitting for the relaunched instance (close-guard bypass armed)")
	if a.quitFn != nil {
		a.quitFn(a.wailsCtx())
		return
	}
	wailsRuntime.Quit(a.wailsCtx())
}

// PickDirectory opens the native directory picker, reopening at the directory
// the user last picked (persisted in dialog_state.json and validated to still
// exist; a non-cancelled pick updates that memory). Must remain on App — it
// requires the Wails context (specs/contracts/desktop-frontend.md).
func (a *App) PickDirectory() (string, error) {
	if a.wailsCtx() == nil {
		return "", errors.New("PickDirectory: application context is not initialized")
	}

	options := wailsRuntime.OpenDialogOptions{
		Title:                "Select Workspace Directory",
		CanCreateDirectories: true,
	}
	// DefaultDirectory must reference an existing directory or the runtime
	// returns an error without showing any dialog; LoadDialogState drops
	// stale/non-existent paths, so adopting its value is always safe.
	if last := LoadDialogState(a.agentDir()); last.LastDirectory != "" {
		options.DefaultDirectory = last.LastDirectory
	}

	dir, err := wailsRuntime.OpenDirectoryDialog(a.wailsCtx(), options)
	if err != nil {
		return "", err
	}
	// On cancel OpenDirectoryDialog returns ("", nil) — keep the previous
	// memory instead of overwriting it with an empty path.
	rememberDialogDirectory(a.agentDir(), dir, a.log())
	return dir, nil
}

// attachmentFilterPattern builds the Wails FileFilter pattern string for the
// markitdown supported extensions, using the conventional "*.ext" form (e.g.
// "*.pdf;*.docx;*.pptx"). This is the cross-platform Wails convention — on
// macOS the backend strips the "*." prefix internally and matches by extension.
func attachmentFilterPattern() string {
	exts := markitdown.SupportedExtensions()
	cleaned := make([]string, 0, len(exts))
	for _, ext := range exts {
		if e := strings.TrimPrefix(ext, "."); e != "" {
			cleaned = append(cleaned, "*."+e)
		}
	}
	return strings.Join(cleaned, ";")
}

// PickAttachmentFiles opens a native multi-select file picker restricted to
// the document formats markitdown can convert; on cancel
// OpenMultipleFilesDialog returns an empty slice with nil error, returned
// as-is. Must remain on App — it requires the Wails context
// (specs/contracts/desktop-frontend.md, including the no-all-files-filter
// rule).
func (a *App) PickAttachmentFiles() ([]string, error) {
	if a.wailsCtx() == nil {
		return nil, errors.New("PickAttachmentFiles: application context is not initialized")
	}

	// No "All files" filter: a "*" pattern resolves to a dynamic macOS UTType
	// that corrupts the panel's whole content-type filter
	// (specs/contracts/desktop-frontend.md). The two filters below already
	// cover every extension the backend accepts.
	return wailsRuntime.OpenMultipleFilesDialog(a.wailsCtx(), wailsRuntime.OpenDialogOptions{
		Title: "Attach files",
		Filters: []wailsRuntime.FileFilter{
			{
				DisplayName: "Supported documents",
				Pattern:     attachmentFilterPattern(),
			},
			{
				DisplayName: "Images",
				Pattern:     "*.png;*.jpg;*.jpeg;*.gif;*.webp",
			},
		},
	})
}

// PickStudyDocument opens a native single-select file picker backing the
// Papers panel's "pick a local document" gesture beside the Study field; on
// cancel OpenFileDialog returns ("", nil) — returned as-is so the frontend
// distinguishes "user cancelled" from a failure. Must remain on App — it
// requires the Wails context (specs/contracts/desktop-frontend.md).
func (a *App) PickStudyDocument() (string, error) {
	if a.wailsCtx() == nil {
		return "", errors.New("PickStudyDocument: application context is not initialized")
	}

	// Same filter rationale as PickAttachmentFiles (no "All files" entry —
	// specs/contracts/desktop-frontend.md).
	return wailsRuntime.OpenFileDialog(a.wailsCtx(), wailsRuntime.OpenDialogOptions{
		Title: "Study a local document",
		Filters: []wailsRuntime.FileFilter{
			{
				DisplayName: "Supported documents",
				Pattern:     attachmentFilterPattern(),
			},
		},
	})
}

// PickAndImportThemes opens a native multi-select CSS picker and imports
// every chosen file as a user theme in one action; on cancel it returns
// (nil, nil) — nothing imported, the frontend maps the null result to "user
// cancelled". Per-file outcomes (including failures) come back in the result
// list. This picker is the ONLY import entry point: the import functions are
// deliberately not FrontendAPI methods, so the binding generator never
// publishes a path-taking RPC to the renderer
// (specs/contracts/desktop-frontend.md, theming).
func (a *App) PickAndImportThemes() ([]backend.ThemeImportResult, error) {
	if a.wailsCtx() == nil {
		return nil, errors.New("PickAndImportThemes: application context is not initialized")
	}

	paths, err := wailsRuntime.OpenMultipleFilesDialog(a.wailsCtx(), wailsRuntime.OpenDialogOptions{
		Title: "Import Themes",
		Filters: []wailsRuntime.FileFilter{
			{
				DisplayName: "Theme files",
				Pattern:     "*.css",
			},
		},
	})
	if err != nil {
		return nil, err
	}
	if len(paths) == 0 {
		// Cancelled: import nothing and report no error.
		return nil, nil
	}

	// The picker above is the sole path source; see the doc comment for why
	// the import entry points are not FrontendAPI methods.
	return backend.ImportThemesFromPaths(a.frontendAPI(), paths), nil
}

// setShutdownHardDeadline records the configured hard deadline (Startup only).
func (a *App) setShutdownHardDeadline(d time.Duration) {
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	a.shutdownHardDeadline = d
}

// shutdownDeadline returns the effective hard deadline for the Shutdown
// teardown: the value Startup recorded from shutdown.hardDeadline, or
// defaultShutdownHardDeadline when unset (0) or Startup never got that far.
func (a *App) shutdownDeadline() time.Duration {
	a.stateMu.RLock()
	defer a.stateMu.RUnlock()
	if a.shutdownHardDeadline > 0 {
		return a.shutdownHardDeadline
	}
	return defaultShutdownHardDeadline
}

// setLaunchStderrObserved records whether this launch's stderr is observable
// (main.go, before wails.Run).
func (a *App) setLaunchStderrObserved(observed bool) {
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	a.launchStderrObserved = observed
}

// stderrObserved reports the launch's stderr-observation fact for the
// FrontendAPI init (zero value = unobserved, the safe direction).
func (a *App) stderrObserved() bool {
	a.stateMu.RLock()
	defer a.stateMu.RUnlock()
	return a.launchStderrObserved
}

// setLogger records the active logger (Startup only).
func (a *App) setLogger(log *slog.Logger) {
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	a.logger = log
}

// setSessionLogger records the session log handle (Startup only).
func (a *App) setSessionLogger(l *logger.SessionLogger) {
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	a.sessionLogger = l
}

// sessionLog returns the session log handle for the close path, or nil when
// Startup has not opened (or has already swapped) one.
func (a *App) sessionLog() *logger.SessionLogger {
	a.stateMu.RLock()
	defer a.stateMu.RUnlock()
	return a.sessionLogger
}

// setContext records the Wails context (Startup only).
func (a *App) setContext(ctx context.Context) {
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	a.ctx = ctx
}

// wailsCtx returns the Wails application context, or nil before Startup
// bound it. Deliberately UNexported: every exported App method is promoted
// onto the Wails binding surface and becomes renderer-callable. Cross-
// goroutine readers — the second-instance relay, binding-dispatch
// goroutines, the close/quit path — must use this instead of touching
// a.ctx directly.
func (a *App) wailsCtx() context.Context {
	a.stateMu.RLock()
	defer a.stateMu.RUnlock()
	return a.ctx
}

// setDatabase records the shared SQLite connection (Startup only).
func (a *App) setDatabase(db *sql.DB) {
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	a.db = db
}

// database returns the shared SQLite connection for the close path, or nil
// before Startup opened it.
func (a *App) database() *sql.DB {
	a.stateMu.RLock()
	defer a.stateMu.RUnlock()
	return a.db
}

// setApplication records the backend Application (Startup only).
func (a *App) setApplication(app *backend.Application) {
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	a.app = app
}

// application returns the backend Application, or nil before Startup built
// it. Cross-goroutine readers (the close guard, Shutdown) must use this
// instead of touching a.app directly.
func (a *App) application() *backend.Application {
	a.stateMu.RLock()
	defer a.stateMu.RUnlock()
	return a.app
}

// frontendAPI returns the live FrontendAPI — never nil: NewApp seeds a
// non-nil zero-value shell (see NewApp for the publish-once contract). The
// shell has no wiring until Startup initializes it: callers that need live
// state must treat it accordingly (the Lifecycle accessors guard
// internally).
func (a *App) frontendAPI() *backend.FrontendAPI {
	a.stateMu.RLock()
	defer a.stateMu.RUnlock()
	return a.FrontendAPI
}

// log returns the instance logger, falling back to slog.Default() when nil.
func (a *App) log() *slog.Logger {
	a.stateMu.RLock()
	log := a.logger
	a.stateMu.RUnlock()
	if log != nil {
		return log
	}
	return slog.Default()
}

// setEventTap installs (fn != nil) or removes (fn == nil) the side event
// tap; the atomic-swap rationale lives on the eventTap field.
func (a *App) setEventTap(fn func(name string, args []interface{})) {
	if fn == nil {
		a.eventTap.Store(nil)
		return
	}
	a.eventTap.Store(&fn)
}

// emit dispatches a Wails event — the single event funnel
// (specs/domains/web-access.md). Callers must not invoke it before Startup
// binds a.wailsCtx() (or before a.wailsEmit is set in tests). The event tap,
// when set, observes every dispatch and never alters, blocks, or fails it —
// enforced by the panic isolation in invokeEventTap, not by convention.
func (a *App) emit(eventName string, optionalData ...any) {
	if tap := a.eventTap.Load(); tap != nil {
		a.invokeEventTap(*tap, eventName, optionalData)
	}
	if a.wailsEmit != nil {
		a.wailsEmit(eventName, optionalData...)
		return
	}
	if a.wailsCtx() == nil {
		a.log().Warn("emit called with nil ctx, event dropped", "event", eventName)
		return
	}
	wailsRuntime.EventsEmit(a.wailsCtx(), eventName, optionalData...)
}

// invokeEventTap calls the side event tap with panic isolation: the tap
// contract — "must never block, alter, or fail the emit" — is enforced here
// rather than trusted from the tap implementation (a panicking tap would
// otherwise kill whichever goroutine called emit). The panic is logged and
// the emit continues.
func (a *App) invokeEventTap(tap func(name string, args []interface{}), eventName string, args []interface{}) {
	defer func() {
		if r := recover(); r != nil {
			a.log().Warn("event tap panicked; emit continues",
				"event", eventName, "panic", r)
		}
	}()
	tap(eventName, args)
}

// showWindow reveals AND raises the main window. Every reveal path funnels
// through here — the startup phases, OnDomReady, the exit guard, the
// notification-click callback — and the raise applies to all of them by
// design: reveal without raise would leave a buried window "shown". The
// per-platform activation matrix (Linux pager-then-present, darwin
// show-only, Windows guarded restore) is specified in
// specs/domains/frontend/system-notifications.md (§ Click → window
// activation).
func (a *App) showWindow(ctx context.Context) {
	if a.windowShowFn != nil {
		a.windowShowFn(ctx)
		return
	}
	switch showWindowPlatform {
	case "linux":
		// Own EWMH pager-source activation first (see window_activation_linux.go):
		// the Wails present() carries a stale/zero timestamp that KWin's focus
		// stealing prevention rejects, leaving the taskbar merely flashing.
		if a.tryX11PagerActivation() {
			return
		}
		// Fallback (Wayland, headless, no matching window): gtk_window_present
		// — raise + deiconify + focus in one call.
		a.windowUnminimise(ctx)
	case "darwin":
		// makeKeyAndOrderFront + activateIgnoringOtherApps.
		a.windowRaise(ctx)
	case "windows":
		// Show maps + activates; Restore only when really minimized, or a
		// maximized window would be unmaximized (WPF Form.Restore).
		a.windowRaise(ctx)
		if a.windowIsMinimised(ctx) {
			a.windowUnminimise(ctx)
		}
	default:
		// Unknown platform: keep the historical behavior (plain show).
		a.windowRaise(ctx)
	}
}

// showWindowPlatform selects the activation branch of showWindow — a package
// var (not a build tag) so tests on any platform exercise every branch;
// production reads runtime.GOOS, exactly like notificationAuthorizationPlatform.
var showWindowPlatform = runtime.GOOS

// windowRaise dispatches to wailsRuntime.WindowShow (the per-call seam lives
// on App — see the windowRaiseFn field).
func (a *App) windowRaise(ctx context.Context) {
	if a.windowRaiseFn != nil {
		a.windowRaiseFn(ctx)
		return
	}
	wailsRuntime.WindowShow(ctx)
}

// windowUnminimise dispatches to wailsRuntime.WindowUnminimise.
func (a *App) windowUnminimise(ctx context.Context) {
	if a.windowUnminimiseFn != nil {
		a.windowUnminimiseFn(ctx)
		return
	}
	wailsRuntime.WindowUnminimise(ctx)
}

// tryX11PagerActivation attempts the Linux EWMH pager-source activation of
// the app's own window behind the x11PagerActivateFn seam; true means the
// pager activation ran and the caller skips the Wails present() fallback.
// The seam is nil in production (the cgo implementation runs); tests fake it.
func (a *App) tryX11PagerActivation() bool {
	if a.x11PagerActivateFn != nil {
		return a.x11PagerActivateFn()
	}
	return x11ActivateOwnWindow()
}

// windowIsMinimised dispatches to wailsRuntime.WindowIsMinimised.
func (a *App) windowIsMinimised(ctx context.Context) bool {
	if a.windowIsMinimisedFn != nil {
		return a.windowIsMinimisedFn(ctx)
	}
	return wailsRuntime.WindowIsMinimised(ctx)
}

func (a *App) resolvePendingMessage(sessionID, role, matchField, matchValue string, extra map[string]any) error {
	fa := a.frontendAPI()
	if fa == nil {
		return nil
	}
	return fa.ResolvePendingMessage(sessionID, role, matchField, matchValue, extra)
}

// ── Web access sidecar: mount, teardown, control RPCs (ADR-101) ──────────
//
// Everything the browser sidecar needs from the app mounts at this seam;
// the full contract is specs/domains/web-access.md. The control RPCs below
// are methods on App, never on the embedded *FrontendAPI — the reflection
// dispatcher sees the FrontendAPI surface plus the adapter's pinned method
// set, never the App itself — so a browser tab can neither read the
// credential state nor flip the switch that feeds it.

// webAccessStopBudget bounds one sidecar stop — deliberately small: a Stop
// is a listener close plus a WebSocket fan-out abort on a stateless server,
// nothing worth making a quit wait for (specs/domains/web-access.md). On
// expiry Server.Stop force-closes what a graceful stop could not finish.
const webAccessStopBudget = 3 * time.Second

// setConfigRef captures the desktop's handle on the live config object and
// its path (buildFrontendAPI, Startup goroutine). See the liveConfig field
// comment for the ownership discipline.
func (a *App) setConfigRef(cfg *config.Config, path string) {
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	a.liveConfig = cfg
	a.liveConfigPath = path
}

// configRef returns the captured live config and path (either may be empty on
// a Startup that never reached buildFrontendAPI).
func (a *App) configRef() (cfg *config.Config, path string) {
	a.stateMu.RLock()
	defer a.stateMu.RUnlock()
	return a.liveConfig, a.liveConfigPath
}

// SetWebAccessStaticFS stores the SPA asset tree the sidecar serves. Exported
// for main.go by the same precedent as SetWebviewGpuWatchdog: internal startup
// plumbing rides the Wails binding surface so the process-level wiring can
// hand over a value only it holds (the embedded asset tree). Call it strictly
// before wails.Run — the plain-field publishing contract in the field comment.
func (a *App) SetWebAccessStaticFS(static fs.FS) {
	a.webAccessStatic = static
}

// WebAccessInfo is the GetWebAccessInfo payload: the Settings section's whole
// view of the sidecar. Every field is specified in
// specs/domains/web-access.md (the WebAccessInfo table).
type WebAccessInfo struct {
	Enabled                  bool   `json:"enabled"`
	Running                  bool   `json:"running"`
	URL                      string `json:"url"`
	Port                     int    `json:"port"`
	ConfiguredPort           int    `json:"configured_port"`
	ConfiguredBind           string `json:"configured_bind"`
	BindIsLoopback           bool   `json:"bind_is_loopback"`
	AllowInsecureLAN         bool   `json:"allow_insecure_lan"`
	AllowInsecureLANRequired bool   `json:"allow_insecure_lan_required"`
	Token                    string `json:"token"`
	TokenPinned              bool   `json:"token_pinned"`
	TLSEnabled               bool   `json:"tls_enabled"`
	TLSCert                  string `json:"tls_cert"`
	TLSKey                   string `json:"tls_key"`
	CertStatus               string `json:"cert_status"`
	CertDetail               string `json:"cert_detail"`
}

// webAccessURLHost maps a listener host to the host a browser opens: an
// unspecified wildcard host (0.0.0.0, ::) becomes "localhost" — the wildcard
// names "every interface", not an openable address. Shared by the URL
// renderer and the certificate status check (ADR-102 decision 5) so both
// name the SAME browser-facing host the certificate's SANs must cover.
func webAccessURLHost(host string) string {
	if ip := net.ParseIP(host); ip != nil && ip.IsUnspecified() {
		return "localhost"
	}
	return host
}

// webAccessBrowserURL renders the URL a browser opens for the sidecar's
// resolved listener address: the scheme follows the transport the sidecar is
// actually serving (https under web_access.tls, ADR-102 decision 1 — what
// the operator sees is always what the browser must open) plus host:port,
// with one rewrite — an unspecified wildcard host becomes "localhost"
// (webAccessURLHost).
func webAccessBrowserURL(addr string, tlsEnabled bool) string {
	scheme := "http"
	if tlsEnabled {
		scheme = "https"
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		// Not host:port — never expected from a TCP listener address; pass
		// the raw value through rather than invent a port.
		return scheme + "://" + addr
	}
	return scheme + "://" + net.JoinHostPort(webAccessURLHost(host), port)
}

// GetWebAccessInfo reports the sidecar state for Settings → Web Access
// (desktop-only by construction: an App method, so the web reflection surface
// cannot serve it back to a browser tab). The section read is a whole-struct
// snapshot under webAccessMu — the same lock the control-RPC writers mutate
// it under, and this method is itself renderer-callable, so it races them on
// real paths; Token is a multi-word string header whose torn read is
// UB-class, so the fields are copied once, never walked field-by-field off
// the live config. Classification and URL rendering stay OUTSIDE the lock on
// purpose — no file I/O rides the transition lock (ADR-102).
func (a *App) GetWebAccessInfo() WebAccessInfo {
	info := WebAccessInfo{}
	var sec config.WebAccessConfig
	if cfg, _ := a.configRef(); cfg != nil {
		a.webAccessMu.Lock()
		sec = cfg.WebAccess
		a.webAccessMu.Unlock()
		info.Enabled = sec.Enabled
		info.ConfiguredBind = sec.Bind
		info.BindIsLoopback = config.IsLoopbackBind(sec.Bind)
		info.AllowInsecureLAN = sec.AllowInsecureLAN
		// State-only hint: would these pins be refused if the section were
		// enabled? (ValidateWebAccessExposure over the snapshot with Enabled
		// forced true — the same predicate every boundary applies.)
		asEnabled := sec
		asEnabled.Enabled = true
		info.AllowInsecureLANRequired = config.ValidateWebAccessExposure(asEnabled) != nil
		info.ConfiguredPort = sec.Port
		info.TokenPinned = sec.Token != ""
		info.TLSEnabled = sec.TLS.Enabled
		info.TLSCert = sec.TLS.Cert
		info.TLSKey = sec.TLS.Key
		info.CertStatus, info.CertDetail = classifyWebAccessCert(
			sec.TLS.Cert, sec.TLS.Key, sec.Bind)
	}
	if srv := a.webAccess.Load(); srv != nil {
		info.Running = true
		if addr, ok := srv.Addr(); ok {
			info.URL = webAccessBrowserURL(addr, srv.TLSEnabled())
			if _, port, perr := net.SplitHostPort(addr); perr == nil {
				info.Port, _ = strconv.Atoi(port)
			}
		}
		info.Token = srv.AuthToken()
	} else {
		// Not running: the configured token is what the next start would
		// use (an empty value means that start mints and persists one).
		// Same snapshot as above — a second configRef read here could
		// interleave a writer between the two reads.
		info.Token = sec.Token
	}
	return info
}

// The warning-channel refresh (ADR-102): transport-affecting control RPCs —
// enable, connection, TLS, the insecure-LAN opt-in (the accepted-risk warning
// rides the flag), and certificate regeneration (a fresh valid pair heals a
// warned-unusable section, CS #67) — recompute the section's
// config-load-warning contribution AFTER their webAccessMu critical section,
// outside the lock on purpose (see RefreshWebAccessLoadWarnings for the
// lock-order rule). The refresh is idempotent, so it also runs after
// refusals and rollbacks, reflecting whatever state the attempt left
// behind. The token RPCs deliberately do not refresh: the credential cannot
// move the warning state.
func (a *App) refreshWebAccessLoadWarnings() {
	a.frontendAPI().Lifecycle().RefreshWebAccessLoadWarnings()
}

// SetWebAccessEnabled persists the web_access.enabled switch and applies it
// live: enabling starts the sidecar, disabling stops it. Same-state is a
// no-op; a failed disk write rolls the in-memory flip back
// (specs/domains/web-access.md). Enabling preflights a configured-but-
// unusable TLS pair fail-closed (ADR-102), and with the master experimental
// gate closed it persists operator intent but mounts nothing (ADR-101
// decision 13). Locking: webAccessMu serializes the whole transition
// against the startup mount and the Shutdown stop; the config write is the
// desktop-owned whole-file window documented on the liveConfig field; the
// warning refresh runs after the unlock (ADR-102).
func (a *App) SetWebAccessEnabled(on bool) error {
	a.webAccessMu.Lock()
	err := a.setWebAccessEnabledLocked(on)
	a.webAccessMu.Unlock()
	a.refreshWebAccessLoadWarnings()
	return err
}

func (a *App) setWebAccessEnabledLocked(on bool) error {
	cfg, cfgPath := a.configRef()
	if cfg == nil || cfgPath == "" {
		return errors.New("config not initialized")
	}
	if cfg.WebAccess.Enabled == on {
		return nil
	}

	// ADR-102: a flip onto an unusable TLS is refused up front — fail-closed
	// at this interactive boundary.
	if on && cfg.WebAccess.TLS.Enabled {
		if err := config.ValidateWebAccessTLSPair(cfg.WebAccess.TLS); err != nil {
			return fmt.Errorf("cannot enable web access: %w", err)
		}
	}
	// Insecure-LAN gate: enabling onto a non-loopback plaintext bind without
	// the explicit allow_insecure_lan opt-in is refused up front — the same
	// exposure predicate the load, save and mount paths enforce
	// (specs/domains/web-access.md).
	if on {
		next := cfg.WebAccess
		next.Enabled = true
		if err := config.ValidateWebAccessExposure(next); err != nil {
			return fmt.Errorf("cannot enable web access: %w", err)
		}
	}

	prev := cfg.WebAccess
	cfg.WebAccess.Enabled = on
	if err := config.Save(cfg, cfgPath); err != nil {
		cfg.WebAccess = prev
		return fmt.Errorf("failed to persist web access config: %w", err)
	}

	if on {
		if !webAccessMountAllowed(cfg, cfg.Experimental.Enabled) {
			// ADR-101 decision 13: with the master gate closed the toggle
			// still persists operator intent (the same paper state a
			// restart would produce) but mounts no sidecar; opening the
			// gate live-follows through applyExperimentalWebAccess.
			a.log().Debug("web access enabled while the experimental gate is closed; the sidecar stays unmounted",
				"experimental_enabled", cfg.Experimental.Enabled)
			return nil
		}
		srv, err := a.startWebAccessLocked(a.log())
		if err != nil {
			// The persisted switch stays on (operator intent; the next
			// launch retries) but the failure must be visible now.
			return fmt.Errorf("web access enabled, but the sidecar failed to start: %w", err)
		}
		logWebAccessReady(a.log(), srv)
		return nil
	}

	sctx, cancel := context.WithTimeout(context.Background(), webAccessStopBudget)
	defer cancel()
	if err := a.stopWebAccessLocked(sctx); err != nil {
		return fmt.Errorf("web access disabled, but the sidecar stop failed: %w", err)
	}
	return nil
}

// applyExperimentalWebAccess is the backend's post-persist hook for
// experimental.enabled (UpdateExperimentalFeatures → the OnExperimentalChanged
// FrontendAPIConfig seam, ADR-101): gate open mounts an enabled-and-stopped
// sidecar, gate closed stops a running one (webAccessStopBudget), everything
// else is a no-op (specs/domains/web-access.md). Failure is fail-soft and
// loud exactly like the startup mount: an Error log, the desktop app fully
// usable, the persisted switch retrying on the next launch or successful
// toggle. Locking: webAccessMu serializes the whole transition against the
// startup mount, the Shutdown stop and the control RPCs; no lock-order cycle
// with the backend — UpdateExperimentalFeatures holds configMu while this
// takes webAccessMu, and desktop code never takes configMu.
func (a *App) applyExperimentalWebAccess(enabled bool) {
	a.webAccessMu.Lock()
	defer a.webAccessMu.Unlock()

	cfg, _ := a.configRef()
	if cfg == nil {
		// A Startup that never reached buildFrontendAPI: nothing to mount,
		// and no live config to read the section's own switch from.
		return
	}

	switch {
	case webAccessMountAllowed(cfg, enabled):
		srv, err := a.startWebAccessLocked(a.log())
		if err != nil {
			a.log().Error("web access sidecar failed to start after the experimental gate opened; continuing without it", "error", err)
			return
		}
		// Idempotent on a live server: a repeated open returns the running
		// sidecar instead of double-binding, so this is quiet for that case.
		logWebAccessReady(a.log(), srv)
	case !enabled:
		if a.webAccess.Load() == nil {
			return
		}
		sctx, cancel := context.WithTimeout(context.Background(), webAccessStopBudget)
		defer cancel()
		if err := a.stopWebAccessLocked(sctx); err != nil {
			a.log().Error("web access sidecar failed to stop after the experimental gate closed", "error", err)
		}
	}
}

// SetWebAccessConnection persists the pinned bind and port (Settings →
// Web Access: the single address field parses into these two) and applies
// them live — a running sidecar hot-restarts onto the new connection. The
// token is deliberately NOT part of this call: the credential has its own
// RPCs, all serialized by webAccessMu. Empty bind = the loopback default;
// port 0 = auto, 1–65535 = pinned; a same-pin request is a no-op
// (specs/domains/web-access.md). If the restart with the new settings fails,
// the previous pin is restored, persisted, and the sidecar brought back on
// it (see restartWebAccessOrRollbackLocked). Locking: webAccessMu, like
// SetWebAccessEnabled; the warning refresh runs after the unlock (ADR-102).
func (a *App) SetWebAccessConnection(bind string, port int) error {
	a.webAccessMu.Lock()
	err := a.setWebAccessConnectionLocked(bind, port)
	a.webAccessMu.Unlock()
	a.refreshWebAccessLoadWarnings()
	return err
}

func (a *App) setWebAccessConnectionLocked(bind string, port int) error {
	cfg, cfgPath := a.configRef()
	if cfg == nil || cfgPath == "" {
		return errors.New("config not initialized")
	}
	bind = strings.TrimSpace(bind)
	if bind == "" {
		bind = config.DefaultWebAccessBind
	}
	if err := config.ValidateWebAccessBind(bind); err != nil {
		return err
	}
	if err := config.ValidateWebAccessPort(port); err != nil {
		return err
	}
	// Insecure-LAN gate on the re-bind boundary: refused before anything
	// mutates or persists so the caller's error is a single honest step
	// instead of a failed restart rollback. A disabled section keeps the
	// paper-state doctrine: the pin persists regardless and the exposure
	// gate fires at the enable boundary (and at load/save).
	next := cfg.WebAccess
	next.Bind = bind
	next.Port = port
	if err := config.ValidateWebAccessExposure(next); err != nil {
		return err
	}

	if cfg.WebAccess.Bind == bind && cfg.WebAccess.Port == port {
		return nil
	}

	prev := cfg.WebAccess
	cfg.WebAccess.Bind = bind
	cfg.WebAccess.Port = port
	if err := config.Save(cfg, cfgPath); err != nil {
		cfg.WebAccess = prev
		return fmt.Errorf("failed to persist web access config: %w", err)
	}
	return a.restartWebAccessOrRollbackLocked(prev, cfg, cfgPath, "connection")
}

// RegenerateWebAccessToken mints a fresh persistent credential (the Settings
// → Web Access button) and hot-restarts a running sidecar onto it — every
// open browser tab must sign in again with the token Settings now shows.
// With the sidecar stopped the persisted write is the whole effect. Failure
// ordering and locking discipline: see restartWebAccessOrRollbackLocked and
// specs/domains/web-access.md (webAccessMu).
func (a *App) RegenerateWebAccessToken() error {
	a.webAccessMu.Lock()
	defer a.webAccessMu.Unlock()

	cfg, cfgPath := a.configRef()
	if cfg == nil || cfgPath == "" {
		return errors.New("config not initialized")
	}
	tok, err := webaccess.GenerateToken()
	if err != nil {
		return fmt.Errorf("web access: generating auth token: %w", err)
	}

	prev := cfg.WebAccess
	cfg.WebAccess.Token = tok
	if err := config.Save(cfg, cfgPath); err != nil {
		cfg.WebAccess = prev
		return fmt.Errorf("failed to persist web access config: %w", err)
	}
	return a.restartWebAccessOrRollbackLocked(prev, cfg, cfgPath, "token regeneration")
}

// SetWebAccessToken persists an operator-typed credential (the Settings →
// Web Access token field) and applies it live exactly like the rotation
// above. The value is trimmed and bounds-checked by
// config.ValidateWebAccessToken; the empty value is rejected — emptiness
// means GENERATED, and clearing the field reverts the UI instead of
// overwriting an existing credential. A request matching the stored token is
// a no-op; failure ordering and locking mirror RegenerateWebAccessToken.
func (a *App) SetWebAccessToken(token string) error {
	a.webAccessMu.Lock()
	defer a.webAccessMu.Unlock()

	cfg, cfgPath := a.configRef()
	if cfg == nil || cfgPath == "" {
		return errors.New("config not initialized")
	}
	token = strings.TrimSpace(token)
	if token == cfg.WebAccess.Token {
		return nil
	}
	if err := config.ValidateWebAccessToken(token); err != nil {
		return err
	}

	prev := cfg.WebAccess
	cfg.WebAccess.Token = token
	if err := config.Save(cfg, cfgPath); err != nil {
		cfg.WebAccess = prev
		return fmt.Errorf("failed to persist web access config: %w", err)
	}
	return a.restartWebAccessOrRollbackLocked(prev, cfg, cfgPath, "token change")
}

// Web-access TLS certificate statuses (ADR-102 decision 5): the cert_status
// vocabulary. The empty string (the zero value, not a constant) means no
// cert path is configured — nothing to classify.
const (
	webAccessCertStatusOK         = "ok"         // loads and covers the bind within its validity window
	webAccessCertStatusMismatch   = "mismatch"   // leaf does not cover the pinned bind's browser-facing host
	webAccessCertStatusUnreadable = "unreadable" // pair cannot be loaded or parsed
	webAccessCertStatusExpired    = "expired"    // outside the validity window (either end)
)

// SetWebAccessTLS persists the web_access.tls subsection (the HTTPS switch,
// ADR-102 decision 1) and applies it live: a running sidecar is hot-restarted
// onto the new transport. The caller authors both PEM paths explicitly — the
// key path is never derived from the cert path. An enabled subsection with
// empty paths is refused (config.ValidateWebAccessTLS), and a non-empty pair
// is loaded BEFORE the no-op check or any persist — the interactive boundary
// of the ADR-102 usability rule, with the commit check's own two-bucket
// error split (see setWebAccessTLSLocked); a broken pair never reaches disk
// or the sidecar. The subsection deliberately consults neither
// web_access.enabled nor the experimental gate. Failure ordering mirrors the
// other pin writers (persist rollback, restart restore); locking:
// webAccessMu, with the warning refresh after the unlock (ADR-102) — the
// HTTPS flip is precisely the transition that must move the
// config-load-warning banner.
func (a *App) SetWebAccessTLS(enabled bool, cert, key string) error {
	a.webAccessMu.Lock()
	err := a.setWebAccessTLSLocked(enabled, cert, key)
	a.webAccessMu.Unlock()
	a.refreshWebAccessLoadWarnings()
	return err
}

func (a *App) setWebAccessTLSLocked(enabled bool, cert, key string) error {
	cfg, cfgPath := a.configRef()
	if cfg == nil || cfgPath == "" {
		return errors.New("config not initialized")
	}
	cert = strings.TrimSpace(cert)
	key = strings.TrimSpace(key)
	if cert != "" && key != "" {
		// Refuse an unloadable pair before the no-op check or any persist,
		// with the commit check's own two-bucket error split (NOT the
		// cert_status vocabulary — see classifyWebAccessCert): a key that
		// does not match its certificate is a mismatch ("does not match"),
		// every other load failure an unreadable pair.
		if _, err := tls.LoadX509KeyPair(cert, key); err != nil {
			if strings.Contains(err.Error(), "does not match") {
				return fmt.Errorf("web access: TLS private key %q does not match the certificate %q: %w", key, cert, err)
			}
			return fmt.Errorf("web access: TLS certificate/key pair (%q, %q) is unreadable: %w", cert, key, err)
		}
	}
	next := config.WebAccessTLSConfig{Enabled: enabled, Cert: cert, Key: key}
	if err := config.ValidateWebAccessTLS(next); err != nil {
		return err
	}
	// Insecure-LAN gate on the TLS-off boundary: the transport must not
	// silently become a plaintext LAN exposure through the very switch that
	// was added to encrypt it; refusing here keeps the caller's error one
	// honest step instead of a failed restart rollback.
	exposureNext := cfg.WebAccess
	exposureNext.TLS = next
	if err := config.ValidateWebAccessExposure(exposureNext); err != nil {
		return err
	}
	if cfg.WebAccess.TLS == next {
		return nil
	}

	prev := cfg.WebAccess
	cfg.WebAccess.TLS = next
	if err := config.Save(cfg, cfgPath); err != nil {
		cfg.WebAccess.TLS = prev.TLS
		return fmt.Errorf("failed to persist web access config: %w", err)
	}
	return a.restartWebAccessOrRollbackLocked(prev, cfg, cfgPath, "TLS switch")
}

// SetWebAccessAllowInsecureLAN persists the explicit accepted-risk opt-in
// for the one exposure shape refused by default: an enabled, non-loopback
// bind served over plain HTTP (specs/domains/web-access.md — the exposure
// gate the load/save paths and the sidecar mount enforce). Flag-off while
// the section sits in that shape is refused — the operator must first
// disable web access, re-bind loopback, or enable TLS; a disabled section's
// flag is pure paper state and flips freely. The flag never changes a
// RUNNING listener (transport and bind untouched — no hot-restart); the
// warning refresh runs after the unlock (the accepted-risk warning rides
// the flag). Locking: webAccessMu, exactly like SetWebAccessTLS.
func (a *App) SetWebAccessAllowInsecureLAN(allow bool) error {
	a.webAccessMu.Lock()
	err := a.setWebAccessAllowInsecureLANLocked(allow)
	a.webAccessMu.Unlock()
	a.refreshWebAccessLoadWarnings()
	return err
}

func (a *App) setWebAccessAllowInsecureLANLocked(allow bool) error {
	cfg, cfgPath := a.configRef()
	if cfg == nil || cfgPath == "" {
		return errors.New("config not initialized")
	}
	if cfg.WebAccess.AllowInsecureLAN == allow {
		return nil
	}
	next := cfg.WebAccess
	next.AllowInsecureLAN = allow
	if err := config.ValidateWebAccessExposure(next); err != nil {
		return err
	}
	prev := cfg.WebAccess
	cfg.WebAccess.AllowInsecureLAN = allow
	if err := config.Save(cfg, cfgPath); err != nil {
		cfg.WebAccess = prev
		return fmt.Errorf("failed to persist web access config: %w", err)
	}
	return nil
}

// GenerateWebAccessCertificate regenerates the app-owned self-signed pair
// for the CURRENTLY pinned bind (ADR-102 decision 3) at the fixed locations
// ~/.c0wrk/web-access.crt + .key, persists the pair's paths into
// web_access.tls (never flipping tls.enabled, never touching
// operator-authored paths), and hot-restarts a running sidecar onto the
// fresh certificate. Works while the sidecar is stopped too. The files are
// written BEFORE the config save — the reverse would persist paths at files
// that do not exist yet; leftover files on a failed persist are harmless
// (the idempotent standard pair nothing references until the paths persist).
// Failure ordering mirrors the other control RPCs; locking: webAccessMu.
func (a *App) GenerateWebAccessCertificate() error {
	err := a.generateWebAccessCertificateLocked()
	// Regeneration can move the section's load-warning contribution
	// (CS #67): a fresh valid pair heals a warned-unusable one. After the
	// unlock, mirroring the other control RPCs.
	a.refreshWebAccessLoadWarnings()
	return err
}

func (a *App) generateWebAccessCertificateLocked() error {
	a.webAccessMu.Lock()
	defer a.webAccessMu.Unlock()

	cfg, cfgPath := a.configRef()
	if cfg == nil || cfgPath == "" {
		return errors.New("config not initialized")
	}
	agentDir := config.AgentDir()
	certPath := config.WebAccessCertPath(agentDir)
	keyPath := config.WebAccessKeyPath(agentDir)
	if err := webaccess.GenerateWebAccessCertificate(cfg.WebAccess.Bind, certPath, keyPath); err != nil {
		return err
	}

	prev := cfg.WebAccess
	next := config.WebAccessTLSConfig{
		Enabled: cfg.WebAccess.TLS.Enabled,
		Cert:    certPath,
		Key:     keyPath,
	}
	if cfg.WebAccess.TLS != next {
		cfg.WebAccess.TLS = next
		if err := config.Save(cfg, cfgPath); err != nil {
			cfg.WebAccess.TLS = prev.TLS
			return fmt.Errorf("failed to persist web access config: %w", err)
		}
	}
	// Hot-restart even when the persisted paths did not change: regeneration
	// replaced the key material on disk, and a running sidecar keeps serving
	// the pair it loaded at start until it is rebuilt.
	return a.restartWebAccessOrRollbackLocked(prev, cfg, cfgPath, "certificate generation")
}

// classifyWebAccessCert computes the cert_status/cert_detail pair of
// GetWebAccessInfo (ADR-102 decision 5): whenever a cert path is configured,
// the pair is loaded exactly the way the sidecar loads it and the leaf is
// checked against the pinned bind — SAN coverage of the bind's
// browser-facing host (the same rewrite the rendered URL applies, so the
// status certifies the address the browser actually opens) and the validity
// window. The check order is fixed for determinism: unreadable → mismatch →
// expired → ok; an unconfigured cert path is the empty status.
func classifyWebAccessCert(certPath, keyPath, bind string) (status, detail string) {
	if certPath == "" {
		return "", ""
	}
	host := webAccessURLHost(bind)
	pair, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return webAccessCertStatusUnreadable, fmt.Sprintf(
			"the certificate pair could not be loaded (%v) — the sidecar will refuse to start while this pair is configured; fix the files or regenerate the pair", err)
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return webAccessCertStatusUnreadable, fmt.Sprintf(
			"the certificate could not be parsed (%v) — the sidecar will refuse to start while this pair is configured; fix the files or regenerate the pair", err)
	}
	if !webAccessCertCoversHost(leaf, host) {
		return webAccessCertStatusMismatch, fmt.Sprintf(
			"the certificate does not cover %q (the current bind) — regenerate the pair for this address or point cert/key at a pair that covers it", host)
	}
	if !time.Now().Before(leaf.NotAfter) {
		return webAccessCertStatusExpired, fmt.Sprintf(
			"the certificate expired %s — regenerate the pair to renew it", leaf.NotAfter.UTC().Format(time.RFC3339))
	}
	if time.Now().Before(leaf.NotBefore) {
		// Same clock-derived bucket: outside the validity window.
		return webAccessCertStatusExpired, fmt.Sprintf(
			"the certificate is not valid until %s — check the system clock or regenerate the pair", leaf.NotBefore.UTC().Format(time.RFC3339))
	}
	return webAccessCertStatusOK, ""
}

// webAccessCertCoversHost reports whether the leaf's SANs cover host: an IP
// host must appear among the IP SANs, a name host among the DNS SANs
// (RFC 6125 — the two lists are not interchangeable; a DNS SAN may carry the
// RFC 6125 §6.4.3 wildcard form).
func webAccessCertCoversHost(leaf *x509.Certificate, host string) bool {
	if ip := net.ParseIP(host); ip != nil {
		for _, san := range leaf.IPAddresses {
			if san.Equal(ip) {
				return true
			}
		}
		return false
	}
	for _, san := range leaf.DNSNames {
		if dnsSANCovers(san, host) {
			return true
		}
	}
	return false
}

// dnsSANCovers reports whether one DNS SAN pattern covers host: exact match,
// or the RFC 6125 §6.4.3 wildcard — a single "*" as the complete left-most
// label matching exactly one label ("*.example.com" covers "app.example.com"
// but not "example.com" nor "a.b.example.com").
func dnsSANCovers(pattern, host string) bool {
	pattern = strings.ToLower(pattern)
	host = strings.ToLower(host)
	if pattern == host {
		return true
	}
	wildLabel, wildRest, wildOK := strings.Cut(pattern, ".")
	if !wildOK || wildLabel != "*" {
		return false
	}
	hostLabel, hostRest, hostOK := strings.Cut(host, ".")
	return hostOK && hostLabel != "" && hostRest == wildRest
}

// restartWebAccessOrRollbackLocked hot-restarts a running sidecar onto the
// just-persisted config state; a no-op when the sidecar is not running (the
// persisted write was the whole effect). If the restart cannot serve, prev —
// the section state the sidecar was running with — is restored in memory,
// persisted, and the sidecar brought back on it. Every failure, including a
// failed rollback persist, is named in the returned error: the operator is
// never told the previous settings were restored while disk still holds the
// pin that failed to serve. Callers hold webAccessMu and have already
// persisted the new state into cfg; `what` names the change in errors.
func (a *App) restartWebAccessOrRollbackLocked(prev config.WebAccessConfig, cfg *config.Config, cfgPath, what string) error {
	if a.webAccess.Load() == nil {
		return nil
	}

	// Hot-restart onto the new state. The stop's error is deliberately not
	// fatal: the start below needs the listener gone, and a failed stop
	// surfaces through the start failure — the rollback path then restores
	// the previous state.
	sctx, cancel := context.WithTimeout(context.Background(), webAccessStopBudget)
	defer cancel()
	_ = a.stopWebAccessLocked(sctx)
	srv, err := a.startWebAccessLocked(a.log())
	if err == nil {
		logWebAccessReady(a.log(), srv)
		return nil
	}

	// The new state does not serve. Restore the previous one on disk and
	// bring the sidecar back up on it; every failure is named to the caller.
	cfg.WebAccess = prev
	saveErr := config.Save(cfg, cfgPath)
	if saveErr != nil {
		a.log().Error("web access: failed to persist the rolled-back settings", "err", saveErr)
	}
	restored, restartErr := a.startWebAccessLocked(a.log())
	if restartErr != nil {
		if saveErr != nil {
			return fmt.Errorf(
				"web access %s not applied: the sidecar failed to start with the new settings (%w) and could not be restored on the previous ones (%w); persisting the rolled-back settings also failed (%w) — disk still holds the settings that failed to serve, and the sidecar is down until the next launch",
				what, err, restartErr, saveErr)
		}
		return fmt.Errorf(
			"web access %s not applied: the sidecar failed to start with the new settings (%w) and could not be restored on the previous ones (%w) — it will retry on the next launch",
			what, err, restartErr)
	}
	logWebAccessReady(a.log(), restored)
	if saveErr != nil {
		return fmt.Errorf(
			"web access %s not applied: the sidecar failed to start with the new settings (%w); the sidecar is running again on the previous settings, but persisting the rollback failed (%w) — disk still holds the settings that failed to serve and will retry them on the next launch",
			what, err, saveErr)
	}
	return fmt.Errorf(
		"web access %s not applied: the sidecar failed to start with the new settings (%w); the previous settings were restored and the sidecar is running again",
		what, err)
}

// webAccessMountAllowed is the ADR-101 decision-13 availability conjunction —
// the master experimental gate AND the feature's own web_access switch — as
// ONE predicate for every mount decision: the startup mount, the live-follow
// hook and the section toggle. experimentalEnabled is passed explicitly
// because the live-follow hook carries the post-toggle gate value the
// in-memory config has only just converged to.
func webAccessMountAllowed(cfg *config.Config, experimentalEnabled bool) bool {
	return cfg != nil && experimentalEnabled && cfg.WebAccess.Enabled
}

// startWebAccessLocked constructs and starts the sidecar and publishes the
// handle + event tap. Callers must hold webAccessMu. Idempotent on a live
// server: it returns the running one so a startup mount and a toggle can
// never double-bind.
func (a *App) startWebAccessLocked(log *slog.Logger) (*webaccess.Server, error) {
	if cur := a.webAccess.Load(); cur != nil {
		return cur, nil
	}
	cfg, cfgPath := a.configRef()
	if cfg == nil || cfgPath == "" {
		return nil, errors.New("config not initialized")
	}
	// The persistent credential: an empty config token means "mint one, now"
	// — generated once and written back immediately (ADR-101), so the
	// credential is stable across restarts from the first start on.
	if cfg.WebAccess.Token == "" {
		tok, err := webaccess.GenerateToken()
		if err != nil {
			return nil, fmt.Errorf("web access: generating auth token: %w", err)
		}
		prev := cfg.WebAccess
		cfg.WebAccess.Token = tok
		if err := config.Save(cfg, cfgPath); err != nil {
			cfg.WebAccess = prev
			return nil, fmt.Errorf("web access: failed to persist the generated auth token: %w", err)
		}
		log.Info("web access: generated a persistent auth token (the config token was empty)")
	}
	srv, err := webaccess.New(webaccess.Options{
		Config:   cfg.WebAccess,
		Dispatch: a.frontendAPI(),
		// App-level methods the mirrored UI needs, behind the minimal
		// adapter (desktop/webaccess_extra.go) — never the App itself.
		ExtraDispatch: &webExtraSurface{app: a},
		Static:        a.webAccessStatic,
		CSP:           CSPMiddleware(),
		Logger:        log,
		// The browser-side twin of wireWailsEventListeners: an authenticated
		// tab resolves the whitelisted HITL response events through the same
		// App handlers the Wails runtime path uses (desktop/webaccess_inbound.go).
		WSInbound: a.webWSInboundDispatcher(log),
	})
	if err != nil {
		return nil, err
	}
	if err := srv.Start(context.Background()); err != nil {
		return nil, err
	}
	// Publish the handle before the tap: once events fan out, a concurrent
	// GetWebAccessInfo must already see Running.
	a.webAccess.Store(srv)
	a.setEventTap(srv.EventTap())
	return srv, nil
}

// stopWebAccessLocked detaches and stops the live sidecar. Callers must hold
// webAccessMu. The handle and tap are cleared BEFORE the stop so events stop
// flowing to a dying server and a concurrent toggle sees "not running"
// immediately; the stop itself is idempotent (Server.Stop is once-guarded).
// A nil handle is a no-op.
func (a *App) stopWebAccessLocked(ctx context.Context) error {
	srv := a.webAccess.Load()
	if srv == nil {
		return nil
	}
	a.webAccess.Store(nil)
	a.setEventTap(nil)
	return srv.Stop(ctx)
}

// logWebAccessReady records the sidecar's URL at Info. The auth token is
// deliberately NOT part of the record — it is app-secret material the
// Settings section reveals on demand; the log only names where a browser
// connects.
func logWebAccessReady(log *slog.Logger, srv *webaccess.Server) {
	addr, ok := srv.Addr()
	if !ok {
		addr = "(unresolved)"
	}
	log.Info("web access sidecar ready", "url", webAccessBrowserURL(addr, srv.TLSEnabled()),
		"token", "(hidden — see Settings → Web Access)")
}
