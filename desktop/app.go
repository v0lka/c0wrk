package desktop

import (
	"context"
	"database/sql"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/v0lka/c0wrk/backend"
	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/backend/logger"
	"github.com/v0lka/c0wrk/backend/session"
	"github.com/v0lka/c0wrk/core/savedialog"
	"github.com/v0lka/c0wrk/core/shutdown"
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

	// judgeWG tracks in-flight judge-evaluation goroutines so Shutdown can
	// wait for them before tearing down the backend application.
	judgeWG sync.WaitGroup

	// judgeService owns the judge evaluation lifecycle (in-flight claims plus
	// the transport-neutral evaluation call in backend/judge.go), bounding
	// judge spawning to one evaluation per confirmation — a repeat frame for
	// an in-flight evaluation is dropped instead of starting a second
	// full-budget LLM call that could only lose the first-responder race.
	// Entries live exactly as long as their goroutine; judgeWG remains the
	// shutdown barrier.
	judgeService backend.JudgeService

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

	// ── Web access sidecar (backend.WebAccessService, ADR-101) ───────────
	// Contract: specs/domains/web-access.md. The sidecar's whole lifecycle —
	// the live server handle, the transition lock, and every control
	// operation — lives in backend/frontend_api_webaccess.go as the
	// transport-neutral WebAccessService (hooks in, no Wails types out).
	// The App keeps the wire bindings (thin delegates below), the
	// desktop-owned adapters the hooks bind (webExtraSurface,
	// webWSInboundDispatcher, CSPMiddleware), and the WebAccessInfo wire
	// DTO. The hooks are configured once in NewApp, before wails.Run
	// publishes any RPC goroutine.
	webAccessService backend.WebAccessService

	// liveConfig and liveConfigPath are the desktop's handle on the live
	// config object and its on-disk path, captured in buildFrontendAPI.
	// liveConfig is the SAME pointer FrontendAPI.Init seeds — the shared
	// live config the backend mutates in place under its configMu. Desktop
	// never touches backend-owned sections through it; it exists solely so
	// the desktop-owned web_access section can be flipped and persisted
	// without a backend edit. The persist marshals and writes the WHOLE
	// config file while holding only the service's transition lock (desktop cannot take
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
	batcherPtr  atomic.Pointer[backend.EventBatcher]

	// reloadAppFn, when non-nil, replaces wailsRuntime.WindowReloadApp in
	// (*App).reloadFrontend — the test seam for the deferred wake reload.
	// Production keeps it nil.
	reloadAppFn func(ctx context.Context)

	// toolsBinPath is the managed tools bin directory (e.g. ~/.c0wrk/tools/bin/),
	// set during Phase 2 and prepended to PATH so exec.CommandContext calls
	// resolve managed binaries (rg, uv, markitdown).
	toolsBinPath string

	// toolSplashTotal is the splash denominator emitted with every
	// tool_manager:progress event: Wails drops events emitted before the
	// webview subscribes, so tool_manager:start can be lost and each progress
	// event must self-describe the total. Written via the ToolPhaseDeps
	// SetToolsTotal hook by backend reconcileToolsWithSplash (NeedsInstall
	// list length), read by the desktop progress adapter.
	toolSplashTotal atomic.Int64

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
	// lookup in the close guard. Lets tests exercise the guard without a
	// live backend application. Production wiring keeps it nil.
	activeSessionsFn func() []session.ActiveSessionInfo

	// quitFn, when non-nil, replaces wailsRuntime.Quit in ConfirmExit. Lets
	// tests observe the confirmed quit without a live Wails runtime.
	// Production wiring keeps it nil.
	quitFn func(ctx context.Context)

	// shutdownHardDeadline is the hard budget for the WHOLE Shutdown
	// teardown, set from config (shutdown.hardDeadline) during Startup; zero
	// falls back to the core/shutdown default hard deadline. Enforced by the
	// shutdown watchdog (specs/domains/crash-logging.md): on expiry the
	// process logs at Error and exits. Guarded by stateMu — see above.
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
	windowGeomCache atomic.Pointer[backend.WindowBounds]

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
	a := &App{
		FrontendAPI: &backend.FrontendAPI{},
	}
	// Bind the web-access service's transport hooks to this App's desktop
	// adapters (webExtraSurface / webWSInboundDispatcher / CSPMiddleware)
	// and accessors — the FrontendAPIConfig.EmitEvent seam shape. The
	// accessors are hook closures reading the stateMu-guarded fields, so
	// later setLogger/setConfigRef writes are observed by every call.
	a.webAccessService.Configure(backend.WebAccessHooks{
		Config:        a.configRef,
		Logger:        a.log,
		API:           a.frontendAPI,
		ExtraDispatch: func() any { return &webExtraSurface{app: a} },
		WSInbound:     a.webWSInboundDispatcher,
		CSP:           func() func(http.Handler) http.Handler { return CSPMiddleware() },
		SetEventTap:   a.setEventTap,
	})
	return a
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
	a.wireGpuWatchdogQuit(w)
	w.Start()
}

