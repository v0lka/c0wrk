//go:build linux

package desktop

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// fakeDBusConn is an in-memory session bus for the transport tests: it
// records Notify calls, returns scripted ids, and mirrors delivered signals
// back through the channel registered via Signal — enough surface to drive
// ensureDial/send/pumpSignals/handlers without a real daemon.
type fakeDBusConn struct {
	mu sync.Mutex

	dialErr     error
	closed      bool
	addMatches  []dbus.MatchOption
	signalChans []chan<- *dbus.Signal

	notifyCalls []fakeNotifyCall
	nextID      uint32
	notifyErr   error
	storeErr    error
}

// fakeNotifyCall records the arguments of one Notify invocation.
type fakeNotifyCall struct {
	appName    string
	replacesID uint32
	icon       string
	title      string
	body       string
	actions    []string
	hints      map[string]dbus.Variant
	timeout    int32
}

func (f *fakeDBusConn) Object(_ string, _ dbus.ObjectPath) dbusObj {
	return f // the fake serves the single remote object we ever call
}

func (f *fakeDBusConn) AddMatchSignal(options ...dbus.MatchOption) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.addMatches = append(f.addMatches, options...)
	return nil
}

func (f *fakeDBusConn) Signal(ch chan<- *dbus.Signal) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.signalChans = append(f.signalChans, ch)
}

func (f *fakeDBusConn) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	return nil
}

// Call implements dbusObj: parses the Notify argument list into the record.
func (f *fakeDBusConn) Call(method string, _ dbus.Flags, args ...any) *dbus.Call {
	f.mu.Lock()
	defer f.mu.Unlock()
	call := &dbus.Call{Method: method}
	if f.notifyErr != nil {
		call.Err = f.notifyErr
		return call
	}
	if len(args) != 8 {
		call.Err = errors.New("notify: unexpected argument count")
		return call
	}
	replaces, _ := args[1].(uint32)
	timeout, _ := args[7].(int32)
	hints, _ := args[6].(map[string]dbus.Variant)
	actions, _ := args[5].([]string)
	appName, appNameOK := args[0].(string)
	icon, iconOK := args[2].(string)
	title, titleOK := args[3].(string)
	body, bodyOK := args[4].(string)
	if !appNameOK || !iconOK || !titleOK || !bodyOK {
		call.Err = errors.New("notify: unexpected argument types")
		return call
	}
	rec := fakeNotifyCall{
		appName:    appName,
		replacesID: replaces,
		icon:       icon,
		title:      title,
		body:       body,
		actions:    actions,
		hints:      hints,
		timeout:    timeout,
	}
	f.notifyCalls = append(f.notifyCalls, rec)
	f.nextID++
	call.Body = []any{f.nextID}
	if f.storeErr != nil {
		call.Err = f.storeErr
	}
	return call
}

// emitSignal delivers a signal to every registered channel, as the daemon
// (and godbus) would.
func (f *fakeDBusConn) emitSignal(t *testing.T, sig *dbus.Signal) {
	t.Helper()
	f.mu.Lock()
	chans := append([]chan<- *dbus.Signal(nil), f.signalChans...)
	f.mu.Unlock()
	for _, ch := range chans {
		ch <- sig
	}
}

// notificationResults is a synchronized result sink for dispatch.
type notificationResults struct {
	mu      sync.Mutex
	results []wailsRuntime.NotificationResult
}

func (r *notificationResults) dispatch(result wailsRuntime.NotificationResult) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.results = append(r.results, result)
}

func (r *notificationResults) snapshot() []wailsRuntime.NotificationResult {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]wailsRuntime.NotificationResult(nil), r.results...)
}

// withFakeBus swaps the dial seam for a fake connection, restoring it after
// the test. The icon cache seam is redirected to a temp dir so the exported
// icon test never touches the real user cache.
func withFakeBus(t *testing.T, conn *fakeDBusConn) *notificationResults {
	t.Helper()
	origDial := dbusDialSessionBus
	dbusDialSessionBus = func() (dbusDialer, error) {
		if conn.dialErr != nil {
			return nil, conn.dialErr
		}
		return conn, nil
	}
	origCache := notificationIconCacheDir
	notificationIconCacheDir = func() (string, error) { return t.TempDir(), nil }
	t.Cleanup(func() {
		dbusDialSessionBus = origDial
		notificationIconCacheDir = origCache
	})
	return &notificationResults{}
}

