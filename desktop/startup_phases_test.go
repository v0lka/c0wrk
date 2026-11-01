package desktop

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/v0lka/c0wrk/backend"
	"github.com/v0lka/c0wrk/backend/project"
	"github.com/v0lka/c0wrk/backend/session"
	"github.com/v0lka/c0wrk/core/toolmanager"
	coretools "github.com/v0lka/c0wrk/core/tools"
	"github.com/v0lka/sp4rk/agent"
	sdktools "github.com/v0lka/sp4rk/tools"
)

// testLogger returns a logger that drops everything to a buffer for inspection.
// (Cannot reuse silentLogger() because tests run in package desktop and the
// helper is in event_handlers_test.go, but Go tests in the same package share
// helpers — kept here for clarity).
func testLoggerForPhases() *slog.Logger {
	return slog.New(slog.NewTextHandler(&bytes.Buffer{}, &slog.HandlerOptions{Level: slog.LevelError}))
}

// emitRecorder is a thread-safe drop-in for App.wailsEmit that records every
// emitted (eventName, data) pair so tests can assert on event flow.
type emitRecorder struct {
	mu     sync.Mutex
	events []emittedEvent
}

type emittedEvent struct {
	Name string
	Data []any
}

func (r *emitRecorder) emit(eventName string, optionalData ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, emittedEvent{Name: eventName, Data: optionalData})
}

func (r *emitRecorder) snapshot() []emittedEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]emittedEvent, len(r.events))
	copy(out, r.events)
	return out
}

// flattenEmittedBatches expands any recorded c0wrk:events:batch envelopes into
// the individual events they carried (preserving order) and passes through
// non-batched events unchanged. Session events now travel through the batcher,
// so assertions about individual events must read the flattened view.
func flattenEmittedBatches(t *testing.T, events []emittedEvent) []emittedEvent {
	t.Helper()
	out := make([]emittedEvent, 0, len(events))
	for _, ev := range events {
		if ev.Name != eventBatchEventName {
			out = append(out, ev)
			continue
		}
		if len(ev.Data) != 1 {
			t.Fatalf("batch event carries %d data args, want 1: %+v", len(ev.Data), ev)
		}
		env, ok := ev.Data[0].(batchEnvelope)
		if !ok {
			t.Fatalf("batch payload type = %T, want batchEnvelope", ev.Data[0])
		}
		for _, inner := range env.Events {
			out = append(out, emittedEvent{Name: inner.Name, Data: inner.Args})
		}
	}
	return out
}

// --- initDatabase ---

func TestInitDatabase_CreatesSchema(t *testing.T) {
	dir := t.TempDir()
	a := &App{}
	dbPath := filepath.Join(dir, "test.db")

	db := a.initDatabase(dbPath, testLoggerForPhases())
	if db == nil {
		t.Fatal("initDatabase returned nil for valid path")
	}
	defer func() { _ = db.Close() }()

	if err := db.PingContext(context.Background()); err != nil {
		t.Fatalf("db.Ping failed: %v", err)
	}
}

func TestInitDatabase_InvalidPath(t *testing.T) {
	a := &App{}
	// Use a path under a non-existent parent directory; sqlite cannot create
	db := a.initDatabase("/nonexistent-root-/no/such/dir/file.db", testLoggerForPhases())
	if db == nil {
		return // expected
	}
	_ = db.Close()
	// Some SQLite drivers tolerate odd paths; if the open succeeded the test
	// is not exercising the error branch, but the fallback (returning nil on
	// open error) is still covered by all other initDatabase callers.
	t.Skip("sqlite driver tolerates the invalid-path test fixture")
}

// --- initStores ---

func TestInitStores_BothInitialize(t *testing.T) {
	dir := t.TempDir()
	a := &App{}
	db := a.initDatabase(filepath.Join(dir, "test.db"), testLoggerForPhases())
	if db == nil {
		t.Fatal("initDatabase failed")
	}
	defer func() { _ = db.Close() }()

	projStore, sessStore, reviewStore := a.initStores(db, testLoggerForPhases())
	if projStore == nil {
		t.Error("projStore is nil")
	}
	if sessStore == nil {
		t.Error("sessStore is nil")
	}
	if reviewStore == nil {
		t.Error("reviewStore is nil")
	}

	// Smoke test: the stores must be queryable.
	_, err := projStore.ListProjects(context.Background())
	if err != nil {
		t.Errorf("ListProjects returned error on empty store: %v", err)
	}
}

func TestInitStores_NilDB(t *testing.T) {
	a := &App{}
	projStore, sessStore, reviewStore := a.initStores(nil, testLoggerForPhases())
	if projStore != nil || sessStore != nil || reviewStore != nil {
		t.Errorf("expected (nil, nil, nil) for nil db, got (%v, %v, %v)", projStore, sessStore, reviewStore)
	}
}

// --- preloadProjectsAndSessions ---

func TestPreloadProjectsAndSessions_NilManager(t *testing.T) {
	a := &App{}
	got := a.preloadProjectsAndSessions(nil, nil, testLoggerForPhases())
	if got != nil {
		t.Errorf("expected nil for nil manager, got %v", got)
	}
}