// wireGpuWatchdogQuit arms the watchdog's post-relaunch quit to
// a.gpuFallbackQuit when a live Wails context exists. Split from
// startWebviewGpuWatchdog so the close-guard bypass test can pin the wiring
// without spawning the bounded scanner goroutine (the started flag is
// backend-internal since the P-13 move).
func (a *App) wireGpuWatchdogQuit(w *webviewGpuWatchdog) {
	if w.QuitFn() == nil && a.wailsCtx() != nil {
		w.SetQuitFn(a.gpuFallbackQuit)
	}
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
	// returns an error without showing any dialog; backend.LoadDialogState
	// drops stale/non-existent paths, so adopting its value is always safe.
	if last := backend.LoadDialogState(a.agentDir()); last.LastDirectory != "" {
		options.DefaultDirectory = last.LastDirectory
	}

	dir, err := wailsRuntime.OpenDirectoryDialog(a.wailsCtx(), options)
	if err != nil {
		return "", err
	}
	// On cancel OpenDirectoryDialog returns ("", nil) — keep the previous
	// memory instead of overwriting it with an empty path.
	backend.RememberDialogDirectory(a.agentDir(), dir, a.log())
	return dir, nil
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
				Pattern:     savedialog.AttachmentFilterPattern(),
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
				Pattern:     savedialog.AttachmentFilterPattern(),
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
// teardown: the value Startup recorded from shutdown.hardDeadline, or the
// core/shutdown default hard deadline when unset (0) or Startup never got
// that far.
func (a *App) shutdownDeadline() time.Duration {
	a.stateMu.RLock()
	defer a.stateMu.RUnlock()
	if a.shutdownHardDeadline > 0 {
		return a.shutdownHardDeadline
	}
	return shutdown.DefaultHardDeadline()
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
	a.webAccessService.SetStaticFS(static)
}

// WebAccessInfo is the GetWebAccessInfo payload: the Settings section's whole
// view of the sidecar. Every field is specified in
// specs/domains/web-access.md (the WebAccessInfo table). The struct is the
// wire DTO of this Wails binding — its package must stay desktop so the
// generated frontend/wailsjs models keep their namespace; the values are
// produced by backend.WebAccessService.Snapshot (field-identical
// WebAccessSnapshot, converted directly).
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

// GetWebAccessInfo reports the sidecar state for Settings → Web Access
// (desktop-only by construction: an App method, so the web reflection surface
// cannot serve it back to a browser tab). The snapshot — the locked section
// read, the classification and the URL rendering — is the service's
// Snapshot; the binding layer only converts it to the wire DTO.
func (a *App) GetWebAccessInfo() WebAccessInfo {
	return WebAccessInfo(a.webAccessService.Snapshot())
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

// The Set* methods below are thin delegates onto
// backend.WebAccessService.Set* (backend/frontend_api_webaccess.go): the
// transition lock, the config mutation + persist, and the hot-restart /
// rollback discipline live there; each adapter adds exactly the
// warning-channel refresh the transition requires (ADR-102 — see
// refreshWebAccessLoadWarnings). Signatures are the Wails binding surface
// and must not drift.
func (a *App) SetWebAccessEnabled(on bool) error {
	err := a.webAccessService.SetEnabled(on)
	a.refreshWebAccessLoadWarnings()
	return err
}

func (a *App) SetWebAccessConnection(bind string, port int) error {
	err := a.webAccessService.SetConnection(bind, port)
	a.refreshWebAccessLoadWarnings()
	return err
}

func (a *App) RegenerateWebAccessToken() error {
	return a.webAccessService.RegenerateToken()
}

func (a *App) SetWebAccessToken(token string) error {
	return a.webAccessService.SetToken(token)
}

func (a *App) SetWebAccessTLS(enabled bool, cert, key string) error {
	err := a.webAccessService.SetTLS(enabled, cert, key)
	a.refreshWebAccessLoadWarnings()
	return err
}

func (a *App) SetWebAccessAllowInsecureLAN(allow bool) error {
	err := a.webAccessService.SetAllowInsecureLAN(allow)
	a.refreshWebAccessLoadWarnings()
	return err
}

func (a *App) GenerateWebAccessCertificate() error {
	err := a.webAccessService.GenerateCertificate()
	// Regeneration can move the section's load-warning contribution
	// (CS #67): a fresh valid pair heals a warned-unusable one. After the
	// unlock, mirroring the other control RPCs.
	a.refreshWebAccessLoadWarnings()
	return err
}
