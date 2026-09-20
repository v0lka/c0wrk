package desktop

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"

	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/v0lka/c0wrk/backend"
	"github.com/v0lka/c0wrk/backend/logger"
	"github.com/v0lka/c0wrk/backend/session"
	"github.com/v0lka/c0wrk/core/markitdown"
	"github.com/v0lka/c0wrk/core/vectorindex"
)

// App holds the Wails application state and exposes methods to the frontend.
// All frontend API methods live on the embedded *backend.FrontendAPI; promoted
// methods are visible to the Wails binding generator.
// App itself retains only lifecycle management (Startup/Shutdown), the native
// PickDirectory dialog, and Wails event-listener infrastructure.
type App struct {
	ctx context.Context
	*backend.FrontendAPI

	// app is the central ViewModel (owns builder, manager, persister).
	// Kept here for Startup/Shutdown orchestration that references it directly.
	app *backend.Application

	// logger used during Startup before FrontendAPI is constructed.
	logger *slog.Logger

	// wailsLogger bridges Wails internal messages (including Fatal/Error)
	// to a persistent wails.log file. Once the session logger is ready,
	// messages are also duplicated to the session log.
	wailsLogger *wailsLogAdapter

	db *sql.DB // shared SQLite connection; lifecycle: opened in Startup, closed in Shutdown

	// sessionLogger is stored so Shutdown can close it on early Startup exits
	// (e.g. when tool installation fails and Startup returns before
	// FrontendAPI is wired).
	sessionLogger *logger.SessionLogger

	// Wails event-listener infrastructure (used only in startup.go listeners)
	pendingConfirmations sync.Map
	pendingAskUser       sync.Map
	pendingStepLimit     sync.Map
	pendingPlanApprovals sync.Map
	pendingGoalProposals sync.Map

	// judgeWG tracks in-flight runJudgeEvaluation goroutines so Shutdown can
	// wait for them before tearing down the backend application.
	judgeWG sync.WaitGroup

	// vectorMgrPtr mirrors the atomic pointer passed to buildVectorCallbacks
	// so Shutdown can check whether a background-init vector manager was set
	// after Cleanup already sampled FrontendAPI.vectorManager as nil.
	vectorMgrPtr *atomic.Pointer[vectorindex.Manager]

	// wailsEmit, when non-nil, is used in place of wailsRuntime.EventsEmit. It
	// lets tests inject a fake event sink so phase helpers can be exercised
	// without a live Wails runtime (W-19/W-23). Production wiring keeps it nil.
	wailsEmit func(eventName string, optionalData ...any)

	// batcherPtr holds the lazily-created event batcher that sits between the
	// session-event producers and the raw transport (a.emit). It coalesces
	// transient streaming events and delivers content events as one
	// c0wrk:events:batch envelope per ~16ms flush, keeping evaluateJavaScript
	// calls off the AppKit main thread's critical path. batcherOnce guards the
	// one-time construction. See event_batcher.go.
	batcherOnce sync.Once
	batcherPtr  atomic.Pointer[EventBatcher]

	// reloadAppFn, when non-nil, is used in place of wailsRuntime.WindowReloadApp
	// by (*App).reloadFrontend. Lets tests observe the deferred wake reload
	// without a live Wails runtime. Production wiring keeps it nil.
	reloadAppFn func(ctx context.Context)

	// toolsBinPath is the managed tools bin directory (e.g. ~/.c0wrk/tools/bin/),
	// set during Phase 2 and prepended to PATH so exec.CommandContext calls
	// resolve managed binaries (rg, uv, markitdown).
	toolsBinPath string

	// exitConfirmed bypasses the close guard (ShouldPreventClose) once the
	// user confirmed quitting despite active sessions. Wails routes every
	// quit path — including the runtime.Quit issued by ConfirmExit itself —
	// through OnBeforeClose, so the confirmed quit must not be intercepted
	// a second time.
	//
	// Invariant: the flag is armed only immediately before an imminent quit
	// (ConfirmExit stores it and calls wailsRuntime.Quit in the same
	// breath), and the platform quit paths that follow cannot be cancelled
	// by the user — so an armed flag never outlives the process it was armed
	// for, and a later guard bypass with newly active sessions is
	// impossible. Any change that makes quit cancellable after arming must
	// reset this flag on cancellation.
	exitConfirmed atomic.Bool

	// updateQuitAt records when the updater-driven quit (ApplyUpdate →
	// quitApp) was issued, so the close guard can tell the frontend that an
	// intercepted quit belongs to a pending self-update (payload flag
	// update_pending) and the confirmation modal can present restart
	// context. It is a timestamp rather than a sticky flag for honesty
	// about cancellation: the staged updater gives up once the parent
	// misses StagedUpdaterShutdownWait, so after that window a quit no
	// longer completes the update and must not be presented as one. The
	// zero value (never armed) disables the context. Accessed atomically
	// because quitApp runs on a Wails RPC goroutine while OnBeforeClose
	// runs on the main thread.
	updateQuitAt atomic.Value // time.Time

	// activeSessionsFn, when non-nil, replaces the backend active-session
	// lookup in the close guard. Lets tests exercise the guard without a
	// live backend application. Production wiring keeps it nil.
	activeSessionsFn func() []session.ActiveSessionInfo

	// quitFn, when non-nil, replaces wailsRuntime.Quit in ConfirmExit. Lets
	// tests observe the confirmed quit without a live Wails runtime.
	// Production wiring keeps it nil.
	quitFn func(ctx context.Context)

	// windowShowFn, when non-nil, replaces wailsRuntime.WindowShow in
	// showWindow — and therefore on every path that reveals the window (the
	// startup phases, OnDomReady, and the close guard). Lets tests observe
	// those paths without a live Wails runtime (wailsRuntime panics on a
	// foreign context). Production wiring keeps it nil.
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
	// x11ActivateOwnWindow over a private X connection). Same purpose as the
	// per-call seams above — lets tests observe/fake the activation matrix
	// without a live X server. Production wiring keeps it nil.
	x11PagerActivateFn func() bool

	// ── System notifications (notifications.go) ────────────────────────────
	//
	// Test seams for the Wails notification runtime calls, following the
	// wailsEmit / windowShowFn / quitFn precedent: each replaces exactly one
	// wailsRuntime package function so the notification paths can be exercised
	// without a live Wails runtime (the real calls fatal on a context no
	// runtime owns). Production wiring keeps every one of them nil.

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
	// notificationsSendViaWailsFn replaces the sendNotificationViaWails
	// fallback (the Wails transport behind the wailsRuntime.SendNotification
	// call). Lets tests observe the Linux dial-failure fallback path — the
	// real Wails call fatals on a context no live runtime owns. Production
	// wiring keeps it nil.
	notificationsSendViaWailsFn func(ctx context.Context, options wailsRuntime.NotificationOptions) error
}

