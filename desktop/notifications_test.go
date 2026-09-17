package desktop

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// notificationsFixture wires an App with the notification test hooks: every
// Wails notification runtime call is replaced by a recording stand-in. The
// real wailsRuntime functions log.Fatal on a context no live runtime owns,
// so exercising InitNotifications / SendSystemNotification / Shutdown without
// these seams is impossible in-process — the same pattern as windowShowFixture.
type notificationsFixture struct {
	app *App

	initCalls        int
	responseCBs      []func(result runtime.NotificationResult)
	authCalls        int
	authGranted      bool
	authErr          error
	authCheckCalls   int
	authCheckGranted bool
	authCheckErr     error
	sendOptions      []runtime.NotificationOptions
	sendErr          error
	cleanupCalls     int
	showCalls        int
	mu               sync.Mutex

	// emittedEvents/emittedPayloads point at the slices appended by the
	// wailsEmit sink (kept behind the same mutex as the rest).
	emittedEvents   *[]string
	emittedPayloads *[]any
}

func newNotificationsFixture(t *testing.T) *notificationsFixture {
	t.Helper()
	f := &notificationsFixture{authGranted: true, authCheckGranted: true}
	f.app = NewApp()
	f.app.ctx = context.Background()
	f.app.notificationsInitFn = func(_ context.Context) error {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.initCalls++
		return nil
	}
	f.app.onNotificationResponseFn = func(_ context.Context, cb func(result runtime.NotificationResult)) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.responseCBs = append(f.responseCBs, cb)
	}
	f.app.notificationsAuthFn = func(_ context.Context) (bool, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.authCalls++
		return f.authGranted, f.authErr
	}
	f.app.notificationsAuthCheckFn = func(_ context.Context) (bool, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.authCheckCalls++
		return f.authCheckGranted, f.authCheckErr
	}
	f.app.notificationsSendFn = func(_ context.Context, options runtime.NotificationOptions) error {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.sendOptions = append(f.sendOptions, options)
		return f.sendErr
	}
	f.app.notificationsCleanupFn = func(_ context.Context) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.cleanupCalls++
	}
	f.app.windowShowFn = func(_ context.Context) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.showCalls++
	}
	var emittedEvents []string
	var emittedPayloads []any
	f.app.wailsEmit = func(eventName string, optionalData ...any) {
		f.mu.Lock()
		defer f.mu.Unlock()
		emittedEvents = append(emittedEvents, eventName)
		emittedPayloads = append(emittedPayloads, optionalData...)
	}
	f.emittedEvents = &emittedEvents
	f.emittedPayloads = &emittedPayloads
	return f
}

// emitCount returns the number of recorded emissions.
func (f *notificationsFixture) emitCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(*f.emittedEvents)
}

// lastPayload returns the last emitted payload (nil when nothing emitted).
func (f *notificationsFixture) lastPayload() any {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(*f.emittedPayloads) == 0 {
		return nil
	}
	return (*f.emittedPayloads)[len(*f.emittedPayloads)-1]
}

// responseCB returns the single registered click callback (nil when none).
func (f *notificationsFixture) responseCB() func(result runtime.NotificationResult) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.responseCBs) == 0 {
		return nil
	}
	return f.responseCBs[0]
}

// TestInitNotifications_RegistersSingleCallback is the idempotence guard: a
// second (or concurrent) InitNotifications call must not register a second
// OnNotificationResponse callback. The Wails callback slot is a single
// package-level variable, and every registration replaces it — a leaked
// second callback would double-deliver clicks after any future re-init.
func TestInitNotifications_RegistersSingleCallback(t *testing.T) {
	f := newNotificationsFixture(t)

	if err := f.app.InitNotifications(); err != nil {
		t.Fatalf("first InitNotifications failed: %v", err)
	}
	if err := f.app.InitNotifications(); err != nil {
		t.Fatalf("second InitNotifications failed: %v", err)
	}
	// Concurrent retries racing the memoized flag.
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = f.app.InitNotifications()
		}()
	}
	wg.Wait()

	if f.initCalls != 1 {
		t.Errorf("expected exactly one InitializeNotifications call, got %d", f.initCalls)
	}
	if len(f.responseCBs) != 1 {
		t.Fatalf("expected exactly one OnNotificationResponse registration, got %d", len(f.responseCBs))
	}
}