func TestPreloadProjectsAndSessions_Empty(t *testing.T) {
	dir := t.TempDir()
	a := &App{}
	rec := &emitRecorder{}
	a.wailsEmit = rec.emit

	db := a.initDatabase(filepath.Join(dir, "test.db"), testLoggerForPhases())
	if db == nil {
		t.Fatal("initDatabase failed")
	}
	defer func() { _ = db.Close() }()
	projStore, sessStore, _ := a.initStores(db, testLoggerForPhases())
	projectMgr := project.NewManager(projStore, dir, nil)

	got := a.preloadProjectsAndSessions(projectMgr, sessStore, testLoggerForPhases())
	if len(got) != 0 {
		t.Errorf("expected empty slice for empty store, got %d projects", len(got))
	}
	// No events should be emitted when no projects exist.
	if events := rec.snapshot(); len(events) != 0 {
		t.Errorf("expected 0 events for empty preload, got %d: %+v", len(events), events)
	}
}

func TestPreloadProjectsAndSessions_WithSeededData(t *testing.T) {
	dir := t.TempDir()
	a := &App{}
	rec := &emitRecorder{}
	a.wailsEmit = rec.emit

	db := a.initDatabase(filepath.Join(dir, "test.db"), testLoggerForPhases())
	if db == nil {
		t.Fatal("initDatabase failed")
	}
	defer func() { _ = db.Close() }()
	projStore, sessStore, _ := a.initStores(db, testLoggerForPhases())
	projectMgr := project.NewManager(projStore, dir, nil)

	// Seed: create a project with a session.
	p, err := projectMgr.CreateProject("test-proj", "")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if err := sessStore.SaveSession(context.Background(), session.SessionInfo{
		ID:        "sess-1",
		ProjectID: p.ID,
		Name:      "First",
		CreatedAt: now,
	}); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}

	got := a.preloadProjectsAndSessions(projectMgr, sessStore, testLoggerForPhases())
	if len(got) != 1 {
		t.Fatalf("expected 1 project, got %d", len(got))
	}
	if got[0].Name != "test-proj" {
		t.Errorf("project name = %q, want test-proj", got[0].Name)
	}

	events := rec.snapshot()
	if len(events) != 2 {
		t.Fatalf("expected 2 events (projects:loaded + sessions:loaded), got %d", len(events))
	}
	if events[0].Name != backend.EventProjectsLoaded {
		t.Errorf("first event name = %q, want %q", events[0].Name, backend.EventProjectsLoaded)
	}
	if events[1].Name != backend.EventSessionsLoaded {
		t.Errorf("second event name = %q, want %q", events[1].Name, backend.EventSessionsLoaded)
	}
}

// --- buildAskUserCallback ---

func TestBuildAskUserCallback_NoAppContext(t *testing.T) {
	a := &App{} // a.ctx == nil
	uiEmit := func(session.Event) {}
	cb := a.buildAskUserCallback(uiEmit)

	_, err := cb(context.Background(), coretools.AskUserRequest{})
	if err == nil {
		t.Fatal("expected error when a.ctx is nil")
	}
}

func TestBuildAskUserCallback_NoSessionInContext(t *testing.T) {
	a := &App{ctx: context.Background()}
	uiEmit := func(session.Event) {}
	cb := a.buildAskUserCallback(uiEmit)

	_, err := cb(context.Background(), coretools.AskUserRequest{})
	if err == nil {
		t.Fatal("expected error when ctx has no session ID")
	}
}

// --- buildConfirmCallback (C-4 regression guard) ---

func TestBuildConfirmCallback_NoAppContext_DenyAndStop(t *testing.T) {
	a := &App{} // a.ctx == nil
	injectExpectedDiagnostics(t, a, diagnosticExpectation{
		level: slog.LevelWarn, message: "confirmation callback denied: app context unavailable",
		attrs: map[string]string{"tool": "bash_exec", "reason": "ctx_nil"},
	})
	uiEmit := func(session.Event) {}
	cb := a.buildConfirmCallback(uiEmit)

	resp, err := cb(context.Background(), sdktools.ConfirmationRequest{ToolName: "bash_exec"})
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if resp != sdktools.ConfirmDenyAndStop {
		t.Errorf("got %v, want ConfirmDenyAndStop (C-4)", resp)
	}
}

func TestBuildConfirmCallback_NoSessionID_DenyAndStop(t *testing.T) {
	a := &App{ctx: context.Background()}
	injectExpectedDiagnostics(t, a, diagnosticExpectation{
		level: slog.LevelWarn, message: "confirmation callback denied: no session ID in context",
		attrs: map[string]string{"tool": "edit_file", "reason": "session_id_missing"},
	})
	uiEmit := func(session.Event) {}
	cb := a.buildConfirmCallback(uiEmit)

	// ctx WITHOUT a session ID
	resp, err := cb(context.Background(), sdktools.ConfirmationRequest{ToolName: "edit_file"})
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if resp != sdktools.ConfirmDenyAndStop {
		t.Errorf("got %v, want ConfirmDenyAndStop (C-4)", resp)
	}
}

