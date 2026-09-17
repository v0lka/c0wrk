//go:build !linux

package desktop

import (
	"context"

	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// Platform branches of the system-notification transport. On Linux
// (notifications_linux.go) c0wrk sends banners through its own
// org.freedesktop.Notifications D-Bus call so the app_icon argument carries
// the embedded application icon. On macOS and Windows the Wails transport is
// kept as-is: the app bundle (macOS) and the AppUserModelID (Windows)
// already resolve the correct icon from the packaged application, so an
// icon-augmented custom transport would be pure duplication with nothing to
// fix — and nothing to fall back from.
//
// The click path needs no platform branch at all: InitNotifications already
// registers App.notificationCallback with the Wails runtime on every
// platform, and on Linux the custom transport routes D-Bus signals into that
// same callback.

// sendNotificationPlatform is the non-Linux branch of SendSystemNotification's
// platform hook (only reached when the notificationsSendFn test seam is
// unset): deliver straight through the Wails runtime.
// expireTimeoutMs is accepted for signature parity with the Linux branch and
// deliberately ignored: the macOS and Windows notification centers own banner
// lifetime themselves and expose no per-notification expiry to the sender.
func (a *App) sendNotificationPlatform(ctx context.Context, options wailsRuntime.NotificationOptions, _ int32) error {
	return a.sendNotificationViaWails(ctx, options)
}

// cleanupNotificationTransport is the non-Linux teardown: the Wails runtime
// implements notification cleanup as a stub on macOS/Windows, so there is no
// transport state of our own to release.
func (a *App) cleanupNotificationTransport() {}
