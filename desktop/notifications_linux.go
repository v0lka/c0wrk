//go:build linux

package desktop

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"runtime/debug"
	"slices"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// This file implements c0wrk's own Linux notification transport: the same
// org.freedesktop.Notifications D-Bus API the Wails frontend talks to, but
// with the app_icon argument populated so banners carry the application icon
// even when no .desktop file / hicolor theme is installed (dev mode via
// `wails dev`, a standalone binary, or a non-theme-aware notification
// daemon). macOS/Windows keep the Wails transport (notifications_notlinux.go
// — the bundle/AUM already provides the icon there).
//
// Click routing coexists with the Wails transport without double delivery:
// each notification is tracked by exactly ONE side. Notifications sent here
// live in our pending map (Wails' internal map never sees their D-Bus ids,
// so its signal handler drops them); notifications sent through the Wails
// fallback live only in Wails' map and our handler drops those ids. Both
// paths funnel into the same App.notificationCallback.
//
// Everything here is fail-soft: on any D-Bus failure the send falls back to
// the Wails transport (icon-less but functional), so a notification is never
// lost because the icon-augmented path broke.

const (
	// dbusNotificationsInterface / dbusNotificationsPath are the well-known
	// org.freedesktop.Notifications service coordinates every desktop
	// notification daemon (GNOME Shell, KDE Plasma, dunst, swaync, …) serves.
	dbusNotificationsInterface = "org.freedesktop.Notifications"
	dbusNotificationsPath      = "/org/freedesktop/Notifications"

	// notificationAppName is the app_name argument of Notify. A fixed brand
	// name (not the executable basename the Wails frontend derives) so the
	// banner reads "c0wrk" regardless of how the binary was launched.
	notificationAppName = "c0wrk"

	// notificationIconThemeName is the freedesktop icon-theme name packaged
	// installs (AUR builds) install the icon under. Used as the app_icon
	// value when the embedded-icon file URI is unavailable (cache dir not
	// writable): daemons that resolve theme names still find an icon.
	notificationIconThemeName = "c0wrk"

	// notificationIconDirName is the subdirectory (of the user cache dir)
	// holding the exported icon, and notificationIconFileName its name.
	notificationIconDirName  = "c0wrk"
	notificationIconFileName = "notification-icon.png"

	// notificationIconFileMode is the mode of the exported icon: readable by
	// the notification daemon, which may run as a dedicated service user.
	notificationIconFileMode = 0o644

	// dbusNotificationTimeoutDefault (-1) asks the daemon to use its own
	// default expiry; it is not a timeout value itself.
	dbusNotificationTimeoutDefault = int32(-1)

	// dbusDefaultActionKey is the D-Bus action key of the banner's default
	// activation (a click on the body), mirroring the Wails frontend.
	dbusDefaultActionKey = "default"

	// notificationDesktopEntry is the basename of c0wrk's .desktop file,
	// sent as the freedesktop `desktop-entry` hint. It is how a notification
	// daemon binds a banner to the installed application: grouping, the
	// source name, the per-application entries in the desktop's notification
	// settings, and — on daemons that prefer it over app_icon — the icon.
	// Packaged installs ship /usr/share/applications/c0wrk.desktop; on a
	// system where that file is absent the hint is simply unresolvable and
	// daemons fall back to app_name/app_icon, so sending it is never worse
	// than omitting it.
	notificationDesktopEntry = "c0wrk"

	// dbusCapabilityActions is the capability a daemon advertises when it
	// honors the action list a notification carries. Without it the `default`
	// action is ignored, no ActionInvoked signal is ever emitted, and the
	// only routing left is the reason-2 dismiss quirk — see the dial-time
	// probe in ensureDialLocked.
	dbusCapabilityActions = "actions"

	// notificationPendingTTL / notificationPendingMax bound the routing map.
	// Entries are normally consumed by ActionInvoked/NotificationClosed, but
	// a daemon is not obliged to signal anything: KDE Plasma lets an expired
	// banner disappear with no NotificationClosed at all, which would leak
	// its entry for the life of the process. Both bounds are generous — a
	// banner the user has not acted on must stay routable for as long as it
	// could plausibly still be clicked.
	notificationPendingTTL = 24 * time.Hour
	notificationPendingMax = 256
)

// dbusDialer abstracts the minimal *dbus.Conn surface the transport needs so
// tests can drive the protocol logic against a fake session bus. Production
// wires a *dbus.Conn through the connDialer adapter below.
type dbusDialer interface {
	Object(dest string, path dbus.ObjectPath) dbusObj
	AddMatchSignal(options ...dbus.MatchOption) error
	Signal(ch chan<- *dbus.Signal)
	Close() error
}