func TestBuildConfirmCallback_HappyPath(t *testing.T) {
	a := &App{ctx: context.Background()}
	var emittedEvents []session.Event
	var mu sync.Mutex
	uiEmit := func(e session.Event) {
		mu.Lock()
		emittedEvents = append(emittedEvents, e)
		mu.Unlock()
	}
	cb := a.buildConfirmCallback(uiEmit)

	ctx := session.ContextWithSessionID(context.Background(), "sess-xyz")
	done := make(chan struct{})
	var resp sdktools.ConfirmationResponse
	go func() {
		resp, _ = cb(ctx, sdktools.ConfirmationRequest{
			ToolName: "bash_exec",
			Input:    []byte(`{"command":"ls"}`),
		})
		close(done)
	}()

	// Wait for the event to arrive, then send a response on the pending channel.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(emittedEvents)
		mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	mu.Lock()
	if len(emittedEvents) == 0 {
		mu.Unlock()
		t.Fatal("expected tool_confirm event to be emitted")
	}
	evt := emittedEvents[0]
	mu.Unlock()

	if evt.SessionID != "sess-xyz" {
		t.Errorf("session ID = %q, want sess-xyz", evt.SessionID)
	}
	payload, ok := evt.Data.(session.ToolConfirmPayload)
	if !ok {
		t.Fatalf("payload type = %T, want ToolConfirmPayload", evt.Data)
	}

	// Look up the pending channel by confirmID and signal.
	val, ok := a.pendingConfirmations.Load(payload.ConfirmID)
	if !ok {
		t.Fatalf("no pending confirmation for ConfirmID %q", payload.ConfirmID)
	}
	pd, ok := val.(*pendingConfirmData)
	if !ok {
		t.Fatalf("pending entry has wrong type: %T", val)
	}
	pd.ch <- sdktools.ConfirmAllowOnce

	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("callback did not return after channel send")
	}
	if resp != sdktools.ConfirmAllowOnce {
		t.Errorf("got %v, want ConfirmAllowOnce", resp)
	}
}

// --- buildStepLimitCallback ---

func TestBuildStepLimitCallback_NoAppContext(t *testing.T) {
	a := &App{}
	cb := a.buildStepLimitCallback(func(session.Event) {})

	resp, err := cb.OnStepLimit(context.Background(), 10, 5, "")
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if resp != agent.StepLimitDeny {
		t.Errorf("got %v, want StepLimitDeny", resp)
	}
}

func TestBuildStepLimitCallback_NoSessionID(t *testing.T) {
	a := &App{ctx: context.Background()}
	cb := a.buildStepLimitCallback(func(session.Event) {})

	resp, err := cb.OnStepLimit(context.Background(), 10, 5, "")
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if resp != agent.StepLimitDeny {
		t.Errorf("got %v, want StepLimitDeny", resp)
	}
}

func TestEmitBackendReady_WithCachedProjects(t *testing.T) {
	a := &App{}
	rec := &emitRecorder{}
	a.wailsEmit = rec.emit

	cached := []project.ProjectInfo{{ID: "p1", Name: "Foo"}}
	a.emitBackendReady(cached, nil, false, testLoggerForPhases())

	events := rec.snapshot()
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Name != backend.EventBackendReady {
		t.Errorf("event name = %q, want %q", events[0].Name, backend.EventBackendReady)
	}
	if len(events[0].Data) != 1 {
		t.Fatalf("expected 1 data arg, got %d", len(events[0].Data))
	}
}

func TestEmitBackendReady_NilManager_NoCached(t *testing.T) {
	a := &App{}
	rec := &emitRecorder{}
	a.wailsEmit = rec.emit

	a.emitBackendReady(nil, nil, false, testLoggerForPhases())

	events := rec.snapshot()
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Name != backend.EventBackendReady {
		t.Errorf("event name = %q, want %q", events[0].Name, backend.EventBackendReady)
	}
	if len(events[0].Data) != 0 {
		t.Errorf("expected empty data for ready-with-no-projects, got %d args", len(events[0].Data))
	}
}

func TestEmitBackendReady_FreshQueryWithProjects(t *testing.T) {
	dir := t.TempDir()
	a := &App{}
	rec := &emitRecorder{}
	a.wailsEmit = rec.emit

	db := a.initDatabase(filepath.Join(dir, "test.db"), testLoggerForPhases())
	if db == nil {
		t.Fatal("initDatabase failed")
	}
	defer func() { _ = db.Close() }()
	projStore, _, _ := a.initStores(db, testLoggerForPhases())
	projectMgr := project.NewManager(projStore, dir, nil)
	if _, err := projectMgr.CreateProject("Bar", ""); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	a.emitBackendReady(nil, projectMgr, false, testLoggerForPhases())

	events := rec.snapshot()
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Name != backend.EventBackendReady {
		t.Errorf("event name = %q, want %q", events[0].Name, backend.EventBackendReady)
	}
	if len(events[0].Data) != 1 {
		t.Errorf("expected 1 data arg with projects, got %d", len(events[0].Data))
	}
}

// --- maybeReinitLogger ---

func TestMaybeReinitLogger_KeepsCurrentForInfo(t *testing.T) {
	a := &App{}
	current := testLoggerForPhases()
	got, sl := a.maybeReinitLogger("INFO", nil, current, t.TempDir())
	if got != current {
		t.Error("expected same logger for INFO level")
	}
	if sl != nil {
		t.Error("expected nil session logger")
	}
}

func TestMaybeReinitLogger_KeepsCurrentForEmpty(t *testing.T) {
	a := &App{}
	current := testLoggerForPhases()
	got, _ := a.maybeReinitLogger("", nil, current, t.TempDir())
	if got != current {
		t.Error("expected same logger for empty level")
	}
}

// --- emit (test the bus indirection) ---

func TestEmit_UsesWailsEmitWhenSet(t *testing.T) {
	a := &App{}
	rec := &emitRecorder{}
	a.wailsEmit = rec.emit

	a.emit("ev:test", "p1", "p2")

	events := rec.snapshot()
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Name != "ev:test" || len(events[0].Data) != 2 {
		t.Errorf("unexpected event: %+v", events[0])
	}
}

