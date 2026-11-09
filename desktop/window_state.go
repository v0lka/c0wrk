package desktop

import (
	"context"
	"log/slog"

	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/v0lka/c0wrk/backend"

	"github.com/v0lka/c0wrk/backend/config"
)

// PersistWindowBounds is the frontend-callable RPC: the frontend calls it on a
// debounced window resize so the geometry survives even an ungraceful exit
// (force-quit/crash) — the last persisted state is always recent.
// See specs/contracts/desktop-frontend.md (PersistWindowBounds).
func (a *App) PersistWindowBounds() {
	if a.wailsCtx() == nil {
		return
	}
	a.saveWindowBounds(a.log())
}

// restoreMaximizedWindow re-applies the maximized state after the Wails window
// is created (called from Startup); window size itself rides the wails.Run
// options, so only the maximize flag needs runtime re-application.
func (a *App) restoreMaximizedWindow() {
	if a.wailsCtx() == nil {
		return
	}
	if backend.LoadWindowBounds(a.agentDir()).Maximized {
		wailsRuntime.WindowMaximise(a.wailsCtx())
	}
}

// saveWindowBounds writes the live OS window geometry; the
// maximized-preservation rule lives in backend's writeResolvedWindowBounds.
func (a *App) saveWindowBounds(log *slog.Logger) {
	ctx := a.wailsCtx()
	if ctx == nil {
		return
	}
	backend.WriteResolvedWindowBounds(a.agentDir(), a.readLiveWindowGeometry(ctx), log)
}

// readLiveWindowGeometry reads the current OS window geometry through the
// per-call seams (tests) or the Wails runtime (production).
func (a *App) readLiveWindowGeometry(ctx context.Context) backend.WindowBounds {
	var b backend.WindowBounds
	if a.windowGetSizeFn != nil {
		b.Width, b.Height = a.windowGetSizeFn(ctx)
	} else {
		b.Width, b.Height = wailsRuntime.WindowGetSize(ctx)
	}
	if a.windowIsMaximisedFn != nil {
		b.Maximized = a.windowIsMaximisedFn(ctx)
	} else {
		b.Maximized = wailsRuntime.WindowIsMaximised(ctx)
	}
	return b
}

// captureWindowGeometry snapshots the CURRENT window geometry for the shutdown
// persist, from ShouldPreventClose — the one quit-path hook that still runs
// while the window is alive. On Linux Wails destroys the main window BEFORE
// the OnShutdown hook runs (wails v2 production app loop), and reading a
// destroyed GTK window returns the CREATION default — the historical
// read-at-shutdown clobbered the correct resize-debounced value.
func (a *App) captureWindowGeometry() {
	ctx := a.wailsCtx()
	if ctx == nil {
		return
	}
	b := a.readLiveWindowGeometry(ctx)
	a.windowGeomCache.Store(&b)
}

// persistWindowBoundsAtShutdown persists the final geometry on quit,
// preferring the alive-window snapshot and falling back to a live read only
// when no snapshot exists (a quit path that never ran the close guard).
func (a *App) persistWindowBoundsAtShutdown(log *slog.Logger) {
	if snap := a.windowGeomCache.Load(); snap != nil {
		backend.WriteResolvedWindowBounds(a.agentDir(), *snap, log)
		return
	}
	a.saveWindowBounds(log)
}

func (a *App) agentDir() string {
	return config.AgentDir()
}