// dbusObj narrows dbus.BusObject to the single Call the transport makes.
type dbusObj interface {
	Call(method string, flags dbus.Flags, args ...any) *dbus.Call
}

// connDialer adapts a real *dbus.Conn to the dbusDialer seam.
type connDialer struct{ conn *dbus.Conn }

func (c connDialer) Object(dest string, path dbus.ObjectPath) dbusObj {
	return connObject{obj: c.conn.Object(dest, path)}
}

func (c connDialer) AddMatchSignal(options ...dbus.MatchOption) error {
	return c.conn.AddMatchSignal(options...)
}

func (c connDialer) Signal(ch chan<- *dbus.Signal) { c.conn.Signal(ch) }

func (c connDialer) Close() error { return c.conn.Close() }

// connObject adapts a real dbus.BusObject to the dbusObj seam.
type connObject struct{ obj dbus.BusObject }

func (o connObject) Call(method string, flags dbus.Flags, args ...any) *dbus.Call {
	return o.obj.Call(method, flags, args...)
}

// dbusDialSessionBus is the dial seam (package var so Linux tests can
// replace it; production dials the real session bus lazily on first send).
var dbusDialSessionBus = func() (dbusDialer, error) {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return nil, err
	}
	return connDialer{conn: conn}, nil
}

// notificationIconCacheDir is the seam over os.UserCacheDir (tests redirect
// the exported icon into a temp dir instead of the real user cache).
var notificationIconCacheDir = os.UserCacheDir

// platformNotificationState carries the Linux transport state. One instance
// per App (a field on App, initialized by NewApp) so concurrent App
// instances — tests — never share a connection or a routing map.
type platformNotificationState struct {
	mu sync.Mutex
	// conn is the lazily-dialed session-bus connection; nil until the first
	// successful dial. Dropped (and closed) on any send failure so the next
	// send redials instead of hammering a dead connection.
	conn dbusDialer
	// cancel stops the signal-pump goroutine; set together with conn.
	cancel context.CancelFunc
	// pending maps daemon-assigned notification ids to their routing meta;
	// entries are consumed exactly once by ActionInvoked/NotificationClosed.
	pending map[uint32]linuxNotificationMeta
	// icon caches a successfully exported file:// icon URI ("" while unset:
	// a failed export falls back to the theme name on EVERY send, since the
	// cache dir may become writable later).
	icon string
}

// linuxNotificationMeta is the per-notification bookkeeping needed to route
// a D-Bus signal back into the App callback: the Wails-format notification
// id and the routing userInfo the click payload extracts sessionId/projectId
// from.
type linuxNotificationMeta struct {
	wailsID  string
	userInfo map[string]any
	// sentAt is when the notification was handed to the daemon; it exists
	// solely so prunePendingLocked can evict entries the daemon never
	// reported on (see notificationPendingTTL).
	sentAt time.Time
}

// linuxNotifications is the process-wide production transport state. A
// package-level singleton (mirroring the Wails frontend's own layout) rather
// than an App field: the platformNotificationState type only exists on
// Linux, so an App field would break the !linux build. Production runs one
// App per process; tests exercise the protocol logic on their own
// &platformNotificationState{} instances plus this singleton via the dial
// seam.
var linuxNotifications platformNotificationState

// Platform hook: deliver one notification through the current platform's
// transport. The expireTimeoutResolver is consumed by the Linux branch only
// (see the Linux and !linux implementations for the asymmetry).
func (a *App) sendNotificationPlatform(ctx context.Context, options wailsRuntime.NotificationOptions, resolveExpireTimeout expireTimeoutResolver) error {
	var expireTimeoutMs int32
	if resolveExpireTimeout != nil {
		expireTimeoutMs = resolveExpireTimeout()
	}
	if err := linuxNotifications.send(options, a.notificationCallback, expireTimeoutMs, a.log()); err != nil {
		a.log().Warn("linux D-Bus notification transport failed; falling back to the Wails transport",
			"error", err)
		return a.sendNotificationViaWails(ctx, options)
	}
	return nil
}

// cleanupNotificationTransport closes the D-Bus connection (stopping the
// signal pump) on Shutdown. Safe when notifications were never sent.
func (a *App) cleanupNotificationTransport() {
	linuxNotifications.teardown()
}