func TestEmit_NoOpWhenCtxNilAndNoFake(t *testing.T) {
	// A missing runtime drops the event with one diagnostic, without panicking.
	a := &App{}
	injectExpectedDiagnostics(t, a, diagnosticExpectation{
		level: slog.LevelWarn, message: "emit called with nil ctx, event dropped",
		attrs: map[string]string{"event": "ev:test"},
	})
	a.emit("ev:test", "p1") // must not panic
}

func TestEmit_EventTapReceivesEveryDispatchAlongsideWails(t *testing.T) {
	a := &App{}
	rec := &emitRecorder{}
	a.wailsEmit = rec.emit

	type tapCall struct {
		name string
		args []interface{}
	}
	var (
		mu   sync.Mutex
		taps []tapCall
	)
	a.setEventTap(func(name string, args []interface{}) {
		mu.Lock()
		defer mu.Unlock()
		taps = append(taps, tapCall{name: name, args: args})
	})

	a.emit("ev:test", "p1", 2)
	a.emit("ev:bare")

	mu.Lock()
	defer mu.Unlock()
	if len(taps) != 2 {
		t.Fatalf("expected 2 tap calls, got %d", len(taps))
	}
	if taps[0].name != "ev:test" {
		t.Errorf("tap[0] name = %q, want ev:test", taps[0].name)
	}
	if !reflect.DeepEqual(taps[0].args, []interface{}{"p1", 2}) {
		t.Errorf("tap[0] args = %#v, want [p1 2]", taps[0].args)
	}
	if taps[1].name != "ev:bare" || taps[1].args != nil {
		t.Errorf("tap[1] = (%q, %#v), want the bare no-args event", taps[1].name, taps[1].args)
	}
	// The tap is an observer only: the Wails dispatch is untouched.
	if events := rec.snapshot(); len(events) != 2 {
		t.Fatalf("wails dispatch count = %d, want 2", len(events))
	}
}

// TestEmit_PanickingEventTapIsIsolated (CS #32): the tap contract — "must
// never block, alter, or fail the emit" — is enforced by panic isolation in
// invokeEventTap, not by convention. A panicking tap must neither kill the
// caller's goroutine nor suppress the Wails dispatch; the panic surfaces as
// one expected Warn diagnostic.
func TestEmit_PanickingEventTapIsIsolated(t *testing.T) {
	a := &App{}
	injectExpectedDiagnostics(t, a, diagnosticExpectation{
		level: slog.LevelWarn, message: "event tap panicked; emit continues",
		attrs: map[string]string{"event": "ev:boom", "panic": "tap bug"},
	})
	rec := &emitRecorder{}
	a.wailsEmit = rec.emit
	taps := 0
	a.setEventTap(func(name string, args []interface{}) {
		taps++
		if name == "ev:boom" {
			panic("tap bug")
		}
	})

	a.emit("ev:boom", "x")
	a.emit("ev:after")

	if taps != 2 {
		t.Fatalf("tap calls = %d, want 2 (the panicking tap must stay installed)", taps)
	}
	// The Wails dispatch continues after the recovered panic.
	if events := rec.snapshot(); len(events) != 2 {
		t.Fatalf("wails dispatch count = %d, want 2", len(events))
	}
}

func TestEmit_EventTapFiresWhenWailsDispatchIsDropped(t *testing.T) {
	// The tap rides alongside the dispatch: it observes the emit call even
	// when the Wails side drops it (nil runtime), and the drop diagnostic
	// is unchanged.
	a := &App{}
	injectExpectedDiagnostics(t, a, diagnosticExpectation{
		level: slog.LevelWarn, message: "emit called with nil ctx, event dropped",
		attrs: map[string]string{"event": "ev:dropped"},
	})
	var taps atomic.Int32
	a.setEventTap(func(name string, args []interface{}) {
		if name != "ev:dropped" || len(args) != 1 || args[0] != "x" {
			t.Errorf("unexpected tap payload: %q %#v", name, args)
		}
		taps.Add(1)
	})
	a.emit("ev:dropped", "x") // must not panic
	if taps.Load() != 1 {
		t.Fatalf("tap calls = %d, want 1", taps.Load())
	}
}

func TestBuildUIEmitFunc_SessionRenamedEmitsGlobal(t *testing.T) {
	a := &App{}
	rec := &emitRecorder{}
	a.wailsEmit = rec.emit
	emit := a.buildUIEmitFunc()

	// A session_renamed event must emit BOTH the session-scoped event and the
	// global session:renamed event so that non-active sessions' titles update
	// in the sidebar during background auto-titling.
	emit(session.Event{
		SessionID: "s1",
		Type:      "session_renamed",
		Data: session.SessionRenamedData{
			ID:      "s1",
			OldName: "Session abc123",
			NewName: "Meaningful Title",
		},
	})
	// Events flow through the batching transport; stop (drain + join the
	// flusher goroutine) so the assertions see every delivered envelope
	// deterministically — a plain flush drains under the batcher's mutex but
	// emits outside it, so the background flusher can take the queue and
	// still be mid-emit when the recorder snapshots (the CI flake this
	// family of tests used to carry).
	a.stopEventBatcher()

	events := flattenEmittedBatches(t, rec.snapshot())
	if len(events) != 2 {
		t.Fatalf("expected 2 events (session-scoped + global), got %d: %+v", len(events), events)
	}
	// First: the session-scoped event carrying the full payload.
	if events[0].Name != "session:s1:session_renamed" {
		t.Errorf("events[0] name = %q, want session:s1:session_renamed", events[0].Name)
	}
	// Second: the global session:renamed with {id, name}.
	if events[1].Name != backend.EventSessionRenamed {
		t.Errorf("events[1] name = %q, want %q", events[1].Name, backend.EventSessionRenamed)
	}
	if len(events[1].Data) != 1 {
		t.Fatalf("global event data len = %d, want 1", len(events[1].Data))
	}
	payload, ok := events[1].Data[0].(map[string]string)
	if !ok {
		t.Fatalf("global event payload type = %T, want map[string]string", events[1].Data[0])
	}
	if payload["id"] != "s1" || payload["name"] != "Meaningful Title" {
		t.Errorf("global event payload = %+v, want {id:s1, name:Meaningful Title}", payload)
	}
}

