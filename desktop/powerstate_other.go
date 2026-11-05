//go:build !darwin

package desktop

import "log/slog"

// reloadWebviewNative is the non-darwin stub: reports false so reloadFrontend
// falls back to WindowReloadApp (specs/decisions/018-macos-webview-recovery.md).
func reloadWebviewNative() bool { return false }

// registerPowerWakeObserver is the no-op non-darwin stub: NSWorkspace wake
// detection and the blank-after-sleep bug it mitigates are macOS-only
// (specs/decisions/018-macos-webview-recovery.md).
func registerPowerWakeObserver(_ *slog.Logger, _ func()) {}