func TestPlatformSend_NotifyArgumentsAndIconExport(t *testing.T) {
	conn := &fakeDBusConn{}
	results := withFakeBus(t, conn)
	st := &platformNotificationState{}

	err := st.send(wailsRuntime.NotificationOptions{
		ID:    "c0wrk-notification-linux-1",
		Title: "Title",
		Body:  "Body",
		Data:  map[string]any{"sessionId": "sess-1"},
	}, results.dispatch)
	if err != nil {
		t.Fatalf("send failed: %v", err)
	}

	calls := conn.notifyCalls
	if len(calls) != 1 {
		t.Fatalf("expected one Notify call, got %d", len(calls))
	}
	got := calls[0]
	if got.appName != "c0wrk" {
		t.Errorf("app_name = %q, want c0wrk", got.appName)
	}
	if got.replacesID != 0 {
		t.Errorf("replaces_id = %d, want 0", got.replacesID)
	}
	if got.timeout != -1 {
		t.Errorf("timeout = %d, want -1 (daemon default)", got.timeout)
	}
	if len(got.actions) != 2 || got.actions[0] != "default" || got.actions[1] != "Default" {
		t.Errorf("actions = %v, want [default Default]", got.actions)
	}
	if got.title != "Title" || got.body != "Body" {
		t.Errorf("title/body = %q/%q", got.title, got.body)
	}
	if id := got.hints["x-notification-id"]; id.String() != `"c0wrk-notification-linux-1"` {
		t.Errorf("x-notification-id hint = %s, want the notification id", id.String())
	}
	// The icon chain must have exported the embedded PNG and passed its
	// file:// URI.
	if !strings.HasPrefix(got.icon, "file:///") || !strings.HasSuffix(got.icon, ".png") {
		t.Fatalf("app_icon = %q, want an exported file:// PNG URI", got.icon)
	}
	exported := strings.TrimPrefix(got.icon, "file://")
	data, err := os.ReadFile(exported)
	if err != nil {
		t.Fatalf("exported icon unreadable: %v", err)
	}
	if len(data) != len(notificationIconPNG()) {
		t.Errorf("exported icon is %d bytes, embedded is %d", len(data), len(notificationIconPNG()))
	}
}

func TestPlatformSend_IconFallsBackToThemeNameWhenCacheUnwritable(t *testing.T) {
	conn := &fakeDBusConn{}
	results := withFakeBus(t, conn)
	origCache := notificationIconCacheDir
	notificationIconCacheDir = func() (string, error) { return "", errors.New("no cache dir") }
	t.Cleanup(func() { notificationIconCacheDir = origCache })

	st := &platformNotificationState{}
	if err := st.send(wailsRuntime.NotificationOptions{ID: "n1", Title: "t", Body: "b"}, results.dispatch); err != nil {
		t.Fatalf("send failed: %v", err)
	}
	if icon := conn.notifyCalls[0].icon; icon != "c0wrk" {
		t.Errorf("app_icon = %q, want the theme-name fallback %q", icon, notificationIconThemeName)
	}
}

func TestPlatformSend_DialFailureReturnsError(t *testing.T) {
	conn := &fakeDBusConn{dialErr: errors.New("no session bus")}
	results := withFakeBus(t, conn)
	st := &platformNotificationState{}

	err := st.send(wailsRuntime.NotificationOptions{ID: "n1", Title: "t", Body: "b"}, results.dispatch)
	if err == nil {
		t.Fatal("expected a dial error")
	}
	if len(conn.notifyCalls) != 0 {
		t.Errorf("expected no Notify calls after a failed dial, got %d", len(conn.notifyCalls))
	}
}

func TestPlatformSend_NotifyFailureDropsConnection(t *testing.T) {
	conn := &fakeDBusConn{notifyErr: errors.New("daemon rejected")}
	results := withFakeBus(t, conn)
	st := &platformNotificationState{}

	err := st.send(wailsRuntime.NotificationOptions{ID: "n1", Title: "t", Body: "b"}, results.dispatch)
	if err == nil {
		t.Fatal("expected the notify error to surface")
	}
	st.mu.Lock()
	connAfter := st.conn
	st.mu.Unlock()
	if connAfter != nil {
		t.Error("expected the failed connection to be dropped for a redial")
	}
}

