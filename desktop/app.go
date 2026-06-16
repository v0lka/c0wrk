package desktop

import (
	"context"
	"database/sql"
	"log/slog"
	"sync"
	"sync/atomic"

	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/v0lka/c0wrk/backend"
	"github.com/v0lka/c0wrk/backend/logger"
	"github.com/v0lka/c0wrk/sdk/vectorindex"
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

	db *sql.DB // shared SQLite connection; lifecycle: opened in Startup, closed in Shutdown

	// sessionLogger is stored so Shutdown can close it on early Startup exits
	// (e.g. when verifyExternalDependencies fails and Startup returns before
	// FrontendAPI is wired).
	sessionLogger *logger.SessionLogger

	// Wails event-listener infrastructure (used only in startup.go listeners)
	pendingConfirmations sync.Map
	pendingAskUser       sync.Map
	pendingStepLimit     sync.Map

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

// PickDirectory opens a native directory picker dialog.
// This must remain on App (not FrontendAPI) because it requires the Wails context.
func (a *App) PickDirectory() (string, error) {
	return wailsRuntime.OpenDirectoryDialog(a.ctx, wailsRuntime.OpenDialogOptions{
		Title: "Select Workspace Directory",
	})
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
		slog.Warn("emit called with nil ctx, event dropped", "event", eventName)
		return
	}
	wailsRuntime.EventsEmit(a.ctx, eventName, optionalData...)
}