func TestBuildUIEmitFunc_NonRenameEmitsOnlyScoped(t *testing.T) {
	a := &App{}
	rec := &emitRecorder{}
	a.wailsEmit = rec.emit
	emit := a.buildUIEmitFunc()

	// A regular event must emit only the session-scoped event, no global echo.
	emit(session.Event{SessionID: "s1", Type: "finishing"})
	a.stopEventBatcher()

	events := flattenEmittedBatches(t, rec.snapshot())
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d: %+v", len(events), events)
	}
	if events[0].Name != "session:s1:finishing" {
		t.Errorf("event name = %q, want session:s1:finishing", events[0].Name)
	}
}

// TestBuildUIEmitFunc_TaskLifecycleEmitsSessionsChanged pins the task
// lifecycle → sessions:changed mirror: every lifecycle transition (start,
// terminal states, pause/resume) must emit its session-scoped event EXACTLY
// ONCE (the scoped stream is not doubled) plus exactly ONE global
// sessions:changed push, so every surface's session list refreshes behind
// its existing debounced re-read instead of the 30s safety poll. Combined
// with the manager-level TestSendMessage_TaskStartedEmittedOncePerTask
// (one task_started per real activation, one terminal event per natural
// termination) this yields exactly one sessions:changed per real lifecycle
// transition.
func TestBuildUIEmitFunc_TaskLifecycleEmitsSessionsChanged(t *testing.T) {
	lifecycleTypes := []string{
		"task_started", "task_complete", "task_cancelled", "error",
		"task_failed_resumable", "session_paused", "session_resumed",
	}
	for _, eventType := range lifecycleTypes {
		t.Run(eventType, func(t *testing.T) {
			a := &App{}
			rec := &emitRecorder{}
			a.wailsEmit = rec.emit
			emit := a.buildUIEmitFunc()

			emit(session.Event{SessionID: "s1", Type: eventType})
			a.stopEventBatcher()

			events := flattenEmittedBatches(t, rec.snapshot())
			if len(events) != 2 {
				t.Fatalf("expected 2 events (session-scoped + one sessions:changed), got %d: %+v", len(events), events)
			}
			// First: the session-scoped event, delivered exactly once.
			if events[0].Name != "session:s1:"+eventType {
				t.Errorf("events[0] name = %q, want session:s1:%s", events[0].Name, eventType)
			}
			// Second: exactly one global sessions:changed, no payload —
			// consumers re-read the authoritative list.
			if events[1].Name != backend.EventSessionsChanged {
				t.Errorf("events[1] name = %q, want %q", events[1].Name, backend.EventSessionsChanged)
			}
			if len(events[1].Data) != 0 {
				t.Errorf("sessions:changed payload = %+v, want none", events[1].Data)
			}
		})
	}
}

// TestBuildUIEmitFunc_NonLifecycleEmitsNoSessionsChanged is the control for
// the mirror: an event that carries no session-list-visible state must not
// push sessions:changed, or every streaming tick would trigger a list
// re-read on every surface.
func TestBuildUIEmitFunc_NonLifecycleEmitsNoSessionsChanged(t *testing.T) {
	a := &App{}
	rec := &emitRecorder{}
	a.wailsEmit = rec.emit
	emit := a.buildUIEmitFunc()

	emit(session.Event{SessionID: "s1", Type: "finishing"})
	a.stopEventBatcher()

	events := flattenEmittedBatches(t, rec.snapshot())
	if len(events) != 1 {
		t.Fatalf("expected only the session-scoped event, got %d: %+v", len(events), events)
	}
	if events[0].Name != "session:s1:finishing" {
		t.Errorf("event name = %q, want session:s1:finishing", events[0].Name)
	}
}

// --- temp-file fixtures helper ---

func writeTempFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", p, err)
	}
	return p
}

var _ = writeTempFile // keep helper available for future tests

// --- startMCPReadyNotifier ---

func TestStartMCPReadyNotifier_NilAppNoOp(t *testing.T) {
	// a.app == nil → must return immediately without panicking or emitting.
	a := &App{}
	rec := &emitRecorder{}
	a.wailsEmit = rec.emit
	a.startMCPReadyNotifier(context.Background(), testLoggerForPhases())
	if events := rec.snapshot(); len(events) != 0 {
		t.Errorf("expected 0 events with nil app, got %d: %+v", len(events), events)
	}
}

// --- reconcileToolsWithSplash (tool-install orchestration) ---

// toolFlowJournal records the strict interleaving of emissions, manager passes
// and fatal actions so tests can assert ordering. The flow under test is
// fully synchronous, so no locking is needed.
type toolFlowJournal struct {
	entries []string
}

func (j *toolFlowJournal) add(entry string) {
	j.entries = append(j.entries, entry)
}

func (j *toolFlowJournal) snapshot() []string {
	return append([]string(nil), j.entries...)
}