// sendSerial serializes D-Bus Notify round trips. Sends hold NO state lock
// across the blocking call (see send): the pump's takePending and a Shutdown
// teardown must stay free while a daemon is slow to answer, so the
// serialization lives on its own mutex instead.
var sendSerial sync.Mutex

// send delivers one notification over D-Bus. Callers hold no locks; the
// state mutex guards only the dial and the pending-map write — never the
// blocking Notify round trip. A wedged or restarting notification daemon can
// make that round trip take arbitrarily long, and holding s.mu across it
// would stall the signal pump's takePending (godbus silently discards
// signals delivered while the pump is busy — the channel buffer only
// softens the window), exactly the failure class the X11 activation path
// engineered away. Concurrent sends serialize on sendSerial so the pending
// registration below cannot interleave out of order.
func (s *platformNotificationState) send(options wailsRuntime.NotificationOptions, dispatch func(wailsRuntime.NotificationResult), expireTimeoutMs int32, log *slog.Logger) error {
	sendSerial.Lock()
	defer sendSerial.Unlock()

	s.mu.Lock()
	if err := s.ensureDialLocked(dispatch, log); err != nil {
		s.mu.Unlock()
		return err
	}
	conn := s.conn
	obj := conn.Object(dbusNotificationsInterface, dbusNotificationsPath)
	icon := s.iconLocked()
	s.mu.Unlock()

	call := obj.Call(
		dbusNotificationsInterface+".Notify",
		0,
		notificationAppName,
		uint32(0), // replaces_id: always a fresh notification
		icon,
		options.Title,
		options.Body,
		[]string{dbusDefaultActionKey, "Default"},
		map[string]dbus.Variant{
			"x-notification-id": dbus.MakeVariant(options.ID),
			"desktop-entry":     dbus.MakeVariant(notificationDesktopEntry),
		},
		expireTimeoutMs,
	)
	if call.Err != nil {
		// The connection may be dead (bus went away, daemon restarted): drop
		// it so the next send redials instead of failing forever. Only the
		// connection this call rode on is torn down; a concurrent send that
		// already redialled must not lose its fresh connection to a stale
		// teardown.
		s.teardownIfCurrent(conn)
		return fmt.Errorf("notify call: %w", call.Err)
	}
	var dbusID uint32
	if err := call.Store(&dbusID); err != nil {
		return fmt.Errorf("store notify id: %w", err)
	}
	// Register the routing entry under the state mutex: the pump's
	// takePending reads this map under the same lock. Sends serialize on
	// sendSerial, so entries still land in send order.
	s.mu.Lock()
	if s.pending == nil {
		s.pending = make(map[uint32]linuxNotificationMeta)
	}
	s.pending[dbusID] = linuxNotificationMeta{
		wailsID:  options.ID,
		userInfo: options.Data,
		sentAt:   time.Now(),
	}
	s.prunePendingLocked()
	s.mu.Unlock()
	return nil
}

// ensureDialLocked dials the session bus on first use and starts the signal
// pump. Must be called with s.mu held.
func (s *platformNotificationState) ensureDialLocked(dispatch func(wailsRuntime.NotificationResult), log *slog.Logger) error {
	if s.conn != nil {
		return nil
	}
	conn, err := dbusDialSessionBus()
	if err != nil {
		return fmt.Errorf("dial session bus: %w", err)
	}
	if err := conn.AddMatchSignal(
		dbus.WithMatchInterface(dbusNotificationsInterface),
		dbus.WithMatchMember("ActionInvoked"),
	); err != nil {
		_ = conn.Close()
		return fmt.Errorf("subscribe ActionInvoked: %w", err)
	}
	if err := conn.AddMatchSignal(
		dbus.WithMatchInterface(dbusNotificationsInterface),
		dbus.WithMatchMember("NotificationClosed"),
	); err != nil {
		_ = conn.Close()
		return fmt.Errorf("subscribe NotificationClosed: %w", err)
	}

	// Capability probe, once per dial: purely diagnostic, never fatal. A
	// daemon that does not advertise "actions" silently ignores the action
	// list every send carries, so no ActionInvoked ever arrives and a banner
	// click does nothing. Without this line that degradation is invisible —
	// banners appear, clicks do not work, and nothing says why.
	logActionsCapability(conn, log)

	pumpCtx, cancel := context.WithCancel(context.Background())
	signals := make(chan *dbus.Signal, 16)
	conn.Signal(signals)

	s.conn = conn
	s.cancel = cancel
	s.pending = make(map[uint32]linuxNotificationMeta)

	// dispatch travels with the pump instead of living on the struct: a
	// redial (teardown after a failed Notify, then the next send) would
	// otherwise write the field while the previous pump — which cancel() has
	// signalled but not yet stopped — is still reading it to route a signal.
	go s.pumpSignals(pumpCtx, signals, dispatch, log)
	return nil
}

