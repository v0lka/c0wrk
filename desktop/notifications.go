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

// EventNotificationClicked is the global event emitted when the user activates
// a delivered system notification (clicks its body / default action). Payload
// is notificationClickedPayload. The Go callback focuses the main window
// BEFORE emitting, so the window comes forward even if the webview is busy or
// the renderer never handles the event.
const EventNotificationClicked = "notification_clicked"

// notificationAuthorizationPlatform gates the macOS-only authorization
// prompt. A package var (not a build tag) so tests on any platform can
// exercise the darwin branch; production reads runtime.GOOS.
var notificationAuthorizationPlatform = runtime.GOOS

// notificationDefaultActionIdentifier is the action identifier the Wails
// frontends use for the notification's default activation (a click on the
// banner body, not a category action button). The Wails v2 runtime keeps the
// constant internal (internal/frontend/desktop/{linux,darwin,windows}/
// notifications.go all define it as "DEFAULT_ACTION"), so it is mirrored
// here — on upgrade, keep this in sync with those packages.
const notificationDefaultActionIdentifier = "DEFAULT_ACTION"

// notificationClickedPayload is the payload of notification_clicked. JSON
// keys are snake_case, mirroring the other backend → frontend event payloads
// (e.g. exitGuardPayload). SessionID/ProjectID are extracted from the
// notification's own data map (the frontend-supplied routing context) and are
// empty strings when the notification carried none — the frontend treats that
// as a no-navigation click.
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
// InitNotifications. It is invoked on its own goroutine by the Wails
// frontends, so it must be safe against concurrent emission.
//
// Dedupe by action identifier: the Linux and macOS frontends deliver a
// NotificationResult for every notification interaction, and only the default
// action (a click on the banner body) should activate navigation. Category
// action buttons (unused by c0wrk) and platform error results
// (result.Error != nil, e.g. a malformed payload) are ignored — the window is
// neither focused nor navigated.
//
// Known platform quirk (documented, accepted): the Linux frontend also maps
// reason-2 NotificationClosed (user clicked the banner's X) to the default
// action identifier, so on Linux an explicit dismiss can navigate too; there
// is no identifier-level way to distinguish the two. Timeout expiry (1),
// programmatic close (3) and undefined (4) never fire the callback.
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
	if a.ctx != nil {
		a.showWindow(a.ctx)
	}
	a.emit(EventNotificationClicked, payload)
}