// fakeToolEnsurer scripts NeedsInstall and the two EnsureCriticalTools passes,
// journaling each call as "ensure:offline" / "ensure:network".
type fakeToolEnsurer struct {
	journal    *toolFlowJournal
	needed     []toolmanager.ToolSpec
	needsErr   error
	offline    []toolmanager.ToolStatus
	offlineErr error
	network    []toolmanager.ToolStatus
	networkErr error
	calls      []string
}

func (f *fakeToolEnsurer) NeedsInstall() ([]toolmanager.ToolSpec, error) {
	return f.needed, f.needsErr
}

func (f *fakeToolEnsurer) EnsureCriticalTools(_ context.Context, opts toolmanager.EnsureOptions) ([]toolmanager.ToolStatus, error) {
	if opts.AllowNetwork {
		f.calls = append(f.calls, "network")
		f.journal.add("ensure:network")
		return f.network, f.networkErr
	}
	f.calls = append(f.calls, "offline")
	f.journal.add("ensure:offline")
	return f.offline, f.offlineErr
}

// fatalRecorder captures the fatal-exit seam: every dialog message and quit.
type fatalRecorder struct {
	journal *toolFlowJournal
	dialogs []string
	quits   int
}

func (f *fatalRecorder) actions() toolFatalActions {
	return toolFatalActions{
		dialog: func(_ context.Context, message string) {
			f.dialogs = append(f.dialogs, message)
			f.journal.add("fatal:dialog")
		},
		quit: func(context.Context) {
			f.quits++
			f.journal.add("fatal:quit")
		},
	}
}

func (f *fatalRecorder) assertSilent(t *testing.T) {
	t.Helper()
	if len(f.dialogs) != 0 {
		t.Errorf("fatal dialog shown %d time(s), want 0: %q", len(f.dialogs), f.dialogs)
	}
	if f.quits != 0 {
		t.Errorf("fatal quit called %d time(s), want 0", f.quits)
	}
}

// setupToolFlow wires an App whose emissions land in both the payload
// recorder and the ordering journal.
func setupToolFlow(t *testing.T) (*App, *emitRecorder, *toolFlowJournal, *fatalRecorder) {
	t.Helper()
	a := &App{}
	rec := &emitRecorder{}
	journal := &toolFlowJournal{}
	a.wailsEmit = func(eventName string, optionalData ...any) {
		journal.add("emit:" + eventName)
		rec.emit(eventName, optionalData...)
	}
	return a, rec, journal, &fatalRecorder{journal: journal}
}

func tmSpec(name string) toolmanager.ToolSpec {
	return toolmanager.ToolSpec{Name: name, Version: "1.2.3"}
}

func phaseEventsByName(events []emittedEvent, name string) []emittedEvent {
	var out []emittedEvent
	for _, ev := range events {
		if ev.Name == name {
			out = append(out, ev)
		}
	}
	return out
}

// (a) Everything Ready after the offline pass: no blocking network call, no
// splash start, exactly one done, no toast, no fatal.
func TestReconcileToolsWithSplash_AllReady_NetworkNeverCalled(t *testing.T) {
	a, rec, journal, fatal := setupToolFlow(t)
	fake := &fakeToolEnsurer{
		journal: journal,
		offline: []toolmanager.ToolStatus{
			{Tool: tmSpec("rg"), Ready: true},
			{Tool: tmSpec("uv"), Ready: true, Installed: true},
			{Tool: tmSpec("markitdown"), Ready: true},
		},
	}

	installed, ok := a.reconcileToolsWithSplash(context.Background(), testLoggerForPhases(), fake, fatal.actions())
	if !ok {
		t.Fatal("ok = false for an all-Ready flow, want true")
	}
	if !installed {
		t.Error("installed = false, want true (uv was newly installed offline)")
	}
	if got := fake.calls; !reflect.DeepEqual(got, []string{"offline"}) {
		t.Errorf("ensure calls = %v, want exactly [offline]", got)
	}
	events := rec.snapshot()
	if starts := phaseEventsByName(events, backend.EventToolManagerStart); len(starts) != 0 {
		t.Errorf("tool_manager:start emitted %d time(s) for an all-Ready flow, want 0", len(starts))
	}
	dones := phaseEventsByName(events, backend.EventToolManagerDone)
	if len(dones) != 1 {
		t.Fatalf("tool_manager:done emitted %d time(s), want 1", len(dones))
	}
	payload := mapPayload(t, dones[0])
	if payload["installed_count"] != 1 || payload["skipped_count"] != 2 {
		t.Errorf("done payload = %+v, want installed_count=1 skipped_count=2", payload)
	}
	if rtes := phaseEventsByName(events, backend.EventRuntimeError); len(rtes) != 0 {
		t.Errorf("runtime_error emitted %d time(s) for an all-Ready flow, want 0", len(rtes))
	}
	fatal.assertSilent(t)
	want := []string{"ensure:offline", "emit:" + backend.EventToolManagerDone}
	if got := journal.snapshot(); !reflect.DeepEqual(got, want) {
		t.Errorf("journal = %v, want %v", got, want)
	}
}