// TestInitNotifications_NilContext guards the pre-Startup path: the binding
// is frontend-callable, and an early call must fail with a descriptive error
// instead of log.Fatal-ing inside wailsRuntime on a nil context.
func TestInitNotifications_NilContext(t *testing.T) {
	f := newNotificationsFixture(t)
	f.app.ctx = nil

	err := f.app.InitNotifications()
	if err == nil {
		t.Fatal("expected an error when the application context is not initialized")
	}
	if !strings.Contains(err.Error(), "context is not initialized") {
		t.Errorf("unexpected error message: %v", err)
	}
}

// TestInitNotifications_FailedInitNotMemoized: a failed initialize is not
// memoized — the frontend must be able to retry (a Linux session bus can
// appear after the first attempt) and get a real second attempt.
func TestInitNotifications_FailedInitNotMemoized(t *testing.T) {
	f := newNotificationsFixture(t)
	var calls int
	f.app.notificationsInitFn = func(_ context.Context) error {
		f.mu.Lock()
		defer f.mu.Unlock()
		calls++
		if calls == 1 {
			return context.DeadlineExceeded
		}
		return nil
	}

	if err := f.app.InitNotifications(); err == nil {
		t.Fatal("expected the first init to fail")
	}
	if len(f.responseCBs) != 0 {
		t.Fatalf("expected no callback registration on a failed init, got %d", len(f.responseCBs))
	}
	if err := f.app.InitNotifications(); err != nil {
		t.Fatalf("expected the retry to succeed, got %v", err)
	}
	if len(f.responseCBs) != 1 {
		t.Fatalf("expected one callback registration after the retry, got %d", len(f.responseCBs))
	}
}

// TestNotificationCallback_DefaultActionFocusesAndEmits is the click path:
// a default-action activation must reveal the window FIRST, then emit
// notification_clicked with the routing context extracted from UserInfo.
func TestNotificationCallback_DefaultActionFocusesAndEmits(t *testing.T) {
	f := newNotificationsFixture(t)
	if err := f.app.InitNotifications(); err != nil {
		t.Fatalf("InitNotifications failed: %v", err)
	}
	cb := f.responseCB()
	if cb == nil {
		t.Fatal("expected a registered callback")
	}

	cb(runtime.NotificationResult{
		Response: runtime.NotificationResponse{
			ID:               "c0wrk-notification-1",
			ActionIdentifier: "DEFAULT_ACTION",
			UserInfo: map[string]any{
				"sessionId": "sess-7",
				"projectId": "proj-9",
			},
		},
	})

	if f.showCalls != 1 {
		t.Errorf("expected the window to be revealed once, got %d", f.showCalls)
	}
	if f.emitCount() != 1 {
		t.Fatalf("expected one emitted event, got %d", f.emitCount())
	}
	payload, ok := f.lastPayload().(notificationClickedPayload)
	if !ok {
		t.Fatalf("expected a notificationClickedPayload, got %T", f.lastPayload())
	}
	if payload.NotificationID != "c0wrk-notification-1" || payload.SessionID != "sess-7" || payload.ProjectID != "proj-9" {
		t.Errorf("unexpected payload routing fields: %+v", payload)
	}
	if payload.SessionID != "sess-7" || payload.ProjectID != "proj-9" {
		t.Errorf("expected session/project routing from UserInfo, got %+v", payload)
	}
}

