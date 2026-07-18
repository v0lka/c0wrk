//go:build !darwin

package desktop

import "log/slog"

// registerPowerWakeObserver is a no-op on non-darwin platforms: power-state
// wake detection via NSWorkspace notifications is macOS-specific, and the
// WKWebView-blank-after-sleep bug it mitigates only affects macOS. The
// signature matches the darwin implementation so Startup wiring is uniform
// across platforms without per-OS conditionals.
func registerPowerWakeObserver(_ *slog.Logger, _ func()) {}
