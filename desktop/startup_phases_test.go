package desktop

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
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

// mapPayload returns the single map[string]any argument of a recorded event,
// failing the test on a shape mismatch.
func mapPayload(t *testing.T, ev emittedEvent) map[string]any {
	t.Helper()
	if len(ev.Data) != 1 {
		t.Fatalf("event %q carries %d args, want 1", ev.Name, len(ev.Data))
	}
	m, ok := ev.Data[0].(map[string]any)
	if !ok {
		t.Fatalf("event %q payload type = %T, want map[string]any", ev.Name, ev.Data[0])
	}
	return m
}

// phaseEventsByName filters recorded events by name.
func phaseEventsByName(events []emittedEvent, name string) []emittedEvent {
	var out []emittedEvent
	for _, ev := range events {
		if ev.Name == name {
			out = append(out, ev)
		}
	}
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
		if ev.Name != backend.EventBatchEventName {
			out = append(out, ev)
			continue
		}
		if len(ev.Data) != 1 {
			t.Fatalf("batch event carries %d data args, want 1: %+v", len(ev.Data), ev)
		}
		env, ok := ev.Data[0].(backend.BatchEnvelope)
		if !ok {
			t.Fatalf("batch payload type = %T, want backend.BatchEnvelope", ev.Data[0])
		}
		for _, inner := range env.Events {
			out = append(out, emittedEvent{Name: inner.Name, Data: inner.Args})
		}
	}
	return out
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

	db := backend.InitDatabase(filepath.Join(dir, "test.db"), testLoggerForPhases())
	if db == nil {
		t.Fatal("initDatabase failed")
	}
	defer func() { _ = db.Close() }()
	projStore, _, _ := backend.InitStores(db, testLoggerForPhases())
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

// Every tool_manager:progress event carries the full payload contract: the
// stage, byte counts, the terminal error text, and the stamped splash
// denominator — the webview-subscription race can lose tool_manager:start,
// so no single progress event may be less than self-describing.
func TestEmitToolManagerProgress_CarriesFullPayload(t *testing.T) {
	a := &App{}
	rec := &emitRecorder{}
	a.wailsEmit = rec.emit
	a.toolSplashTotal.Store(3)

	a.emitToolManagerProgress(toolmanager.ToolProgress{
		Tool:       "rg",
		Stage:      toolmanager.StageDownload,
		BytesDone:  50,
		BytesTotal: 100,
	})
	a.emitToolManagerProgress(toolmanager.ToolProgress{
		Tool:   "uv",
		Stage:  toolmanager.StagePythonBootstrap,
		Detail: toolmanager.DetailPackages,
	})
	a.emitToolManagerProgress(toolmanager.ToolProgress{
		Tool:  "markitdown",
		Stage: toolmanager.StageFailed,
		Error: "checksum mismatch",
	})

	events := phaseEventsByName(rec.snapshot(), backend.EventToolManagerProgress)
	if len(events) != 3 {
		t.Fatalf("tool_manager:progress emitted %d time(s), want 3", len(events))
	}
	download := mapPayload(t, events[0])
	if download["tool"] != "rg" || download["stage"] != "download" ||
		download["bytes_done"] != int64(50) || download["bytes_total"] != int64(100) ||
		download["tools_total"] != int64(3) {
		t.Errorf("download payload = %+v, want rg 50/100 with tools_total=3", download)
	}
	bootstrap := mapPayload(t, events[1])
	if bootstrap["stage"] != "python_bootstrap" || bootstrap["detail"] != "packages" {
		t.Errorf("bootstrap payload = %+v, want the packages sub-step", bootstrap)
	}
	if failed := mapPayload(t, events[2]); failed["stage"] != "failed" || failed["error"] != "checksum mismatch" {
		t.Errorf("failed payload = %+v, want the failure cause", failed)
	}
}
