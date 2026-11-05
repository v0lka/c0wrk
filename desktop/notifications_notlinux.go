//go:build !linux

package desktop

import (
	"context"

	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// Platform branches of the system-notification transport: on Linux the
// icon-augmented D-Bus transport, on macOS/Windows the stock Wails runtime
// (the packaged app already resolves the icon); the click path needs no
// branch. See specs/domains/frontend/system-notifications.md.

// sendNotificationPlatform is the non-Linux branch of the platform hook
// (only reached when the notificationsSendFn test seam is unset); the
// expire-timeout resolver is signature parity, never called — those centers
// own banner lifetime. See specs/domains/frontend/system-notifications.md.
func (a *App) sendNotificationPlatform(ctx context.Context, options wailsRuntime.NotificationOptions, _ expireTimeoutResolver) error {
	return a.sendNotificationViaWails(ctx, options)
}

// cleanupNotificationTransport is the non-Linux teardown: Wails implements
// notification cleanup as a stub there — no transport state of our own.
func (a *App) cleanupNotificationTransport() {}