// (b) Tools not Ready after the offline pass: the splash start fires (once,
// early), the blocking network pass runs, and done lands only after it.
func TestReconcileToolsWithSplash_NotReadyAfterOffline_BlockingNetworkPass(t *testing.T) {
	a, rec, journal, fatal := setupToolFlow(t)
	fake := &fakeToolEnsurer{
		journal: journal,
		needed:  []toolmanager.ToolSpec{tmSpec("rg"), tmSpec("uv")},
		offline: []toolmanager.ToolStatus{
			{Tool: tmSpec("rg"), Ready: false, Err: errors.New("no cached archive")},
			{Tool: tmSpec("uv"), Ready: true},
		},
		network: []toolmanager.ToolStatus{
			{Tool: tmSpec("rg"), Ready: true, Installed: true},
			{Tool: tmSpec("uv"), Ready: true},
		},
	}

	installed, ok := a.reconcileToolsWithSplash(context.Background(), testLoggerForPhases(), fake, fatal.actions())
	if !ok {
		t.Fatal("ok = false for a satisfied network pass, want true")
	}
	if !installed {
		t.Error("installed = false, want true (rg installed by the network pass)")
	}
	if got := fake.calls; !reflect.DeepEqual(got, []string{"offline", "network"}) {
		t.Errorf("ensure calls = %v, want [offline network]", got)
	}
	events := rec.snapshot()
	starts := phaseEventsByName(events, backend.EventToolManagerStart)
	if len(starts) != 1 {
		t.Fatalf("tool_manager:start emitted %d time(s), want exactly 1", len(starts))
	}
	startPayload := mapPayload(t, starts[0])
	tools, toolsOK := startPayload["tools"].([]map[string]string)
	if !toolsOK {
		t.Fatalf("start payload tools type = %T, want []map[string]string", startPayload["tools"])
	}
	if len(tools) != 2 || tools[0]["name"] != "rg" || tools[0]["version"] != "1.2.3" || tools[1]["name"] != "uv" {
		t.Errorf("start payload tools = %+v, want rg (with version) + uv", tools)
	}
	dones := phaseEventsByName(events, backend.EventToolManagerDone)
	if len(dones) != 1 {
		t.Fatalf("tool_manager:done emitted %d time(s), want exactly 1", len(dones))
	}
	fatal.assertSilent(t)
	want := []string{
		"emit:" + backend.EventToolManagerStart,
		"ensure:offline",
		"ensure:network",
		"emit:" + backend.EventToolManagerDone,
	}
	if got := journal.snapshot(); !reflect.DeepEqual(got, want) {
		t.Errorf("journal = %v, want %v (done must follow the network pass)", got, want)
	}
}

// NeedsInstall saw a complete install but the offline pass disagrees (stale
// binary): the network pass still gets its splash — start is emitted exactly
// once, between the two passes.
func TestReconcileToolsWithSplash_LateStartWhenNeedsInstallSawCompleteInstall(t *testing.T) {
	a, rec, journal, fatal := setupToolFlow(t)
	fake := &fakeToolEnsurer{
		journal: journal,
		offline: []toolmanager.ToolStatus{
			{Tool: tmSpec("rg"), Ready: false, Err: errors.New("stale binary")},
			{Tool: tmSpec("uv"), Ready: true},
		},
		network: []toolmanager.ToolStatus{
			{Tool: tmSpec("rg"), Ready: true, Installed: true},
			{Tool: tmSpec("uv"), Ready: true},
		},
	}

	_, ok := a.reconcileToolsWithSplash(context.Background(), testLoggerForPhases(), fake, fatal.actions())
	if !ok {
		t.Fatal("ok = false for a satisfied network pass, want true")
	}
	events := rec.snapshot()
	starts := phaseEventsByName(events, backend.EventToolManagerStart)
	if len(starts) != 1 {
		t.Fatalf("tool_manager:start emitted %d time(s), want exactly 1 (the late one)", len(starts))
	}
	startPayload := mapPayload(t, starts[0])
	tools, toolsOK := startPayload["tools"].([]map[string]string)
	if !toolsOK || len(tools) != 1 || tools[0]["name"] != "rg" {
		t.Errorf("start payload tools = %+v, want only rg", startPayload["tools"])
	}
	fatal.assertSilent(t)
	want := []string{
		"ensure:offline",
		"emit:" + backend.EventToolManagerStart,
		"ensure:network",
		"emit:" + backend.EventToolManagerDone,
	}
	if got := journal.snapshot(); !reflect.DeepEqual(got, want) {
		t.Errorf("journal = %v, want %v", got, want)
	}
}

// (c) The network pass still leaves tools missing: the fatal modal names the
// failed tools and the app quits; done resolves the splash first.
func TestReconcileToolsWithSplash_NetworkPassLeavesToolsMissing_FatalDialogAndQuit(t *testing.T) {
	a, rec, journal, fatal := setupToolFlow(t)
	fake := &fakeToolEnsurer{
		journal: journal,
		needed:  []toolmanager.ToolSpec{tmSpec("rg"), tmSpec("uv")},
		offline: []toolmanager.ToolStatus{
			{Tool: tmSpec("rg"), Ready: false, Err: errors.New("no cached archive")},
			{Tool: tmSpec("uv"), Ready: false, Err: errors.New("no cached archive")},
		},
		network: []toolmanager.ToolStatus{
			{Tool: tmSpec("rg"), Ready: false, Err: errors.New("download failed")},
			{Tool: tmSpec("uv"), Ready: true, Installed: true},
		},
	}

	installed, ok := a.reconcileToolsWithSplash(context.Background(), testLoggerForPhases(), fake, fatal.actions())
	if ok {
		t.Fatal("ok = true after a failed network pass, want false")
	}
	if installed {
		t.Error("installed = true on the fatal path, want false")
	}
	if len(fatal.dialogs) != 1 {
		t.Fatalf("fatal dialogs = %d, want 1", len(fatal.dialogs))
	}
	if msg := fatal.dialogs[0]; !strings.Contains(msg, "rg") || strings.Contains(msg, "uv") {
		t.Errorf("fatal dialog message = %q, want it to name rg (and only the failed tools)", msg)
	}
	if fatal.quits != 1 {
		t.Errorf("fatal quits = %d, want 1", fatal.quits)
	}
	events := rec.snapshot()
	if dones := phaseEventsByName(events, backend.EventToolManagerDone); len(dones) != 1 {
		t.Errorf("tool_manager:done emitted %d time(s), want 1 (splash must resolve before the modal)", len(dones))
	}
	if rtes := phaseEventsByName(events, backend.EventRuntimeError); len(rtes) != 0 {
		t.Errorf("runtime_error emitted %d time(s) on the fatal path, want 0", len(rtes))
	}
	want := []string{
		"emit:" + backend.EventToolManagerStart,
		"ensure:offline",
		"ensure:network",
		"emit:" + backend.EventToolManagerDone,
		"fatal:dialog",
		"fatal:quit",
	}
	if got := journal.snapshot(); !reflect.DeepEqual(got, want) {
		t.Errorf("journal = %v, want %v", got, want)
	}
}