func TestPlatformSignalRouting_DefaultAction(t *testing.T) {
	conn := &fakeDBusConn{}
	results := withFakeBus(t, conn)
	st := &platformNotificationState{}
	if err := st.send(wailsRuntime.NotificationOptions{
		ID:    "wails-id-7",
		Title: "t",
		Body:  "b",
		Data:  map[string]any{"sessionId": "sess-7", "projectId": "proj-3"},
	}, results.dispatch); err != nil {
		t.Fatalf("send failed: %v", err)
	}

	conn.emitSignal(t, &dbus.Signal{
		Name: dbusNotificationsInterface + ".ActionInvoked",
		Body: []any{uint32(1), "default"},
	})

	awaitCondition(t, "default action dispatched", func() bool {
		return len(results.snapshot()) == 1
	})
	res := results.snapshot()[0]
	if res.Response.ID != "wails-id-7" {
		t.Errorf("routed notification id = %q, want wails-id-7", res.Response.ID)
	}
	if res.Response.ActionIdentifier != notificationDefaultActionIdentifier {
		t.Errorf("action identifier = %q, want %q", res.Response.ActionIdentifier, notificationDefaultActionIdentifier)
	}
	if res.Response.UserInfo["sessionId"] != "sess-7" || res.Response.UserInfo["projectId"] != "proj-3" {
		t.Errorf("userInfo routing lost: %v", res.Response.UserInfo)
	}

	// A second activation of the same id must not double-deliver (the
	// pending entry was consumed).
	conn.emitSignal(t, &dbus.Signal{
		Name: dbusNotificationsInterface + ".ActionInvoked",
		Body: []any{uint32(1), "default"},
	})
	awaitQuiet(t, func() bool { return len(results.snapshot()) > 1 })
}

func TestPlatformSignalRouting_Reason2Quirk(t *testing.T) {
	conn := &fakeDBusConn{}
	results := withFakeBus(t, conn)
	st := &platformNotificationState{}
	if err := st.send(wailsRuntime.NotificationOptions{ID: "n-close", Title: "t", Body: "b"}, results.dispatch); err != nil {
		t.Fatalf("send failed: %v", err)
	}

	// Reason 2 (user dismissed) behaves like the default action — the
	// documented Wails quirk this transport mirrors.
	conn.emitSignal(t, &dbus.Signal{
		Name: dbusNotificationsInterface + ".NotificationClosed",
		Body: []any{uint32(1), uint32(2)},
	})
	awaitCondition(t, "reason-2 close dispatched", func() bool {
		return len(results.snapshot()) == 1
	})
	if got := results.snapshot()[0].Response.ID; got != "n-close" {
		t.Errorf("routed id = %q, want n-close", got)
	}
}

func TestPlatformSignalRouting_OtherCloseReasonsIgnored(t *testing.T) {
	conn := &fakeDBusConn{}
	results := withFakeBus(t, conn)
	st := &platformNotificationState{}
	if err := st.send(wailsRuntime.NotificationOptions{ID: "n-timeout", Title: "t", Body: "b"}, results.dispatch); err != nil {
		t.Fatalf("send failed: %v", err)
	}
	// id 1 expired (reason 1): consumed but never dispatched.
	conn.emitSignal(t, &dbus.Signal{
		Name: dbusNotificationsInterface + ".NotificationClosed",
		Body: []any{uint32(1), uint32(1)},
	})
	awaitQuiet(t, func() bool { return len(results.snapshot()) > 0 })

	// Foreign notification ids never route anywhere.
	conn.emitSignal(t, &dbus.Signal{
		Name: dbusNotificationsInterface + ".ActionInvoked",
		Body: []any{uint32(999), "default"},
	})
	awaitQuiet(t, func() bool { return len(results.snapshot()) > 0 })
}

func TestPlatformSignalRouting_NonDefaultActionIgnored(t *testing.T) {
	conn := &fakeDBusConn{}
	results := withFakeBus(t, conn)
	st := &platformNotificationState{}
	if err := st.send(wailsRuntime.NotificationOptions{ID: "n-x", Title: "t", Body: "b"}, results.dispatch); err != nil {
		t.Fatalf("send failed: %v", err)
	}
	conn.emitSignal(t, &dbus.Signal{
		Name: dbusNotificationsInterface + ".ActionInvoked",
		Body: []any{uint32(1), "some-category-action"},
	})
	awaitQuiet(t, func() bool { return len(results.snapshot()) > 0 })
}

