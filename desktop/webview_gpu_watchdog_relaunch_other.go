//go:build !linux

package desktop

import (
	"errors"
	"log/slog"
)

// relaunchDetachedSelf is the non-Linux compile stub: the GPU watchdog never
// arms off Linux (specs/domains/webview-gpu-policy.md).
func relaunchDetachedSelf(*slog.Logger) error {
	return errors.New("webview GPU watchdog relaunch is Linux-only")
}
