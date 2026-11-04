//go:build linux

package desktop

import (
	"context"
	"errors"
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

	"github.com/v0lka/sp4rk/safeio"
)

// c0wrk's own Linux notification transport: the org.freedesktop.Notifications
// D-Bus API with app_icon populated (the Wails frontend hard-codes it to ""),
// plus the click-routing split and fail-soft Wails fallback — see
// specs/domains/frontend/system-notifications.md.

const (
	// The well-known org.freedesktop.Notifications service coordinates every
	// desktop notification daemon serves.
	dbusNotificationsInterface = "org.freedesktop.Notifications"
	dbusNotificationsPath      = "/org/freedesktop/Notifications"

	// notificationAppName is Notify's app_name argument — a fixed brand name,
	// not the executable basename the Wails frontend derives.
	notificationAppName = "c0wrk"

	// notificationIconThemeName is the app_icon fallback when the exported
	// file URI is unavailable (unwritable cache dir); packaged installs ship
	// the icon under this freedesktop theme name.
	notificationIconThemeName = "c0wrk"

	// notificationIconDirName is the cache-dir subdirectory holding the
	// exported icon, notificationIconFileName its name, and
	// notificationIconFileMode its mode (readable by the daemon, which may
	// run as a dedicated service user).
	notificationIconDirName  = "c0wrk"
	notificationIconFileName = "notification-icon.png"
	notificationIconFileMode = 0o644

	// dbusNotificationTimeoutDefault (-1) asks the daemon to use its own
	// default expiry; it is not a timeout value itself.
	dbusNotificationTimeoutDefault = int32(-1)

	// dbusDefaultActionKey is the D-Bus action key of the banner's default
	// activation (a click on the body), mirroring the Wails frontend.
	dbusDefaultActionKey = "default"

	// notificationDesktopEntry is the freedesktop `desktop-entry` hint — how
	// a daemon binds the banner to the installed application. Unresolvable
	// when c0wrk.desktop is absent; sending it is never worse than omitting
	// it (specs/domains/frontend/system-notifications.md).
	notificationDesktopEntry = "c0wrk"

	// dbusCapabilityActions is the capability a daemon advertises when it
	// honors the action list; without it no ActionInvoked ever arrives and
	// only the reason-2 dismiss quirk routes clicks — hence the once-per-dial
	// probe in dial.
	dbusCapabilityActions = "actions"

	// notificationPendingTTL / notificationPendingMax bound the routing map:
	// a daemon is not obliged to signal anything (Plasma expires banners with
	// no NotificationClosed), which would otherwise leak entries for the
	// life of the process.
	notificationPendingTTL = 24 * time.Hour
	notificationPendingMax = 256
)

// dbusDialer abstracts the minimal *dbus.Conn surface the transport needs so
// tests can drive the protocol logic against a fake session bus.
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

// platformNotificationState carries the Linux transport state; one instance
// per App so concurrent App instances (tests) never share a connection or
// routing map.
type platformNotificationState struct {
	mu sync.Mutex
	// conn is the lazily-dialed session-bus connection; dropped (and closed)
	// on any send failure so the next send redials.
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
	// gen increments on every teardown: a send whose unlocked dial is in
	// flight while teardown runs must not publish the fresh connection
	// afterwards. Sends capture the generation before dialing and re-check it
	// before publishing. teardownIfCurrent does not bump it — dialing again
	// after a redial is legitimate.
	gen uint64
}

// linuxNotificationMeta is the per-notification bookkeeping needed to route a
// D-Bus signal back into the App callback: the Wails-format id and the
// routing userInfo the click payload extracts sessionId/projectId from.
type linuxNotificationMeta struct {
	wailsID  string
	userInfo map[string]any
	// sentAt exists solely so prunePendingLocked can evict entries the
	// daemon never reported on (see notificationPendingTTL).
	sentAt time.Time
}

// linuxNotifications is the process-wide production transport state — a
// package-level singleton because platformNotificationState only exists on
// Linux and an App field would break the !linux build.
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

