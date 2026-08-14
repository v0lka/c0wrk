package desktop

import (
	"encoding/json"
	"log/slog"
	"os"

	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/v0lka/c0wrk/backend/config"
)

// WindowBounds is the persisted OS-level window geometry. It is written on
// resize (debounced, from the frontend) and on shutdown, and read on startup
// to seed wails.Run's Width/Height and re-apply a maximized window.
type WindowBounds struct {
	Width     int  `json:"width"`
	Height    int  `json:"height"`
	Maximized bool `json:"maximized"`
}

// Default and minimum window dimensions. These mirror the initial size and
// minimum constraints declared in main.go's wails.Run options; the constants
// live here so LoadWindowBounds can validate/clamp persisted values without a
// dependency on the options struct.
const (
	defaultWindowWidth  = 1400
	defaultWindowHeight = 900
	minWindowWidth      = 1024
	minWindowHeight     = 600
)

// LoadWindowBounds reads the persisted window geometry from agentDir. It is
// defensive: a missing file, a malformed/unreadable file, or out-of-range
// values all fall back to the built-in defaults so a corrupt state file can
// never prevent the app from starting. main.go calls this before wails.Run to
// seed the initial window size.
func LoadWindowBounds(agentDir string) WindowBounds {
	b := defaultWindowBounds()

	data, err := os.ReadFile(config.WindowStatePath(agentDir))
	if err != nil {
		// Missing file on first run is expected — return defaults silently.
		return b
	}

	var stored WindowBounds
	if err := json.Unmarshal(data, &stored); err != nil {
		return b
	}

	// Only adopt persisted dimensions that satisfy the minimum window size;
	// otherwise keep the default. This guards against nonsense values (0,
	// negatives) that would shrink the window below its usable minimum.
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

// PersistWindowBounds is the frontend-callable RPC (bound via wails Bind). The
// frontend calls it on a debounced window resize so the geometry survives even
// an ungraceful exit (force-quit/crash) — the last persisted state is always
// recent. It reads the live OS window geometry from the Wails runtime.
func (a *App) PersistWindowBounds() {
	if a.ctx == nil {
		return
	}
	a.saveWindowBounds(a.log())
}

// restoreMaximizedWindow re-applies the maximized state after the Wails window
// is created (called from Startup). Window size is handled by wails.Run
// options in main.go; only the maximize flag needs runtime re-application.
func (a *App) restoreMaximizedWindow() {
	if a.ctx == nil {
		return
	}
	if LoadWindowBounds(a.agentDir()).Maximized {
		wailsRuntime.WindowMaximise(a.ctx)
	}
}

// saveWindowBounds reads the current OS window geometry via the Wails runtime
// and writes it to window_state.json. When the window is maximized it
// preserves the last non-maximized Width/Height (loaded from the existing
// file) so the restore rectangle is not overwritten with the fullscreen size —
// only the Maximized flag is updated. Writes are atomic (temp + rename).
func (a *App) saveWindowBounds(log *slog.Logger) {
	if a.ctx == nil {
		return
	}

	agentDir := a.agentDir()
	width, height := wailsRuntime.WindowGetSize(a.ctx)
	maximized := wailsRuntime.WindowIsMaximised(a.ctx)

	next := WindowBounds{Width: width, Height: height, Maximized: maximized}

	if maximized {
		// Preserve the last non-maximized size so un-maximizing restores the
		// user's chosen rectangle rather than the fullscreen dimensions.
		prev := LoadWindowBounds(agentDir)
		next.Width = prev.Width
		next.Height = prev.Height
	}

	if err := writeWindowBounds(agentDir, next); err != nil {
		log.Warn("failed to persist window state", "error", err)
	}
}

// agentDir returns the ~/.c0wrk agent directory. Delegates to config.AgentDir,
// the single source of truth for this computation.
func (a *App) agentDir() string {
	return config.AgentDir()
}

// writeWindowBounds serializes bounds to window_state.json atomically.
func writeWindowBounds(agentDir string, b WindowBounds) error {
	data, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return err
	}
	path := config.WindowStatePath(agentDir)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