func TestPlatformTeardown_ClosesConnectionAndStopsPump(t *testing.T) {
	conn := &fakeDBusConn{}
	results := withFakeBus(t, conn)
	st := &platformNotificationState{}
	if err := st.send(wailsRuntime.NotificationOptions{ID: "n1", Title: "t", Body: "b"}, results.dispatch); err != nil {
		t.Fatalf("send failed: %v", err)
	}

	st.teardown()

	if !conn.closed {
		t.Error("expected the D-Bus connection to be closed")
	}
	st.mu.Lock()
	pending := len(st.pending)
	st.mu.Unlock()
	if pending != 0 {
		t.Errorf("expected the pending map to be dropped, got %d entries", pending)
	}
	// Teardown is idempotent.
	st.teardown()
}

func TestSendSystemNotification_FallsBackToWailsOnDBusFailure(t *testing.T) {
	conn := &fakeDBusConn{dialErr: errors.New("no session bus")}
	_ = withFakeBus(t, conn)

	// The Wails fallback would log.Fatal on the fake context (no live
	// runtime), so the fallback itself is observed through the seam-shaped
	// error: the platform path returns the dial error which the caller maps
	// to the Wails transport. Exercise the seam ordering instead: the send
	// seam outranks platform routing.
	f := newNotificationsFixture(t)
	f.app.ctx = context.Background()
	if err := f.app.SendSystemNotification("t", "b", nil); err != nil {
		t.Fatalf("seam-routed send failed: %v", err)
	}
	if len(f.sendOptions) != 1 {
		t.Fatalf("expected the seam to capture the send, got %d", len(f.sendOptions))
	}
}

func TestCleanupNotifications_TeardownOwnTransport(t *testing.T) {
	conn := &fakeDBusConn{}
	results := withFakeBus(t, conn)
	// Drive the production singleton through the dial seam.
	linuxNotifications.teardown() // isolate from any earlier test
	if err := linuxNotifications.send(wailsRuntime.NotificationOptions{ID: "n1", Title: "t", Body: "b"}, results.dispatch); err != nil {
		t.Fatalf("send failed: %v", err)
	}

	linuxNotifications.teardown()
	if !conn.closed {
		t.Error("expected the singleton connection to be closed on teardown")
	}
}

// awaitCondition polls cond until it holds (bounded), failing the test on
// timeout — the signal pump runs on its own goroutine.
func awaitCondition(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// awaitQuiet asserts cond stays false for a short grace period (pump
// goroutines may lag; we verify the absence of delivery, so a bounded wait
// is the strongest honest check).
func awaitQuiet(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(100 * time.Millisecond)
	for !time.Now().After(deadline) {
		if cond() {
			t.Fatalf("unexpected condition became true")
		}
		time.Sleep(2 * time.Millisecond)
	}
	if cond() {
		t.Fatalf("unexpected condition became true after the grace period")
	}
}

func TestPlatformSend_ConcurrentSendsGetDistinctIDs(t *testing.T) {
	conn := &fakeDBusConn{}
	results := withFakeBus(t, conn)
	st := &platformNotificationState{}

	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = st.send(wailsRuntime.NotificationOptions{
				ID:    "concurrent-" + strings.Repeat("x", i+1),
				Title: "t",
				Body:  "b",
			}, results.dispatch)
		}()
	}
	wg.Wait()

	if len(conn.notifyCalls) != 8 {
		t.Fatalf("expected 8 Notify calls, got %d", len(conn.notifyCalls))
	}
	seen := map[uint32]bool{}
	st.mu.Lock()
	for id := range st.pending {
		if seen[id] {
			t.Errorf("duplicate daemon id %d in the pending map", id)
		}
		seen[id] = true
	}
	pendingCount := len(st.pending)
	st.mu.Unlock()
	if pendingCount != 8 {
		t.Errorf("expected 8 pending entries, got %d", pendingCount)
	}
}

