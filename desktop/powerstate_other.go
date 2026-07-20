//go:build !darwin

package desktop

import "log/slog"

// reloadWebviewNative is the non-darwin stub of the wake-path native reload.
// Returns false so (*App).reloadFrontend falls back to the JS-based
// wailsRuntime.WindowReloadApp. On non-darwin platforms there is no
// WKWebView and no post-sleep blank-webview bug; WindowReloadApp is correct
// there.
func reloadWebviewNative() bool { return false }

// registerPowerWakeObserver is a no-op on non-darwin platforms: power-state
// wake detection via NSWorkspace notifications is macOS-specific, and the
// WKWebView-blank-after-sleep bug it mitigates only affects macOS. The
// signature matches the darwin implementation so Startup wiring is uniform
// across platforms without per-OS conditionals.
func registerPowerWakeObserver(_ *slog.Logger, _ func()) {}