// sendSerial serializes D-Bus Notify round trips: sends hold NO state lock
// across the blocking call (see send), so serialization needs its own mutex.
var sendSerial sync.Mutex

// send delivers one notification over D-Bus. The state mutex guards only the
// dial and the pending-map write — never the blocking Notify round trip: a
// wedged daemon can stretch it arbitrarily, and holding s.mu would stall the
// pump's takePending (godbus silently discards signals while the pump is
// busy). sendSerial serializes sends so pending registrations keep order.
func (s *platformNotificationState) send(options wailsRuntime.NotificationOptions, dispatch func(wailsRuntime.NotificationResult), expireTimeoutMs int32, log *slog.Logger) error {
	sendSerial.Lock()
	defer sendSerial.Unlock()

	conn, icon, err := s.ensureDial(dispatch, log)
	if err != nil {
		return err
	}
	obj := conn.Object(dbusNotificationsInterface, dbusNotificationsPath)

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
		// The connection may be dead (bus away, daemon restarted): drop it so
		// the next send redials — only the connection this call rode on, so a
		// concurrent send that already redialled keeps its fresh connection.
		s.teardownIfCurrent(conn)
		return fmt.Errorf("notify call: %w", call.Err)
	}
	var dbusID uint32
	if err := call.Store(&dbusID); err != nil {
		return fmt.Errorf("store notify id: %w", err)
	}
	// Registered under the state mutex, which the pump's takePending also
	// holds; sendSerial keeps entries in send order.
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

// ensureDial returns the live connection (dialing it on first use) plus the
// resolved app_icon argument.
//
// The dial, match registration and capability probe run WITHOUT s.mu: they
// are deadline-less D-Bus round trips, and holding the state mutex across
// them would let a daemon that accepts the socket but never answers park it
// indefinitely — wedging the pump's takePending and Shutdown's teardown
// (which runs BEFORE stopEmbeddedLLM, so a stall there skips the
// embedded-model stop and orphans the detached llama-server). Only the
// fast-path check, the publish and the icon resolution take the lock;
// sendSerial keeps at most one dial in flight, and an interleaved teardown
// turns the publish into a refused send via the generation guard.
func (s *platformNotificationState) ensureDial(dispatch func(wailsRuntime.NotificationResult), log *slog.Logger) (dbusDialer, string, error) {
	var gen uint64
	s.mu.Lock()
	if s.conn != nil {
		conn, icon := s.conn, s.iconLocked()
		s.mu.Unlock()
		return conn, icon, nil
	}
	gen = s.gen
	s.mu.Unlock()

	conn, cancel, err := s.dial(dispatch, log)
	if err != nil {
		return nil, "", err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.gen != gen || s.conn != nil {
		// A teardown ran while the unlocked dial was in flight — publish
		// nothing after it. (The s.conn != nil half is unreachable under
		// sendSerial; kept as a safety net: serve the published connection.)
		if cancel != nil {
			cancel()
		}
		_ = conn.Close()
		if s.conn != nil {
			return s.conn, s.iconLocked(), nil
		}
		return nil, "", errors.New("notification transport torn down during dial")
	}
	s.conn = conn
	s.cancel = cancel
	s.pending = make(map[uint32]linuxNotificationMeta)
	icon := s.iconLocked()
	return conn, icon, nil
}

// dial performs the connection setup that must NOT run under s.mu (see
// ensureDial): session-bus dial, the two match rules, the capability probe,
// the signal pump. Any error closes the half-configured connection first.
func (s *platformNotificationState) dial(dispatch func(wailsRuntime.NotificationResult), log *slog.Logger) (dbusDialer, context.CancelFunc, error) {
	conn, err := dbusDialSessionBus()
	if err != nil {
		return nil, nil, fmt.Errorf("dial session bus: %w", err)
	}
	if err := conn.AddMatchSignal(
		dbus.WithMatchInterface(dbusNotificationsInterface),
		dbus.WithMatchMember("ActionInvoked"),
	); err != nil {
		_ = conn.Close()
		return nil, nil, fmt.Errorf("subscribe ActionInvoked: %w", err)
	}
	if err := conn.AddMatchSignal(
		dbus.WithMatchInterface(dbusNotificationsInterface),
		dbus.WithMatchMember("NotificationClosed"),
	); err != nil {
		_ = conn.Close()
		return nil, nil, fmt.Errorf("subscribe NotificationClosed: %w", err)
	}

	// Capability probe, once per dial: purely diagnostic, never fatal. A
	// daemon without the "actions" capability silently ignores the action
	// list — banners appear, clicks do nothing, and without this line
	// nothing says why.
	logActionsCapability(conn, log)

	pumpCtx, cancel := context.WithCancel(context.Background())
	signals := make(chan *dbus.Signal, 16)
	conn.Signal(signals)

	// dispatch travels with the pump instead of living on the struct: a redial
	// would otherwise write the field while the previous pump — cancel() has
	// signalled but not yet stopped it — still reads it to route a signal.
	go s.pumpSignals(pumpCtx, signals, dispatch, log)
	return conn, cancel, nil
}

// logActionsCapability reads the daemon's GetCapabilities and reports whether
// banner clicks can route at all; every failure path is a debug line and a
// return, never a failed send.
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

// dispatchSafely runs the App click callback on its own goroutine with panic
// recovery, mirroring the Wails frontends' policy: the callback reaches the
// X11 activation path, and an unrecovered panic would kill the whole process
// on a banner click (specs/domains/frontend/system-notifications.md).
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
// NotificationResult contract and hands it to the App callback; non-default
// actions (c0wrk registers none) are dropped after consuming the entry.
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
	// Dispatched off the pump (guarded by dispatchSafely): the callback's
	// window activation makes blocking X11 round trips, and a blocked pump
	// means godbus silently discards every later ActionInvoked — one slow
	// activation would permanently kill notification clicks.
	dispatchSafely(dispatch, wailsRuntime.NotificationResult{
		Response: wailsRuntime.NotificationResponse{
			ID:               meta.wailsID,
			ActionIdentifier: notificationDefaultActionIdentifier,
			UserInfo:         meta.userInfo,
		},
	}, log)
}