// logActionsCapability reads the daemon's GetCapabilities and reports whether
// banner clicks can route at all. Every failure path is a debug line and a
// return: a daemon that does not answer the probe is not a reason to fail a
// send.
func logActionsCapability(conn dbusDialer, log *slog.Logger) {
	if log == nil {
		return
	}
	call := conn.Object(dbusNotificationsInterface, dbusNotificationsPath).
		Call(dbusNotificationsInterface+".GetCapabilities", 0)
	if call.Err != nil {
		log.Debug("notification daemon capability probe failed", "error", call.Err)
		return
	}
	var caps []string
	if err := call.Store(&caps); err != nil {
		log.Debug("notification daemon capability probe returned an unexpected shape", "error", err)
		return
	}
	if !slices.Contains(caps, dbusCapabilityActions) {
		log.Warn("notification daemon does not advertise the \"actions\" capability; "+
			"clicking a banner will not open its session (only dismissing it can)",
			"capabilities", caps)
		return
	}
	log.Debug("notification daemon capabilities", "capabilities", caps)
}

// dispatchSafely runs the App click callback on its own goroutine, mirroring
// the recovery policy of the Wails frontends' handleNotificationResult ("log
// panic but don't crash the app"): the callback reaches the X11 activation
// path (window_activation_linux.go, cgo/Xlib) — the one area of this app that
// has already crashed startup once. An unrecovered panic on a bare `go
// dispatch(...)` would kill the whole process on a banner click, far from the
// fault site and on a user gesture.
func dispatchSafely(dispatch func(wailsRuntime.NotificationResult), result wailsRuntime.NotificationResult, log *slog.Logger) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Error("panic in notification click callback; recovered",
					"panic", r,
					"stack", string(debug.Stack()))
			}
		}()
		dispatch(result)
	}()
}

// pumpSignals forwards matched signals to the handlers until the state is
// torn down. Both ctx cancellation and the channel close (godbus closes
// registered channels on Conn.Close) end the pump, mirroring the Wails loop.
func (s *platformNotificationState) pumpSignals(ctx context.Context, ch <-chan *dbus.Signal, dispatch func(wailsRuntime.NotificationResult), log *slog.Logger) {
	for {
		select {
		case <-ctx.Done():
			return
		case sig, ok := <-ch:
			if !ok {
				return
			}
			s.handleSignal(sig, dispatch, log)
		}
	}
}

// handleSignal routes one D-Bus signal; ids this process did not send
// (another app's notifications, which the match rules also deliver) find no
// pending entry and are dropped.
func (s *platformNotificationState) handleSignal(sig *dbus.Signal, dispatch func(wailsRuntime.NotificationResult), log *slog.Logger) {
	switch sig.Name {
	case dbusNotificationsInterface + ".ActionInvoked":
		s.handleActionInvoked(sig, dispatch, log)
	case dbusNotificationsInterface + ".NotificationClosed":
		s.handleNotificationClosed(sig, dispatch, log)
	}
}

// handleActionInvoked maps the banner's default activation to the Wails
// NotificationResult contract and hands it to the App callback. Non-default
// actions (c0wrk registers none) are dropped after consuming the pending
// entry, mirroring the frontend's ActionMap behavior.
func (s *platformNotificationState) handleActionInvoked(sig *dbus.Signal, dispatch func(wailsRuntime.NotificationResult), log *slog.Logger) {
	if len(sig.Body) < 2 {
		return
	}
	dbusID, ok := sig.Body[0].(uint32)
	if !ok {
		return
	}
	actionID, ok := sig.Body[1].(string)
	if !ok {
		return
	}
	meta, ok := s.takePending(dbusID)
	if !ok {
		return
	}
	if actionID != dbusDefaultActionKey {
		return
	}
	// Dispatch on its own goroutine (guarded by dispatchSafely): the callback
	// activates the window, which makes blocking X11 round trips. Running it
	// inline would block the signal pump, and a blocked pump means godbus
	// silently discards every later ActionInvoked — i.e. one slow activation
	// permanently kills notification clicks. See the XCloseDisplay wedge in
	// window_activation_linux.go.
	dispatchSafely(dispatch, wailsRuntime.NotificationResult{
		Response: wailsRuntime.NotificationResponse{
			ID:               meta.wailsID,
			ActionIdentifier: notificationDefaultActionIdentifier,
			UserInfo:         meta.userInfo,
		},
	}, log)
}