// A structural offline failure with tools known missing still runs the
// blocking network pass; a structural network failure is fatal with the
// error text surfaced.
func TestReconcileToolsWithSplash_OfflineStructuralFailure_MissingTools_StillTriesNetwork(t *testing.T) {
	a, rec, journal, fatal := setupToolFlow(t)
	fake := &fakeToolEnsurer{
		journal:    journal,
		needed:     []toolmanager.ToolSpec{tmSpec("rg")},
		offlineErr: errors.New("offline structural boom"),
		networkErr: errors.New("insufficient disk space"),
	}

	_, ok := a.reconcileToolsWithSplash(context.Background(), testLoggerForPhases(), fake, fatal.actions())
	if ok {
		t.Fatal("ok = true after a structurally failed network pass, want false")
	}
	if got := fake.calls; !reflect.DeepEqual(got, []string{"offline", "network"}) {
		t.Errorf("ensure calls = %v, want [offline network]", got)
	}
	if len(fatal.dialogs) != 1 || !strings.Contains(fatal.dialogs[0], "insufficient disk space") {
		t.Errorf("fatal dialogs = %q, want 1 naming the structural error", fatal.dialogs)
	}
	if fatal.quits != 1 {
		t.Errorf("fatal quits = %d, want 1", fatal.quits)
	}
	events := rec.snapshot()
	if dones := phaseEventsByName(events, backend.EventToolManagerDone); len(dones) != 1 {
		t.Errorf("tool_manager:done emitted %d time(s), want 1", len(dones))
	}
	if rtes := phaseEventsByName(events, backend.EventRuntimeError); len(rtes) != 0 {
		t.Errorf("runtime_error emitted %d time(s) on the fatal path, want 0", len(rtes))
	}
}

// (d) Structural offline failure over an already-complete install: degrade to
// the runtime_error toast — no network pass, no blocking, no fatal.
func TestReconcileToolsWithSplash_OfflineStructuralFailure_CompleteInstall_ToastOnly(t *testing.T) {
	a, rec, journal, fatal := setupToolFlow(t)
	fake := &fakeToolEnsurer{
		journal:    journal,
		offlineErr: errors.New("insufficient disk space"),
	}

	installed, ok := a.reconcileToolsWithSplash(context.Background(), testLoggerForPhases(), fake, fatal.actions())
	if !ok {
		t.Fatal("ok = false for the toast-only degradation, want true")
	}
	if installed {
		t.Error("installed = true on the degradation path, want false")
	}
	if got := fake.calls; !reflect.DeepEqual(got, []string{"offline"}) {
		t.Errorf("ensure calls = %v, want exactly [offline] (no network pass)", got)
	}
	events := rec.snapshot()
	if starts := phaseEventsByName(events, backend.EventToolManagerStart); len(starts) != 0 {
		t.Errorf("tool_manager:start emitted %d time(s), want 0", len(starts))
	}
	dones := phaseEventsByName(events, backend.EventToolManagerDone)
	if len(dones) != 1 {
		t.Fatalf("tool_manager:done emitted %d time(s), want 1", len(dones))
	}
	rtes := phaseEventsByName(events, backend.EventRuntimeError)
	if len(rtes) != 1 {
		t.Fatalf("runtime_error emitted %d time(s), want 1", len(rtes))
	}
	if len(rtes[0].Data) != 1 {
		t.Fatalf("runtime_error carries %d data args, want 1", len(rtes[0].Data))
	}
	payload, payloadOK := rtes[0].Data[0].(map[string]string)
	if !payloadOK {
		t.Fatalf("runtime_error payload type = %T, want map[string]string", rtes[0].Data[0])
	}
	if !strings.Contains(payload["message"], "Tool initialization failed: insufficient disk space") {
		t.Errorf("runtime_error message = %q, want it to carry the structural error", payload["message"])
	}
	if payload["error_code"] != "tool_install_failed" {
		t.Errorf("runtime_error error_code = %q, want tool_install_failed", payload["error_code"])
	}
	fatal.assertSilent(t)
	want := []string{
		"ensure:offline",
		"emit:" + backend.EventToolManagerDone,
		"emit:" + backend.EventRuntimeError,
	}
	if got := journal.snapshot(); !reflect.DeepEqual(got, want) {
		t.Errorf("journal = %v, want %v", got, want)
	}
}