// NewApp creates a new App instance.
// FrontendAPI is initialized as a non-nil zero-value so that early frontend
// RPC calls (before Startup finishes) hit the per-method nil guards and
// return proper errors instead of panicking on a nil pointer dereference.
// Startup replaces the pointer with a fully-wired FrontendAPI.
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

// PickDirectory opens a native directory picker dialog. The dialog reopens at
// the directory the user last picked (persisted in dialog_state.json and
// validated to still exist); a successful non-cancelled pick updates that
// memory. This must remain on App (not FrontendAPI) because it requires the
// Wails context.
func (a *App) PickDirectory() (string, error) {
	if a.ctx == nil {
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

	dir, err := wailsRuntime.OpenDirectoryDialog(a.ctx, options)
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

// PickAttachmentFiles opens a native multi-select file picker restricted to the
// document formats markitdown can convert. This must remain on App (not
// FrontendAPI) because it requires the Wails context, exactly like PickDirectory.
//
// On cancel, OpenMultipleFilesDialog returns an empty slice and a nil error;
// that ([]string{}, nil) is returned as-is.
func (a *App) PickAttachmentFiles() ([]string, error) {
	if a.ctx == nil {
		return nil, errors.New("PickAttachmentFiles: application context is not initialized")
	}

	// Deliberately no "All files (*.*)" filter here: on macOS, Wails maps
	// each pattern to a UTType via UTType(filenameExtension:) and merges all
	// resolved types into a single NSOpenPanel.allowedContentTypes array
	// (Mac dialogs only support one combined pattern set — see Wails' dialog
	// docs). The "*" extension from "*.*" resolves to an unusable *dynamic*
	// UTType (e.g. "dyn.ah62d4rv4ge8wy") rather than a wildcard/"anything"
	// type. A dynamic UTType in that array corrupts the panel's whole
	// content-type filter, graying out every entry — including files that
	// otherwise match a perfectly valid type like "public.png" (see e.g.
	// https://github.com/r0x0r/pywebview/issues/1780 for the same root cause
	// in another Cocoa-backed webview shell). This is why images could not
	// be selected at all in the attach dialog. The "Supported documents" and
	// "Images" filters already cover every extension the backend accepts.
	return wailsRuntime.OpenMultipleFilesDialog(a.ctx, wailsRuntime.OpenDialogOptions{
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

// PickStudyDocument opens a native single-select file picker restricted to the
// document formats the app can read (the markitdown-supported extensions, PDF
// first among equals). It backs the Papers panel's "pick a local document"
// gesture next to the Study field, so a study can be launched straight from a
// file on disk. This must remain on App (not FrontendAPI) because it requires
// the Wails context, exactly like PickDirectory and PickAttachmentFiles.
//
// On cancel, OpenFileDialog returns ("", nil) — returned as-is so the frontend
// can distinguish "user cancelled" (empty path, no error) from a failure.
func (a *App) PickStudyDocument() (string, error) {
	if a.ctx == nil {
		return "", errors.New("PickStudyDocument: application context is not initialized")
	}

	// Same filter rationale as PickAttachmentFiles: no "All files (*.*)" entry
	// (a "*" pattern resolves to a dynamic UTType on macOS and corrupts the
	// panel's content-type filter), and the supported-documents set already
	// covers every format the study flow can convert and read.
	return wailsRuntime.OpenFileDialog(a.ctx, wailsRuntime.OpenDialogOptions{
		Title: "Study a local document",
		Filters: []wailsRuntime.FileFilter{
			{
				DisplayName: "Supported documents",
				Pattern:     attachmentFilterPattern(),
			},
		},
	})
}

// PickAndImportThemes opens a native multi-select file picker restricted to
// CSS files and imports every chosen file as a user theme in one action. This
// must remain on App (not FrontendAPI) because it requires the Wails context,
// exactly like PickDirectory.
//
// On cancel, OpenMultipleFilesDialog returns an empty slice and a nil error;
// the method then returns (nil, nil) — nothing is imported and the frontend
// maps the null result to "user cancelled". The chosen paths are delegated to
// backend.ImportThemesFromPaths (the package-level bridge over the
// unexported FrontendAPI importer), which validates and sanitizes each file
// independently — one invalid file never blocks the rest of the batch, and
// per-file outcomes (including failures) come back in the result list so the
// frontend can surface them. This picker is the ONLY import entry point: the
// underlying import functions are not FrontendAPI methods precisely so the
// binding generator never publishes a path-taking RPC to the renderer.
func (a *App) PickAndImportThemes() ([]backend.ThemeImportResult, error) {
	if a.ctx == nil {
		return nil, errors.New("PickAndImportThemes: application context is not initialized")
	}

	paths, err := wailsRuntime.OpenMultipleFilesDialog(a.ctx, wailsRuntime.OpenDialogOptions{
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

	// Delegate to the package-level batch importer: the import entry points
	// are deliberately NOT FrontendAPI methods (exported methods are
	// auto-bound to the renderer; a path-taking RPC must not be callable
	// from compromised renderer JS). The picker above is the sole path
	// source.
	return backend.ImportThemesFromPaths(a.FrontendAPI, paths), nil
}

// log returns the instance logger, falling back to slog.Default() when nil.
func (a *App) log() *slog.Logger {
	if a.logger != nil {
		return a.logger
	}
	return slog.Default()
}

// emit dispatches a Wails event. Tests can inject a fake bus by setting
// a.wailsEmit; production code uses wailsRuntime.EventsEmit. Callers must not
// invoke this before Startup binds a.ctx (or before a.wailsEmit is set in tests).
func (a *App) emit(eventName string, optionalData ...any) {
	if a.wailsEmit != nil {
		a.wailsEmit(eventName, optionalData...)
		return
	}
	if a.ctx == nil {
		a.log().Warn("emit called with nil ctx, event dropped", "event", eventName)
		return
	}
	wailsRuntime.EventsEmit(a.ctx, eventName, optionalData...)
}

// showWindow reveals AND raises the main window. Tests can inject a fake by
// setting a.windowShowFn; production code uses wailsRuntime.
//
// Every reveal path funnels through here — the startup phases, OnDomReady,
// the exit guard, and the notification-click callback — and the raise applies
// to ALL of them by design: reveal without raise would leave a buried window
// "shown". The window is created visible (main.go sets no StartHidden), so in
// a normal start these calls have nothing left to do; they exist so a window
// that is hidden, minimized, or buried under other windows still comes back.
//
// Activation semantics per platform (verified against the Wails v2.15
// frontends — internal/frontend/desktop/{linux,darwin,windows}):
//
//		Linux     WindowShow is gtk_widget_show — a bare map call that is a NO-OP
//		          for an already-mapped (but covered or minimized) window: no
//		          raise, no focus, no restore. WindowUnminimise is the real
//		          activation primitive there: gtk_window_present, which raises,
//		          deiconifies and focuses (EWMH _NET_ACTIVE_WINDOW). So on Linux
//	         the present call ALONE is the full reveal+raise, and the plain
//	         Show is skipped (calling both just double-queues main-thread
//	         work for the same effect).
//
//		darwin    WindowShow is makeKeyAndOrderFront + activateIgnoringOtherApps
//		          — already the full activation; WindowUnminimise (deminiaturize)
//		          adds nothing the Show doesn't do, so it is skipped.
//
//		Windows   ShowWindow maps + activates, but WindowUnminimise maps to the
//		          WPF Form.Restore(), which UNMAXIMIZES a maximized window — so it
//		          runs only when the window is actually minimized. The restore
//		          path is the only way to bring the window back from the taskbar
//		          without a focus-steal fight with the shell.
//
// See specs/domains/frontend/system-notifications.md (§ Click → window
// activation) for the full matrix and the Wails source references.
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

// showWindowPlatform selects the activation branch of showWindow. A package
// var (not a build tag) so tests on ANY platform can exercise every branch —
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
// the app's own window (window_activation_linux.go) behind the
// x11PagerActivateFn seam. Returns true when the pager activation ran (the
// caller then skips the Wails fallback); false → fall back to the Wails
// present() path. The seam is nil in production, where the cgo
// implementation runs; tests fake it to exercise both outcomes.
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

// resolvePendingMessage delegates to FrontendAPI.ResolvePendingMessage to mark
// a persisted HITL message as resolved in the DB.
func (a *App) resolvePendingMessage(sessionID, role, matchField, matchValue string, extra map[string]any) error {
	if a.FrontendAPI == nil {
		return nil
	}
	return a.ResolvePendingMessage(sessionID, role, matchField, matchValue, extra)
}