// TestNotificationCallback_IgnoresNonDefaultActions is the dedupe guard:
// category action buttons, plain closes and error results must neither focus
// the window nor navigate. On Linux the NotificationClosed reasons 1/3/4
// (timeout, programmatic, undefined) never fire the callback at all — this
// guards the ones that do arrive with a different identifier.
func TestNotificationCallback_IgnoresNonDefaultActions(t *testing.T) {
	f := newNotificationsFixture(t)
	if err := f.app.InitNotifications(); err != nil {
		t.Fatalf("InitNotifications failed: %v", err)
	}
	cb := f.responseCB()

	cb(runtime.NotificationResult{
		Response: runtime.NotificationResponse{
			ID:               "n1",
			ActionIdentifier: "CATEGORY_ACTION_REPLY",
			UserInfo:         map[string]any{"sessionId": "sess-7"},
		},
	})
	cb(runtime.NotificationResult{
		Response: runtime.NotificationResponse{
			ID:               "n2",
			ActionIdentifier: "",
			UserInfo:         map[string]any{"sessionId": "sess-7"},
		},
	})
	cb(runtime.NotificationResult{
		// Platform error result (e.g. malformed payload on Windows).
		Error: context.DeadlineExceeded,
	})

	if f.showCalls != 0 {
		t.Errorf("expected no window reveal, got %d", f.showCalls)
	}
	if f.emitCount() != 0 {
		t.Errorf("expected no emitted events, got %d", f.emitCount())
	}
}

// TestNotificationCallback_MissingUserInfoFocusesOnly: a click without any
// routing data still focuses the window and emits with empty ids — the
// frontend then treats it as a focus-only no-op (ShowTestNotification's path).
func TestNotificationCallback_MissingUserInfoFocusesOnly(t *testing.T) {
	f := newNotificationsFixture(t)
	if err := f.app.InitNotifications(); err != nil {
		t.Fatalf("InitNotifications failed: %v", err)
	}
	cb := f.responseCB()

	cb(runtime.NotificationResult{
		Response: runtime.NotificationResponse{
			ID:               "n3",
			ActionIdentifier: "DEFAULT_ACTION",
			UserInfo:         nil,
		},
	})

	if f.showCalls != 1 {
		t.Errorf("expected the window to be revealed, got %d", f.showCalls)
	}
	payload, ok := f.lastPayload().(notificationClickedPayload)
	if !ok {
		t.Fatalf("expected a notificationClickedPayload, got %T", f.lastPayload())
	}
	if payload.NotificationID != "n3" || payload.SessionID != "" || payload.ProjectID != "" {
		t.Errorf("expected empty routing ids, got %+v", payload)
	}
}

// TestInitNotifications_DarwinAuthorizationBranch covers the macOS-only
// authorization prompt: granted and denied outcomes both count the init as
// successful (a denial is informational, not an error — the user re-enables
// via OS Settings), and the prompt runs on every platform override for test
// coverage. On production Linux/Windows the branch is skipped entirely.
func TestInitNotifications_DarwinAuthorizationBranch(t *testing.T) {
	orig := notificationAuthorizationPlatform
	defer func() { notificationAuthorizationPlatform = orig }()

	for _, tc := range []struct {
		name    string
		granted bool
		authErr error
	}{
		{name: "granted", granted: true},
		{name: "denied", granted: false},
		{name: "request-failed", granted: false, authErr: context.DeadlineExceeded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newNotificationsFixture(t)
			notificationAuthorizationPlatform = "darwin"
			f.authGranted = tc.granted
			f.authErr = tc.authErr

			if err := f.app.InitNotifications(); err != nil {
				t.Fatalf("InitNotifications failed: %v", err)
			}
			if f.authCalls != 1 {
				t.Errorf("expected one authorization request, got %d", f.authCalls)
			}
			if f.initCalls != 1 || len(f.responseCBs) != 1 {
				t.Errorf("expected one init + one callback despite authorization outcome: init=%d cbs=%d", f.initCalls, len(f.responseCBs))
			}
		})
	}
}

