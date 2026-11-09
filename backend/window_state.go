package backend

import (
	"encoding/json"
	"log/slog"

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

// writeResolvedWindowBounds applies the maximized-preservation rule and writes
// the state file: when maximized, the last non-maximized Width/Height is
// preserved (only the Maximized flag updates), so un-maximizing restores the
// user's chosen rectangle rather than the fullscreen dimensions.
func writeResolvedWindowBounds(agentDir string, live WindowBounds, log *slog.Logger) {
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

// WriteResolvedWindowBounds forwards to writeResolvedWindowBounds for the
// desktop window-geometry adapters (the debounced resize persist and the
// shutdown persist, which read the live geometry through Wails).
func WriteResolvedWindowBounds(agentDir string, live WindowBounds, log *slog.Logger) {
	writeResolvedWindowBounds(agentDir, live, log)
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