// handleNotificationClosed reproduces the documented Wails quirk: close
// reason 2 (dismissed by the user) is delivered as the default action —
// there is no identifier-level way to distinguish it from a body click.
// Reasons 1 (timeout), 3 (programmatic close) and 4 (undefined) are dropped.
func (s *platformNotificationState) handleNotificationClosed(sig *dbus.Signal, dispatch func(wailsRuntime.NotificationResult), log *slog.Logger) {
	if len(sig.Body) < 2 {
		return
	}
	dbusID, ok := sig.Body[0].(uint32)
	if !ok {
		return
	}
	reason, ok := sig.Body[1].(uint32)
	if !ok {
		return
	}
	meta, ok := s.takePending(dbusID)
	if !ok {
		return
	}
	if reason != 2 {
		return
	}
	// Off the pump goroutine, with the same recovery policy — see
	// handleActionInvoked and dispatchSafely.
	dispatchSafely(dispatch, wailsRuntime.NotificationResult{
		Response: wailsRuntime.NotificationResponse{
			ID:               meta.wailsID,
			ActionIdentifier: notificationDefaultActionIdentifier,
			UserInfo:         meta.userInfo,
		},
	}, log)
}

// prunePendingLocked bounds the routing map: it drops entries older than
// notificationPendingTTL and, if the map is still over notificationPendingMax,
// evicts the oldest until it fits. Must be called with s.mu held.
//
// Without it the map grows without limit, because a notification the daemon
// silently drops (Plasma expires banners with no NotificationClosed signal)
// leaves its entry behind forever.
func (s *platformNotificationState) prunePendingLocked() {
	cutoff := time.Now().Add(-notificationPendingTTL)
	for id, meta := range s.pending {
		if meta.sentAt.Before(cutoff) {
			delete(s.pending, id)
		}
	}
	for len(s.pending) > notificationPendingMax {
		var oldestID uint32
		var oldestAt time.Time
		first := true
		for id, meta := range s.pending {
			if first || meta.sentAt.Before(oldestAt) {
				oldestID, oldestAt, first = id, meta.sentAt, false
			}
		}
		delete(s.pending, oldestID)
	}
}

// takePending removes and returns the routing meta for a daemon id (false
// when the id is unknown — a foreign notification).
func (s *platformNotificationState) takePending(dbusID uint32) (linuxNotificationMeta, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	meta, ok := s.pending[dbusID]
	if ok {
		delete(s.pending, dbusID)
	}
	return meta, ok
}

// teardown closes the connection and stops the pump.
func (s *platformNotificationState) teardown() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.teardownLocked()
}

// teardownIfCurrent drops and closes the connection ONLY when it is still the
// one the state currently holds. A failed Notify must trigger a redial on the
// next send, but a concurrent send may already have redialled (its fresh
// connection is not the dead one this call rode on) — closing that would
// break a healthy transport mid-flight.
func (s *platformNotificationState) teardownIfCurrent(conn dbusDialer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn != conn {
		return
	}
	s.teardownLocked()
}

// teardownLocked drops and closes the connection; must be called with s.mu
// held. No-op when the transport was never dialed.
func (s *platformNotificationState) teardownLocked() {
	if s.conn == nil {
		return
	}
	cancel, conn := s.cancel, s.conn
	s.conn = nil
	s.cancel = nil
	s.pending = nil
	if cancel != nil {
		cancel()
	}
	_ = conn.Close() // also closes the pump's signal channel
}

// iconLocked resolves the app_icon argument: the file:// URI of the exported
// embedded icon, or the theme name when the export is impossible. Must be
// called with s.mu held.
func (s *platformNotificationState) iconLocked() string {
	if s.icon != "" {
		return s.icon
	}
	if uri, ok := exportNotificationIcon(); ok {
		s.icon = uri
		return uri
	}
	return notificationIconThemeName
}

// exportNotificationIcon writes the embedded PNG into the user cache dir and
// returns its file:// URI. The URI is percent-encoded via net/url (cache
// paths may contain spaces or non-ASCII user names).
func exportNotificationIcon() (string, bool) {
	dir, err := notificationIconCacheDir()
	if err != nil {
		return "", false
	}
	path := filepath.Join(dir, notificationIconDirName, notificationIconFileName)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", false
	}
	if err := os.WriteFile(path, notificationIconPNG(), notificationIconFileMode); err != nil {
		return "", false
	}
	return (&url.URL{Scheme: "file", Path: path}).String(), true
}