// TestInitNotifications_NonDarwinSkipsAuthorization: on Linux/Windows the
// authorization prompt is a runtime stub — the init path must not round-trip
// it at all.
func TestInitNotifications_NonDarwinSkipsAuthorization(t *testing.T) {
	f := newNotificationsFixture(t)

	if err := f.app.InitNotifications(); err != nil {
		t.Fatalf("InitNotifications failed: %v", err)
	}
	if f.authCalls != 0 {
		t.Errorf("expected no authorization request on non-darwin, got %d", f.authCalls)
	}
}

// TestCheckNotificationAuthorization covers the Settings permission read:
// granted/denied/failed outcomes round-trip the seam, and a nil context
// fails closed with an error (never a silent false the UI would mistake for
// a macOS denial).
func TestCheckNotificationAuthorization(t *testing.T) {
	for _, tc := range []struct {
		name      string
		granted   bool
		checkErr  error
		wantOK    bool
		wantErr   bool
		wantCalls int
	}{
		{name: "granted", granted: true, wantOK: true, wantCalls: 1},
		{name: "denied surfaces false without error", granted: false, wantOK: false, wantCalls: 1},
		{name: "check failed surfaces the error", granted: false, checkErr: context.DeadlineExceeded, wantOK: false, wantErr: true, wantCalls: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newNotificationsFixture(t)
			f.authCheckGranted = tc.granted
			f.authCheckErr = tc.checkErr

			ok, err := f.app.CheckNotificationAuthorization()
			if tc.wantErr != (err != nil) {
				t.Fatalf("error = %v, wantErr %v", err, tc.wantErr)
			}
			if ok != tc.wantOK {
				t.Errorf("granted = %v, want %v", ok, tc.wantOK)
			}
			if f.authCheckCalls != tc.wantCalls {
				t.Errorf("expected %d check calls, got %d", tc.wantCalls, f.authCheckCalls)
			}
			// The read must never trigger the prompting request path.
			if f.authCalls != 0 {
				t.Errorf("expected no authorization REQUEST, got %d", f.authCalls)
			}
		})
	}
}

// TestCheckNotificationAuthorization_NilContext: the binding must not
// log.Fatal on a contextless App (the wailsRuntime call would) — it returns
// an error the frontend logs and treats as granted (fail-open UI: the hint
// must not appear on a transient RPC failure).
func TestCheckNotificationAuthorization_NilContext(t *testing.T) {
	app := NewApp()
	ok, err := app.CheckNotificationAuthorization()
	if ok || err == nil {
		t.Fatalf("expected (false, error) on nil context, got (%v, %v)", ok, err)
	}
}

// TestSendSystemNotification_MapsDataAndRoutesIDs verifies the frontend-
// callable send binding: string data map → NotificationOptions.Data, and
// distinct unique ids across rapid sends.
func TestSendSystemNotification_MapsDataAndRoutesIDs(t *testing.T) {
	f := newNotificationsFixture(t)
	if err := f.app.SendSystemNotification("Title", "Body", map[string]string{
		"sessionId": "sess-1",
		"projectId": "proj-2",
	}); err != nil {
		t.Fatalf("SendSystemNotification failed: %v", err)
	}
	if err := f.app.SendSystemNotification("T2", "B2", nil); err != nil {
		t.Fatalf("SendSystemNotification with nil data failed: %v", err)
	}

	if len(f.sendOptions) != 2 {
		t.Fatalf("expected two sends, got %d", len(f.sendOptions))
	}
	first := f.sendOptions[0]
	if first.Title != "Title" || first.Body != "Body" {
		t.Errorf("unexpected title/body: %+v", first)
	}
	if got := first.Data["sessionId"]; got != "sess-1" {
		t.Errorf("expected sessionId routing data, got %v", got)
	}
	if got := first.Data["projectId"]; got != "proj-2" {
		t.Errorf("expected projectId routing data, got %v", got)
	}
	if len(f.sendOptions[1].Data) != 0 {
		t.Errorf("expected no data map on a nil-data send, got %v", f.sendOptions[1].Data)
	}
	if first.ID == "" || f.sendOptions[1].ID == "" || first.ID == f.sendOptions[1].ID {
		t.Errorf("expected distinct non-empty notification ids: %q vs %q", first.ID, f.sendOptions[1].ID)
	}
}