// handleNotificationClosed reproduces the documented Wails quirk (see
// specs/domains/frontend/system-notifications.md): close reason 2 (user
// dismiss) is delivered as the default action; reasons 1/3/4 are dropped.
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

// prunePendingLocked bounds the routing map (s.mu held): entries older than
// notificationPendingTTL are dropped, then the oldest are evicted until the
// map fits notificationPendingMax — without it, a daemon that signals
// nothing leaks its entries forever.
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

// teardown closes the connection and stops the pump, marking the transport
// permanently closed so an in-flight dial cannot publish a fresh connection
// afterwards (see ensureDial).
func (s *platformNotificationState) teardown() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gen++
	s.teardownLocked()
}

// teardownIfCurrent drops and closes the connection ONLY when it is still the
// one the state holds — a concurrent send may already have redialled, and
// closing its fresh connection would break a healthy transport mid-flight.
func (s *platformNotificationState) teardownIfCurrent(conn dbusDialer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn != conn {
		return
	}
	s.teardownLocked()
}

// teardownLocked drops and closes the connection (s.mu held); no-op when
// never dialed.
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

// iconLocked resolves the app_icon argument (s.mu held): the exported
// file:// URI, or the theme name when the export is impossible.
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
// returns its percent-encoded file:// URI; both filesystem steps are hardened
// (safeio: real directories only, no symlinked final target). Any failure is
// the same fail-soft outcome as an unwritable cache dir — the send falls back
// to the theme icon name (specs/domains/frontend/system-notifications.md).
func exportNotificationIcon() (string, bool) {
	dir, err := notificationIconCacheDir()
	if err != nil {
		return "", false
	}
	path := filepath.Join(dir, notificationIconDirName, notificationIconFileName)
	if err := safeio.MkdirAllReal(filepath.Dir(path), 0o755); err != nil {
		return "", false
	}
	if err := safeio.WriteFile(path, notificationIconPNG(), notificationIconFileMode); err != nil {
		return "", false
	}
	return (&url.URL{Scheme: "file", Path: path}).String(), true
}