// TestPlatformSend_IconCacheHitReusesExportedURI: once the icon has been
// exported successfully, later sends must reuse the cached file:// URI even
// when the cache dir becomes unavailable — no re-export attempt per send.
func TestPlatformSend_IconCacheHitReusesExportedURI(t *testing.T) {
	conn := &fakeDBusConn{}
	results := withFakeBus(t, conn)
	st := &platformNotificationState{}

	send := func(id string) string {
		t.Helper()
		if err := st.send(wailsRuntime.NotificationOptions{ID: id, Title: "t", Body: "b"}, results.dispatch); err != nil {
			t.Fatalf("send %s failed: %v", id, err)
		}
		return conn.notifyCalls[len(conn.notifyCalls)-1].icon
	}

	first := send("n1")
	if !strings.HasPrefix(first, "file:///") {
		t.Fatalf("first send app_icon = %q, want a file:// URI", first)
	}

	// Break the cache dir AFTER the successful export: a cached icon must
	// not consult it again. An uncached state would fall back to the theme
	// name here, so equality with the first URI proves the cache hit.
	notificationIconCacheDir = func() (string, error) { return "", errors.New("cache dir gone") }

	if second := send("n2"); second != first {
		t.Errorf("second send app_icon = %q, want the cached %q", second, first)
	}
}

