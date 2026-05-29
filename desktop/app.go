package desktop

import (
	"context"
	"database/sql"
	"log/slog"
	"sync"

	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/v0lka/c0wrk/backend"
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

	// Wails event-listener infrastructure (used only in startup.go listeners)
	pendingConfirmations sync.Map
	pendingAskUser       sync.Map
	pendingStepLimit     sync.Map
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
