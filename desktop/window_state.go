package desktop

import (
	"context"
	"encoding/json"
	"log/slog"

	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/v0lka/c0wrk/backend/config"

	"github.com/v0lka/sp4rk/safeio"
)

// WindowBounds is the persisted OS-level window geometry, written on debounced
// frontend resizes and at shutdown, read at startup to seed wails.Run's
// Width/Height and re-apply a maximized window.
// See specs/domains/frontend/README.md (window_state.json).
type WindowBounds struct {
	Width     int  `json:"width"`
	Height    int  `json:"height"`
	Maximized bool `json:"maximized"`
}

// Default and minimum window dimensions, mirroring the initial size and
// minimum constraints of main.go's wails.Run options; kept here so
// LoadWindowBounds can validate persisted values without depending on the
// options struct.
const (
	defaultWindowWidth  = 1400
	defaultWindowHeight = 900
	minWindowWidth      = 1024
	minWindowHeight     = 600
)

// LoadWindowBounds reads the persisted window geometry, falling back to the
// built-in defaults for a missing, malformed, or below-minimum file — a
// corrupt state file must never prevent startup. main.go calls this before
// wails.Run to seed the initial window size.
func LoadWindowBounds(agentDir string) WindowBounds {
	b := defaultWindowBounds()

	data, err := safeio.ReadFile(config.WindowStatePath(agentDir))
	if err != nil {
		return b
	}

	var stored WindowBounds
	if err := json.Unmarshal(data, &stored); err != nil {
		return b
	}

	if stored.Width >= minWindowWidth && stored.Height >= minWindowHeight {
		b.Width = stored.Width
		b.Height = stored.Height
	}
	b.Maximized = stored.Maximized
	return b
}

func defaultWindowBounds() WindowBounds {
	return WindowBounds{
		Width:     defaultWindowWidth,
		Height:    defaultWindowHeight,
		Maximized: false,
	}
}

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
	if LoadWindowBounds(a.agentDir()).Maximized {
		wailsRuntime.WindowMaximise(a.wailsCtx())
	}
}

// saveWindowBounds writes the live OS window geometry; the
// maximized-preservation rule lives in writeResolvedWindowBounds.
func (a *App) saveWindowBounds(log *slog.Logger) {
	ctx := a.wailsCtx()
	if ctx == nil {
		return
	}
	a.writeResolvedWindowBounds(a.readLiveWindowGeometry(ctx), log)
}

// readLiveWindowGeometry reads the current OS window geometry through the
// per-call seams (tests) or the Wails runtime (production).
func (a *App) readLiveWindowGeometry(ctx context.Context) WindowBounds {
	var b WindowBounds
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
		a.writeResolvedWindowBounds(*snap, log)
		return
	}
	a.saveWindowBounds(log)
}

// writeResolvedWindowBounds applies the maximized-preservation rule and writes
// the state file: when maximized, the last non-maximized Width/Height is
// preserved (only the Maximized flag updates), so un-maximizing restores the
// user's chosen rectangle rather than the fullscreen dimensions.
func (a *App) writeResolvedWindowBounds(live WindowBounds, log *slog.Logger) {
	agentDir := a.agentDir()
	next := live
	if live.Maximized {
		prev := LoadWindowBounds(agentDir)
		next.Width = prev.Width
		next.Height = prev.Height
	}
	if err := writeWindowBounds(agentDir, next); err != nil {
		log.Warn("failed to persist window state", "error", err)
	}
}

func (a *App) agentDir() string {
	return config.AgentDir()
}

// writeWindowBounds serializes bounds to window_state.json atomically via
// safeio.WriteFileAtomic: the staging file is a random-suffix sibling (no
// fixed path+".tmp" name to pre-plant a blocking FIFO on), and the rename
// REPLACES the directory entry — a planted symlink at window_state.json is
// replaced, never written through.
func writeWindowBounds(agentDir string, b WindowBounds) error {
	data, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return err
	}
	return safeio.WriteFileAtomic(config.WindowStatePath(agentDir), data, 0o600)
}