// TestShowTestNotification_NoRoutingData: the Settings preview sends through
// the same binding with no data map — a click focuses the window and stops
// there (empty session id → frontend logged no-op).
func TestShowTestNotification_NoRoutingData(t *testing.T) {
	f := newNotificationsFixture(t)
	if err := f.app.ShowTestNotification(); err != nil {
		t.Fatalf("ShowTestNotification failed: %v", err)
	}
	if len(f.sendOptions) != 1 {
		t.Fatalf("expected one send, got %d", len(f.sendOptions))
	}
	opt := f.sendOptions[0]
	if len(opt.Data) != 0 {
		t.Errorf("expected no routing data on the preview notification, got %v", opt.Data)
	}
	if opt.Title == "" || opt.Body == "" {
		t.Errorf("expected a non-empty title/body, got %+v", opt)
	}
}

// TestSendSystemNotification_NilContext guards the pre-Startup path.
func TestSendSystemNotification_NilContext(t *testing.T) {
	f := newNotificationsFixture(t)
	f.app.ctx = nil
	err := f.app.SendSystemNotification("t", "b", nil)
	if err == nil {
		t.Fatal("expected an error when the application context is not initialized")
	}
	if !strings.Contains(err.Error(), "context is not initialized") {
		t.Errorf("unexpected error message: %v", err)
	}
}

// TestShutdown_CallsCleanupNotifications verifies the D-Bus teardown hook:
// Shutdown must run cleanupNotifications exactly once. On Linux this closes
// the session-bus connection; skipping it leaks the connection into the exit
// path (and can delay or spam the D-Bus daemon's disconnect logs).
//
// a.ctx is nulled so saveWindowBounds skips its wailsRuntime.WindowGetSize
// call (which log.Fatals on a context no live runtime owns) — the cleanup
// seam receives the Shutdown lifecycle context, exactly as in production.
func TestShutdown_CallsCleanupNotifications(t *testing.T) {
	f := newNotificationsFixture(t)
	f.app.ctx = nil

	f.app.Shutdown(context.Background())

	if f.cleanupCalls != 1 {
		t.Errorf("expected exactly one CleanupNotifications call in Shutdown, got %d", f.cleanupCalls)
	}
}

// TestShutdown_CleanupWithoutInit covers the common path: most runs never
// initialize notifications (toggle off), and Shutdown must still be a plain
// no-op rather than fatal inside the runtime.
func TestShutdown_CleanupWithoutInit(t *testing.T) {
	f := newNotificationsFixture(t)
	f.app.ctx = nil
	f.app.Shutdown(context.Background())
	f.app.Shutdown(context.Background())
	if f.cleanupCalls != 2 {
		t.Errorf("expected cleanup to run on every Shutdown, got %d", f.cleanupCalls)
	}
}

// TestCleanupNotifications_RunsThroughSeamWithLiveContext verifies the
// Shutdown-cleanup path runs the seam when a context exists. (The nil-ctx
// early return cannot be exercised directly: staticcheck SA1012 forbids
// passing a nil context, so the `if ctx == nil` branch is covered by the
// guard's own structure rather than by a call.)
func TestCleanupNotifications_RunsThroughSeamWithLiveContext(t *testing.T) {
	f := newNotificationsFixture(t)
	f.app.cleanupNotifications(context.Background())
	if f.cleanupCalls != 1 {
		t.Errorf("expected exactly one cleanup call through the seam, got %d", f.cleanupCalls)
	}
}
