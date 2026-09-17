package desktop

import (
	"context"
	"errors"
	"runtime"
	"strconv"
	"sync/atomic"
	"time"

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

// SendSystemNotification sends one native notification through the Wails
// runtime. Frontend-callable — the single transport used by the frontend's
// lib/systemNotifications.ts, so clicks round-trip: the data map the frontend
// supplies here is returned as UserInfo in the click callback, from which
// notification_clicked extracts session/project routing.
//
// data keys "sessionId" and "projectId" are the routing contract; other keys
// are forwarded untouched. Must remain on App (Wails context).
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

	if a.notificationsSendFn != nil {
		return a.notificationsSendFn(a.ctx, options)
	}
	return wailsRuntime.SendNotification(a.ctx, options)
}

// notificationIDSeq guarantees unique notification ids within one process
// even inside a single millisecond.
var notificationIDSeq atomic.Uint64

// buildNotificationID builds the frontend-globally-unique notification id
// (mirrors lib/systemNotifications.ts buildNotificationId, keeping the same
// "c0wrk-..." prefix so delivered banners are visually consistent whichever
// layer sent them).
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

// cleanupNotifications releases the notification service resources — on Linux
// this closes the D-Bus session-bus connection. Called from Shutdown with the
// lifecycle context (identical to a.ctx in production; taken as a parameter
// so the teardown stays exercisable in tests); safe to call when
// notifications were never initialized (the Wails call is a no-op on a nil
// connection, and macOS/Windows implement it as a stub).
func (a *App) cleanupNotifications(ctx context.Context) {
	if ctx == nil {
		return
	}
	if a.notificationsCleanupFn != nil {
		a.notificationsCleanupFn(ctx)
		return
	}
	wailsRuntime.CleanupNotifications(ctx)
}
