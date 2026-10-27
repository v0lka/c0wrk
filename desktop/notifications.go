package desktop

import (
	"context"
	"errors"
	"runtime"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/v0lka/c0wrk/backend/config"
	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// EventNotificationClicked is emitted when the user activates a delivered
// system notification; payload notificationClickedPayload. The Go callback
// focuses the window BEFORE emitting, so the reveal never depends on JS — see
// specs/contracts/event-catalog.md (notification_clicked).
const EventNotificationClicked = "notification_clicked"

// notificationAuthorizationPlatform gates the macOS-only authorization
// prompt: a package var (not a build tag) so any-platform tests can exercise
// the darwin branch.
var notificationAuthorizationPlatform = runtime.GOOS

// notificationDefaultActionIdentifier is the Wails frontends' identifier for
// a banner's default activation (body click). The constant is internal to
// Wails v2 (all three platform frontends define "DEFAULT_ACTION"), so it is
// mirrored here — keep in sync on upgrade.
const notificationDefaultActionIdentifier = "DEFAULT_ACTION"

// notificationClickedPayload is the payload of notification_clicked (JSON
// keys snake_case). SessionID/ProjectID come from the notification's own data
// map and are empty when it carried none — the frontend treats that as a
// no-navigation click.
type notificationClickedPayload struct {
	NotificationID string `json:"notification_id"`
	SessionID      string `json:"session_id"`
	ProjectID      string `json:"project_id"`
}

// stringFromUserInfo reads a well-known optional string field off the
// notification's data map (unknown/typed-differently → "").
func stringFromUserInfo(userInfo map[string]any, key string) string {
	if userInfo == nil {
		return ""
	}
	if v, ok := userInfo[key].(string); ok {
		return v
	}
	return ""
}

// notificationCallback is the single OnNotificationResponse handler wired by
// InitNotifications; the Wails frontends invoke it on its own goroutine. Only
// the default action (banner-body click) navigates: category buttons (unused)
// and error results are ignored. Known accepted quirk: on Linux a reason-2
// dismiss maps to the same identifier and can navigate too (see
// specs/domains/frontend/system-notifications.md).
func (a *App) notificationCallback(result wailsRuntime.NotificationResult) {
	if result.Error != nil {
		a.log().Debug("notification response carried an error; ignoring", "error", result.Error)
		return
	}
	if result.Response.ActionIdentifier != notificationDefaultActionIdentifier {
		a.log().Debug("notification response is not the default action; ignoring",
			"action", result.Response.ActionIdentifier, "notification_id", result.Response.ID)
		return
	}

	payload := notificationClickedPayload{
		NotificationID: result.Response.ID,
		SessionID:      stringFromUserInfo(result.Response.UserInfo, "sessionId"),
		ProjectID:      stringFromUserInfo(result.Response.UserInfo, "projectId"),
	}

	// Focus first, then tell the frontend: the reveal must not depend on JS
	// involvement (a busy or reloaded webview may drop the event entirely).
	if a.wailsCtx() != nil {
		a.showWindow(a.wailsCtx())
	}
	a.emit(EventNotificationClicked, payload)
}

// InitNotifications initializes the OS notification bridge and registers the
// click callback (see specs/domains/frontend/system-notifications.md).
// Idempotent after a successful init; a FAILED init is not memoized so the
// frontend can retry (e.g. once a Linux session bus appears). Must remain on
// App — it requires the Wails context.
func (a *App) InitNotifications() error {
	if a.wailsCtx() == nil {
		return errors.New("InitNotifications: application context is not initialized")
	}

	a.notificationsInitMu.Lock()
	defer a.notificationsInitMu.Unlock()

	if a.notificationsInitialized.Load() {
		return nil
	}

	if a.notificationsInitFn != nil {
		if err := a.notificationsInitFn(a.wailsCtx()); err != nil {
			return err
		}
	} else if err := wailsRuntime.InitializeNotifications(a.wailsCtx()); err != nil {
		return err
	}

	// macOS is the only platform with a real authorization prompt; a denial
	// is NOT an error (the service is up — the user re-enables via OS
	// Settings). Skipped elsewhere to avoid a needless runtime round-trip.
	if notificationAuthorizationPlatform == "darwin" {
		var granted bool
		var err error
		if a.notificationsAuthFn != nil {
			granted, err = a.notificationsAuthFn(a.wailsCtx())
		} else {
			granted, err = wailsRuntime.RequestNotificationAuthorization(a.wailsCtx())
		}
		if err != nil {
			a.log().Warn("notification authorization request failed", "error", err)
		} else if !granted {
			a.log().Info("notification authorization denied; re-enable via OS Settings → Notifications")
		}
	}

	// Registering the callback AFTER a successful initialize keeps the
	// single callback slot consistent with the live notification service.
	if a.onNotificationResponseFn != nil {
		a.onNotificationResponseFn(a.wailsCtx(), a.notificationCallback)
	} else {
		wailsRuntime.OnNotificationResponse(a.wailsCtx(), a.notificationCallback)
	}

	a.notificationsInitialized.Store(true)
	a.log().Info("system notifications initialized", "platform", runtime.GOOS)
	return nil
} // CheckNotificationAuthorization reports whether c0wrk may show banners,
// WITHOUT prompting (the Settings "disabled in OS Settings" hint). macOS
// reads UNNotificationCenter authorization; Linux/Windows return (true, nil).
// Must remain on App (Wails context) — see
// specs/domains/frontend/system-notifications.md.
func (a *App) CheckNotificationAuthorization() (bool, error) {
	if a.wailsCtx() == nil {
		return false, errors.New("CheckNotificationAuthorization: application context is not initialized")
	}
	if a.notificationsAuthCheckFn != nil {
		return a.notificationsAuthCheckFn(a.wailsCtx())
	}
	return wailsRuntime.CheckNotificationAuthorization(a.wailsCtx())
}

// SendSystemNotification sends one native notification — the single
// transport of frontend lib/systemNotifications.ts; the data map supplied
// here returns as UserInfo in the click callback (routing contract keys:
// "sessionId", "projectId"; others forwarded untouched). Linux rides c0wrk's
// icon-augmented D-Bus transport with a fail-soft Wails fallback;
// macOS/Windows stay on the Wails runtime
// (specs/domains/frontend/system-notifications.md). The banner lifetime is
// passed as a resolver — only the Linux transport consumes it, so other
// platforms never log clamp warnings for a knob that has no effect there.
// Must remain on App (Wails context).
func (a *App) SendSystemNotification(title, body string, data map[string]string) error {
	if a.wailsCtx() == nil {
		return errors.New("SendSystemNotification: application context is not initialized")
	}

	userInfo := make(map[string]any, len(data))
	for k, v := range data {
		userInfo[k] = v
	}

	options := wailsRuntime.NotificationOptions{
		ID:    buildNotificationID(),
		Title: title,
		Body:  body,
		Data:  userInfo,
	}

	var err error
	if a.notificationsSendFn != nil {
		err = a.notificationsSendFn(a.wailsCtx(), options)
	} else {
		err = a.sendNotificationPlatform(a.wailsCtx(), options, a.notificationExpireTimeoutMs)
	}
	if err != nil {
		return err
	}
	// Distinguishes "frontend never asked" from "daemon swallowed it" in the
	// logs; see the Diagnostics section of
	// specs/domains/frontend/system-notifications.md. Title/body are NOT
	// logged: a banner body carries task output (SECURITY.md no-secrets rule).
	a.log().Debug("system notification sent",
		"id", options.ID,
		"session", stringFromUserInfo(userInfo, "sessionId"))
	return nil
}

// millisecondsPerSecond converts the config's seconds into the freedesktop
// `expire_timeout` unit.
const millisecondsPerSecond = 1000

// expireTimeoutResolver lazily resolves the configured banner lifetime in
// D-Bus milliseconds. Defined in this shared file so both platform branches
// of sendNotificationPlatform share one signature; the deferral keeps
// macOS/Windows free of clamp warnings for a knob they never consume.
type expireTimeoutResolver = func() int32

// notificationExpireTimeoutMs resolves notifications.banner_timeout_seconds
// into the freedesktop expire_timeout (ms). Sentinels pass through: -1 =
// daemon default, 0 = never expire; an unreachable config also falls back to
// -1, never to 0 (banners would stay on screen forever by accident).
// Out-of-range values are clamped (and the clamp logged) because a
// hand-edited config.yaml reaches this unvalidated: the seconds→ms multiply
// overflows int32 from ~2.15e6 up, and a units mix-up wraps to a NEGATIVE
// value that is neither sentinel nor a valid lifetime.
func (a *App) notificationExpireTimeoutMs() int32 {
	seconds := config.NotificationBannerTimeoutDaemonDefault
	if fa := a.frontendAPI(); fa != nil {
		seconds = fa.GetNotificationBannerTimeout()
	}
	switch {
	case seconds == config.NotificationBannerTimeoutDaemonDefault:
		return -1
	case seconds < config.NotificationBannerTimeoutDaemonDefault:
		a.log().Warn("notification banner timeout is below the -1 sentinel; using the daemon default",
			"configured_seconds", seconds)
		return -1
	case seconds == config.NotificationBannerTimeoutNever:
		return 0
	case seconds > config.NotificationBannerTimeoutMaxSeconds:
		a.log().Warn("notification banner timeout exceeds the maximum; clamping",
			"configured_seconds", seconds,
			"max_seconds", config.NotificationBannerTimeoutMaxSeconds)
		return int32(config.NotificationBannerTimeoutMaxSeconds) * millisecondsPerSecond
	default:
		return int32(seconds) * millisecondsPerSecond
	}
}

// sendNotificationViaWails delivers through the Wails runtime transport —
// the macOS/Windows default and the Linux fail-soft fallback. The banner
// renders without the c0wrk icon, but the click path is identical.
func (a *App) sendNotificationViaWails(ctx context.Context, options wailsRuntime.NotificationOptions) error {
	if a.notificationsSendViaWailsFn != nil {
		return a.notificationsSendViaWailsFn(ctx, options)
	}
	return wailsRuntime.SendNotification(ctx, options)
}

// notificationIDSeq guarantees unique notification ids within one process
// even inside a single millisecond.
var notificationIDSeq atomic.Uint64

// buildNotificationID builds the frontend-globally-unique id (Go side only —
// the frontend has no generator of its own).
func buildNotificationID() string {
	return "c0wrk-notification-" + runtime.GOOS + "-" +
		time.Now().UTC().Format("20060102T150405.000000000") + "-" +
		strconv.FormatUint(notificationIDSeq.Add(1), 10)
}

// ShowTestNotification sends a notification with no routing data for the
// Settings preview button: a click focuses the window but navigates nowhere.
func (a *App) ShowTestNotification() error {
	return a.SendSystemNotification(
		"c0wrk",
		"Notifications are working. Clicking this notification focuses the window.",
		nil,
	)
}

// cleanupNotifications releases the notification service resources — on
// Linux both the Wails transport's D-Bus connection and c0wrk's own
// (cleanupNotificationTransport); called from Shutdown with the lifecycle
// context (parameterized for tests); safe when never initialized.
func (a *App) cleanupNotifications(ctx context.Context) {
	// Our own transport first: a Shutdown may race a late send fallback, and
	// a closed own connection simply redials or falls back on the next send.
	a.cleanupNotificationTransport()

	if ctx == nil {
		return
	}
	if a.notificationsCleanupFn != nil {
		a.notificationsCleanupFn(ctx)
		return
	}
	wailsRuntime.CleanupNotifications(ctx)
}