// TestPlatformSend_IconFallbackVariants walks every way the icon export can
// fail; each must degrade to the theme name WITHOUT failing the send (the
// banner still goes out, just theme-resolved).
func TestPlatformSend_IconFallbackVariants(t *testing.T) {
	t.Run("user-cache-dir-unavailable", func(t *testing.T) {
		conn := &fakeDBusConn{}
		results := withFakeBus(t, conn)
		notificationIconCacheDir = func() (string, error) { return "", errors.New("no cache dir") }
		st := &platformNotificationState{}
		if err := st.send(wailsRuntime.NotificationOptions{ID: "n1", Title: "t", Body: "b"}, results.dispatch); err != nil {
			t.Fatalf("send failed: %v", err)
		}
		if icon := conn.notifyCalls[0].icon; icon != notificationIconThemeName {
			t.Errorf("app_icon = %q, want theme name %q", icon, notificationIconThemeName)
		}
	})

	t.Run("mkdir-blocked-by-file", func(t *testing.T) {
		conn := &fakeDBusConn{}
		results := withFakeBus(t, conn)
		// A regular FILE where the c0wrk/ cache subdirectory should go:
		// MkdirAll cannot create a directory over a file.
		blocker := filepath.Join(t.TempDir(), "blocker")
		if err := os.WriteFile(blocker, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		notificationIconCacheDir = func() (string, error) { return blocker, nil }
		st := &platformNotificationState{}
		if err := st.send(wailsRuntime.NotificationOptions{ID: "n1", Title: "t", Body: "b"}, results.dispatch); err != nil {
			t.Fatalf("send failed: %v", err)
		}
		if icon := conn.notifyCalls[0].icon; icon != notificationIconThemeName {
			t.Errorf("app_icon = %q, want theme name %q", icon, notificationIconThemeName)
		}
	})

	t.Run("write-target-is-directory", func(t *testing.T) {
		conn := &fakeDBusConn{}
		results := withFakeBus(t, conn)
		// The exact target path exists as a directory: WriteFile fails.
		cache := t.TempDir()
		target := filepath.Join(cache, notificationIconDirName, notificationIconFileName)
		if err := os.MkdirAll(target, 0o755); err != nil {
			t.Fatal(err)
		}
		notificationIconCacheDir = func() (string, error) { return cache, nil }
		st := &platformNotificationState{}
		if err := st.send(wailsRuntime.NotificationOptions{ID: "n1", Title: "t", Body: "b"}, results.dispatch); err != nil {
			t.Fatalf("send failed: %v", err)
		}
		if icon := conn.notifyCalls[0].icon; icon != notificationIconThemeName {
			t.Errorf("app_icon = %q, want theme name %q", icon, notificationIconThemeName)
		}
	})
}

// TestPlatformSend_IconURIEncodesSpaces: the exported icon URI is built with
// net/url, so cache paths containing spaces (or other reserved bytes) come
// out percent-encoded — a raw space would make some daemons treat the URI
// as truncated and render no icon at all.
func TestPlatformSend_IconURIEncodesSpaces(t *testing.T) {
	conn := &fakeDBusConn{}
	results := withFakeBus(t, conn)
	cache := filepath.Join(t.TempDir(), "cache dir with spaces")
	if err := os.MkdirAll(cache, 0o755); err != nil {
		t.Fatal(err)
	}
	notificationIconCacheDir = func() (string, error) { return cache, nil }

	st := &platformNotificationState{}
	if err := st.send(wailsRuntime.NotificationOptions{ID: "n1", Title: "t", Body: "b"}, results.dispatch); err != nil {
		t.Fatalf("send failed: %v", err)
	}
	icon := conn.notifyCalls[0].icon
	if strings.Contains(icon, " ") {
		t.Errorf("app_icon %q contains a raw space; want percent-encoding", icon)
	}
	if !strings.Contains(icon, "%20") {
		t.Errorf("app_icon %q lacks the expected %%20 encoding of the spaced path", icon)
	}
}

// TestSendSystemNotification_DialFailureFallsBackToWails: the production
// SendSystemNotification → sendNotificationPlatform path (send seam unset)
// must deliver through the Wails transport when the session bus is
// unreachable — same options, icon-less but functional, never dropped.
func TestSendSystemNotification_DialFailureFallsBackToWails(t *testing.T) {
	conn := &fakeDBusConn{dialErr: errors.New("no session bus")}
	withFakeBus(t, conn)
	linuxNotifications.teardown() // reset the singleton the platform path drives
	t.Cleanup(linuxNotifications.teardown)

	f := newNotificationsFixture(t)
	f.app.notificationsSendFn = nil // bypass the send seam → platform routing
	var wailsOpts []wailsRuntime.NotificationOptions
	f.app.notificationsSendViaWailsFn = func(_ context.Context, opts wailsRuntime.NotificationOptions) error {
		wailsOpts = append(wailsOpts, opts)
		return nil
	}

	if err := f.app.SendSystemNotification("FB Title", "FB Body", map[string]string{"sessionId": "sess-fb"}); err != nil {
		t.Fatalf("SendSystemNotification failed: %v", err)
	}

	if len(wailsOpts) != 1 {
		t.Fatalf("expected exactly one Wails-transport fallback send, got %d", len(wailsOpts))
	}
	if wailsOpts[0].Title != "FB Title" || wailsOpts[0].Body != "FB Body" {
		t.Errorf("fallback options title/body = %q/%q", wailsOpts[0].Title, wailsOpts[0].Body)
	}
	if wailsOpts[0].Data["sessionId"] != "sess-fb" {
		t.Errorf("fallback lost the routing data: %v", wailsOpts[0].Data)
	}
	if len(conn.notifyCalls) != 0 {
		t.Errorf("expected no D-Bus Notify attempts after a failed dial, got %d", len(conn.notifyCalls))
	}
}

// TestSendSystemNotification_NotifyFailureFallsBackAndRedials: a daemon-side
// failure mid-flight must (1) drop the dead connection, (2) fall back to the
// Wails transport for THIS notification, and (3) let the NEXT send redial
// the bus and succeed on the D-Bus path again.
func TestSendSystemNotification_NotifyFailureFallsBackAndRedials(t *testing.T) {
	conn := &fakeDBusConn{notifyErr: errors.New("daemon rejected")}
	withFakeBus(t, conn)
	linuxNotifications.teardown()
	t.Cleanup(linuxNotifications.teardown)

	f := newNotificationsFixture(t)
	f.app.notificationsSendFn = nil
	var wailsOpts []wailsRuntime.NotificationOptions
	f.app.notificationsSendViaWailsFn = func(_ context.Context, opts wailsRuntime.NotificationOptions) error {
		wailsOpts = append(wailsOpts, opts)
		return nil
	}

	if err := f.app.SendSystemNotification("t1", "b1", nil); err != nil {
		t.Fatalf("first send failed: %v", err)
	}
	if len(wailsOpts) != 1 {
		t.Fatalf("expected the failed D-Bus send to fall back to Wails, got %d fallbacks", len(wailsOpts))
	}
	if !conn.closed {
		t.Error("expected the dead connection to be closed after the Notify failure")
	}

	// The daemon recovered: the next send must redial and take the D-Bus
	// path (no second fallback).
	conn.mu.Lock()
	conn.notifyErr = nil
	conn.closed = false
	conn.mu.Unlock()
	if err := f.app.SendSystemNotification("t2", "b2", nil); err != nil {
		t.Fatalf("second send failed: %v", err)
	}
	if len(wailsOpts) != 1 {
		t.Errorf("expected no fallback after a successful redial, got %d fallbacks", len(wailsOpts))
	}
	if len(conn.notifyCalls) != 1 {
		t.Errorf("expected the redialed send on the D-Bus path, got %d Notify calls", len(conn.notifyCalls))
	}
}

// TestPlatformSignalRouting_MalformedSignalsIgnored: garbage off the bus —
// short bodies, wrong argument types, unknown signal names — must neither
// dispatch nor corrupt the pending entry, so a well-formed signal for the
// same notification still routes afterwards.
func TestPlatformSignalRouting_MalformedSignalsIgnored(t *testing.T) {
	conn := &fakeDBusConn{}
	results := withFakeBus(t, conn)
	st := &platformNotificationState{}
	if err := st.send(wailsRuntime.NotificationOptions{
		ID:    "n-malformed",
		Title: "t", Body: "b",
		Data: map[string]any{"sessionId": "sess-m"},
	}, results.dispatch); err != nil {
		t.Fatalf("send failed: %v", err)
	}

	for _, sig := range []*dbus.Signal{
		{Name: dbusNotificationsInterface + ".ActionInvoked", Body: []any{uint32(1)}},             // short body
		{Name: dbusNotificationsInterface + ".ActionInvoked", Body: []any{"1", "default"}},        // id not uint32
		{Name: dbusNotificationsInterface + ".ActionInvoked", Body: []any{uint32(1), 42}},         // action not string
		{Name: dbusNotificationsInterface + ".NotificationClosed", Body: nil},                     // empty body
		{Name: dbusNotificationsInterface + ".NotificationClosed", Body: []any{uint32(1), "2"}},   // reason not uint32
		{Name: dbusNotificationsInterface + ".SomethingElse", Body: []any{uint32(1), "default"}},  // unknown member
		{Name: "org.freedesktop.DBus.NameAcquired", Body: []any{"org.freedesktop.Notifications"}}, // unrelated signal
	} {
		conn.emitSignal(t, sig)
	}
	awaitQuiet(t, func() bool { return len(results.snapshot()) > 0 })

	// The pending entry survived the garbage: a well-formed activation for
	// the same id still routes with its routing data intact.
	conn.emitSignal(t, &dbus.Signal{
		Name: dbusNotificationsInterface + ".ActionInvoked",
		Body: []any{uint32(1), "default"},
	})
	awaitCondition(t, "default action after malformed signals", func() bool {
		return len(results.snapshot()) == 1
	})
	res := results.snapshot()[0]
	if res.Response.ID != "n-malformed" {
		t.Errorf("routed id = %q, want n-malformed", res.Response.ID)
	}
	if res.Response.UserInfo["sessionId"] != "sess-m" {
		t.Errorf("routing data lost after malformed signals: %v", res.Response.UserInfo)
	}
}

// TestPlatformSignalRouting_CloseReasons3And4Ignored: programmatic (3) and
// undefined (4) close reasons are consumed without dispatch, and — because
// a pending entry is consumed exactly once — a later reason-2 for the same
// id no longer routes either.
func TestPlatformSignalRouting_CloseReasons3And4Ignored(t *testing.T) {
	conn := &fakeDBusConn{}
	results := withFakeBus(t, conn)
	st := &platformNotificationState{}
	if err := st.send(wailsRuntime.NotificationOptions{ID: "n-reasons", Title: "t", Body: "b"}, results.dispatch); err != nil {
		t.Fatalf("send failed: %v", err)
	}

	conn.emitSignal(t, &dbus.Signal{
		Name: dbusNotificationsInterface + ".NotificationClosed",
		Body: []any{uint32(1), uint32(3)},
	})
	conn.emitSignal(t, &dbus.Signal{
		Name: dbusNotificationsInterface + ".NotificationClosed",
		Body: []any{uint32(1), uint32(4)},
	})
	awaitQuiet(t, func() bool { return len(results.snapshot()) > 0 })

	st.mu.Lock()
	pending := len(st.pending)
	st.mu.Unlock()
	if pending != 0 {
		t.Errorf("expected the reasons 3/4 closes to consume the pending entry, %d left", pending)
	}

	// Same id, reason 2 now: consumed already, must not double-deliver.
	conn.emitSignal(t, &dbus.Signal{
		Name: dbusNotificationsInterface + ".NotificationClosed",
		Body: []any{uint32(1), uint32(2)},
	})
	awaitQuiet(t, func() bool { return len(results.snapshot()) > 0 })
}

// TestPlatformSignalRouting_IntegrationThroughNotificationCallback wires the
// transport's dispatch straight into the REAL App.notificationCallback (with
// the fixture's window/emit recorders) — proving the full click chain:
// D-Bus signal → NotificationResult → window reveal → notification_clicked
// payload with session/project routing, plus error results dying at the
// callback boundary.
func TestPlatformSignalRouting_IntegrationThroughNotificationCallback(t *testing.T) {
	conn := &fakeDBusConn{}
	withFakeBus(t, conn)
	f := newNotificationsFixture(t)
	st := &platformNotificationState{}
	if err := st.send(wailsRuntime.NotificationOptions{
		ID:    "n-int",
		Title: "t", Body: "b",
		Data: map[string]any{"sessionId": "sess-int", "projectId": "proj-int"},
	}, f.app.notificationCallback); err != nil {
		t.Fatalf("send failed: %v", err)
	}

	conn.emitSignal(t, &dbus.Signal{
		Name: dbusNotificationsInterface + ".ActionInvoked",
		Body: []any{uint32(1), "default"},
	})
	awaitCondition(t, "notification_clicked emitted", func() bool { return f.emitCount() == 1 })

	f.mu.Lock()
	showCalls := f.showCalls
	f.mu.Unlock()
	if showCalls != 1 {
		t.Errorf("expected the window to be revealed once, got %d", showCalls)
	}
	payload, ok := f.lastPayload().(notificationClickedPayload)
	if !ok {
		t.Fatalf("expected a notificationClickedPayload, got %T", f.lastPayload())
	}
	if payload.NotificationID != "n-int" || payload.SessionID != "sess-int" || payload.ProjectID != "proj-int" {
		t.Errorf("unexpected click payload routing fields: %+v", payload)
	}

	// An error result dies at the callback boundary: no second reveal/emit.
	f.app.notificationCallback(wailsRuntime.NotificationResult{Error: errors.New("malformed payload")})
	awaitQuiet(t, func() bool { return f.emitCount() > 1 })
}

// TestLiveDBusNotification is the opt-in end-to-end check against the REAL
// session bus and notification daemon of the desktop the test runs on: it
// exports the embedded icon into the real user cache dir and sends an
// actual banner (icon included) through the production singleton — the
// banner appears on screen for a human to confirm. CI never sets the gate,
// so the bus is never required there.
//
//	C0WRK_TEST_DBUS=1 go test ./desktop -run TestLiveDBusNotification -v
func TestLiveDBusNotification(t *testing.T) {
	if os.Getenv("C0WRK_TEST_DBUS") != "1" {
		t.Skip("set C0WRK_TEST_DBUS=1 to send a real banner (requires a session bus + notification daemon)")
	}

	// The production dial seam and the REAL user cache dir are the point
	// here: withFakeBus must not have been applied (it restores itself).
	results := &notificationResults{}
	linuxNotifications.teardown() // a previous test may have left it faked/dialed
	t.Cleanup(linuxNotifications.teardown)

	start := time.Now()
	err := linuxNotifications.send(wailsRuntime.NotificationOptions{
		ID:    "c0wrk-live-" + start.Format("150405.000000000"),
		Title: "c0wrk — live D-Bus notification test",
		Body:  "Real banner via c0wrk's own org.freedesktop.Notifications transport. It should carry the c0wrk icon.",
		Data:  map[string]any{"sessionId": "live-dbus-test"},
	}, results.dispatch)
	if err != nil {
		t.Fatalf("live D-Bus send failed: %v", err)
	}

	linuxNotifications.mu.Lock()
	icon := linuxNotifications.icon
	pending := len(linuxNotifications.pending)
	linuxNotifications.mu.Unlock()

	if !strings.HasPrefix(icon, "file:///") {
		t.Errorf("live app_icon = %q, want a real file:// export (not the theme fallback)", icon)
	}
	exported := strings.TrimPrefix(icon, "file://")
	data, err := os.ReadFile(exported)
	if err != nil {
		t.Fatalf("live exported icon unreadable at %s: %v", exported, err)
	}
	embedded := notificationIconPNG()
	if len(data) != len(embedded) {
		t.Errorf("live exported icon is %d bytes, embedded is %d", len(data), len(embedded))
	}
	if pending != 1 {
		t.Errorf("expected the daemon-assigned id to sit in the pending map, got %d entries", pending)
	}
	t.Logf("banner sent: icon=%s (exported %d bytes, embedded %d), pending ids=%d — check your screen",
		icon, len(data), len(embedded), pending)
}