// InitNotifications initializes the OS notification bridge and registers the
// notification-click callback. Frontend-callable (Settings preview +
// initSystemNotifications). Idempotent: after a successful init, later calls
// return nil without registering a second OnNotificationResponse callback; a
// FAILED init is not memoized, so the frontend can retry (e.g. after a Linux
// session bus appears).
//
// This must remain on App (not FrontendAPI) because it requires the Wails
// context, exactly like PickDirectory.
func (a *App) InitNotifications() error {
	if a.ctx == nil {
		return errors.New("InitNotifications: application context is not initialized")
	}

	a.notificationsInitMu.Lock()
	defer a.notificationsInitMu.Unlock()

	if a.notificationsInitialized.Load() {
		return nil
	}

	if a.notificationsInitFn != nil {
		if err := a.notificationsInitFn(a.ctx); err != nil {
			return err
		}
	} else if err := wailsRuntime.InitializeNotifications(a.ctx); err != nil {
		return err
	}

	// macOS is the only platform that gates banners behind an explicit
	// authorization prompt. A denial is NOT an error here: the init still
	// counts as successful (the service is up), the denial is logged at
	// info, and the user re-enables banners via OS Settings → Notifications.
	// Linux/Windows implement the runtime call as a granted stub, but it is
	// skipped there anyway to keep the non-macOS path free of a needless
	// runtime round-trip.
	if notificationAuthorizationPlatform == "darwin" {
		var granted bool
		var err error
		if a.notificationsAuthFn != nil {
			granted, err = a.notificationsAuthFn(a.ctx)
		} else {
			granted, err = wailsRuntime.RequestNotificationAuthorization(a.ctx)
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
		a.onNotificationResponseFn(a.ctx, a.notificationCallback)
	} else {
		wailsRuntime.OnNotificationResponse(a.ctx, a.notificationCallback)
	}

	a.notificationsInitialized.Store(true)
	a.log().Info("system notifications initialized", "platform", runtime.GOOS)
	return nil
} // CheckNotificationAuthorization reports whether c0wrk may show banners,
// WITHOUT prompting. Frontend-callable — the Settings notification section
// surfaces a "disabled in OS Settings" hint when this returns false.
//
// Platform semantics (mirroring the Wails frontends): macOS performs a real
// UNNotificationCenter authorization read; Linux and Windows return
// (true, nil) unconditionally (no authorization concept), so the hint never
// renders there. Must remain on App (Wails context).
func (a *App) CheckNotificationAuthorization() (bool, error) {
	if a.ctx == nil {
		return false, errors.New("CheckNotificationAuthorization: application context is not initialized")
	}
	if a.notificationsAuthCheckFn != nil {
		return a.notificationsAuthCheckFn(a.ctx)
	}
	return wailsRuntime.CheckNotificationAuthorization(a.ctx)
}

// SendSystemNotification sends one native notification. Frontend-callable —
// the single transport used by the frontend's lib/systemNotifications.ts, so
// clicks round-trip: the data map the frontend supplies here is returned as
// UserInfo in the click callback, from which notification_clicked extracts
// session/project routing.
//
// Platform routing: on Linux the banner goes through c0wrk's own D-Bus
// transport (sendNotificationPlatform) so it carries the application icon
// (embedded PNG → cache file:// URI → theme-name fallback; any transport
// failure falls back to the Wails path). On macOS/Windows the Wails runtime
// remains the transport — the bundle/AUM already provides the icon there.
//
// data keys "sessionId" and "projectId" are the routing contract; other keys
// are forwarded untouched. Must remain on App (Wails context).
//
// The banner lifetime is handed to the platform hook as a resolver
// (App.notificationExpireTimeoutMs), not as a resolved value: only the Linux
// D-Bus transport consumes it, so macOS/Windows never pay for the resolution
// and never log its clamp warnings for a knob that has no effect there.
func (a *App) SendSystemNotification(title, body string, data map[string]string) error {
	if a.ctx == nil {
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
		err = a.notificationsSendFn(a.ctx, options)
	} else {
		err = a.sendNotificationPlatform(a.ctx, options, a.notificationExpireTimeoutMs)
	}
	if err != nil {
		return err
	}
	// A delivered banner used to leave no trace at all, which made "the
	// frontend never asked for one" and "the daemon swallowed it"
	// indistinguishable from the logs — both looked like silence. Paired with
	// the `Wails EventsEmit called` line of the session event that triggered
	// it, this line is what tells the two apart; see the Diagnostics section
	// of specs/domains/frontend/system-notifications.md.
	//
	// Title and body are deliberately NOT logged: a banner body carries task
	// output, and SECURITY.md extends the no-secrets rule to every output
	// channel.
	a.log().Debug("system notification sent",
		"id", options.ID,
		"session", stringFromUserInfo(userInfo, "sessionId"))
	return nil
}

// millisecondsPerSecond converts the config's seconds into the freedesktop
// `expire_timeout` unit.
const millisecondsPerSecond = 1000

// expireTimeoutResolver lazily resolves the configured banner lifetime in
// D-Bus milliseconds. The resolution is deliberately deferred to the platform
// hook: the value is consumed by the Linux D-Bus call alone, so on macOS/
// Windows a hand-edited out-of-range config must not produce clamp warnings
// about a knob that has no effect there (the notification center owns banner
// lifetime).
//
// Defined in this shared file (not a build-tagged one) so both platform
// branches of sendNotificationPlatform share one signature.
type expireTimeoutResolver = func() int32

// notificationExpireTimeoutMs resolves the configured banner lifetime
// (backend config `notifications.banner_timeout_seconds`) into the
// freedesktop `expire_timeout` argument, in milliseconds. The two sentinels
// pass through unchanged: -1 = the daemon's own default, 0 = never expire.
//
// Falls back to the daemon default whenever the config is unreachable (early
// startup, tests with a zero-value FrontendAPI) — never to 0, which would
// leave banners on screen forever by accident.
//
// Out-of-range values are clamped here rather than trusted, because only the
// Settings RPC (SetNotificationBannerTimeout) range-checks its input: a
// hand-edited config.yaml reaches this function unvalidated. Clamping is
// load-bearing above the maximum — the seconds→milliseconds multiply
// overflows int32 from ~2.15e6 seconds up, and a units mix-up
// (`banner_timeout_seconds: 3600000`, meaning milliseconds) wraps to a
// NEGATIVE expire_timeout that is neither the -1 sentinel nor a valid
// lifetime. Every clamp is logged: a misconfigured value must not be applied
// silently.
func (a *App) notificationExpireTimeoutMs() int32 {
	seconds := config.NotificationBannerTimeoutDaemonDefault
	if a.FrontendAPI != nil {
		seconds = a.GetNotificationBannerTimeout()
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

// sendNotificationViaWails delivers a notification through the Wails runtime
// transport — the platform default on macOS/Windows and the fail-soft
// fallback on Linux when the icon-augmented D-Bus transport cannot deliver
// (no session bus, daemon error, …). The banner then renders without the
// c0wrk icon, but the click path is identical: Wails tracks the notification
// and its OnNotificationResponse callback (App.notificationCallback, wired in
// InitNotifications) still fires on activation.
func (a *App) sendNotificationViaWails(ctx context.Context, options wailsRuntime.NotificationOptions) error {
	if a.notificationsSendViaWailsFn != nil {
		return a.notificationsSendViaWailsFn(ctx, options)
	}
	return wailsRuntime.SendNotification(ctx, options)
}

// notificationIDSeq guarantees unique notification ids within one process
// even inside a single millisecond.
var notificationIDSeq atomic.Uint64

// buildNotificationID builds the frontend-globally-unique notification id.
// The id is generated on the Go side only (the frontend carries no generator
// of its own); the shared "c0wrk-..." prefix keeps delivered banners visually
// consistent whichever layer sent them.
func buildNotificationID() string {
	return "c0wrk-notification-" + runtime.GOOS + "-" +
		time.Now().UTC().Format("20060102T150405.000000000") + "-" +
		strconv.FormatUint(notificationIDSeq.Add(1), 10)
}

// ShowTestNotification sends a notification with no routing data, for the
// Settings "preview" button. A click on it focuses the window (the Go
// callback's showWindow) but performs no navigation — the payload carries an
// empty session id, which the frontend treats as a logged no-op.
func (a *App) ShowTestNotification() error {
	return a.SendSystemNotification(
		"c0wrk",
		"Notifications are working. Clicking this notification focuses the window.",
		nil,
	)
}

// cleanupNotifications releases the notification service resources — on
// Linux this closes the D-Bus session-bus connection of the Wails transport
// AND c0wrk's own icon-augmented transport (cleanupNotificationTransport, a
// no-op on other platforms). Called from Shutdown with the lifecycle context
// (identical to a.ctx in production; taken as a parameter so the teardown
// stays exercisable in tests); safe to call when notifications were never
// initialized (the Wails call is a no-op on a nil connection, and
// macOS/Windows implement it as a stub).
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
