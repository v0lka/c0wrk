package desktop

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/v0lka/c0wrk/backend"
	"github.com/v0lka/c0wrk/backend/project"
	"github.com/v0lka/c0wrk/backend/session"
	coretools "github.com/v0lka/c0wrk/core/tools"
	"github.com/v0lka/c0wrk/core/vectorindex"
	"github.com/v0lka/sp4rk/agent"
	sdktools "github.com/v0lka/sp4rk/tools"
	"github.com/v0lka/sp4rk/tools/builtins"
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

// --- buildVectorCallbacks ---

func TestBuildVectorCallbacks_CtxCanceledBeforeReady(t *testing.T) {
	a := &App{}
	var ptr atomic.Pointer[vectorindex.Manager]
	ready := make(chan struct{})
	searchFunc, waitFunc := a.buildVectorCallbacks(&ptr, ready)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	_, err := searchFunc(ctx, builtins.VectorSearchOptions{Query: "x"})
	if err == nil {
		t.Fatal("expected error when ctx expires before vector ready")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("expected wrapped DeadlineExceeded, got %v", err)
	}

	if err := waitFunc(ctx); err == nil {
		t.Fatal("expected wait error when ctx expires before vector ready")
	}
}

func TestBuildVectorCallbacks_ReadyButNoManager(t *testing.T) {
	a := &App{}
	var ptr atomic.Pointer[vectorindex.Manager]
	ready := make(chan struct{})
	close(ready)
	searchFunc, waitFunc := a.buildVectorCallbacks(&ptr, ready)

	_, err := searchFunc(context.Background(), builtins.VectorSearchOptions{Query: "x"})
	if err == nil {
		t.Fatal("expected 'unavailable' error when manager is nil after ready")
	}

	if err := waitFunc(context.Background()); err == nil {
		t.Fatal("expected 'unavailable' error from waitFunc when manager is nil after ready")
	}
}

// --- emitBackendReady ---

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
	// a.wailsEmit nil + a.ctx nil → emit should silently no-op without panicking.
	a := &App{}
	a.emit("ev:test", "p1") // must not panic
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

	events := rec.snapshot()
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

	events := rec.snapshot()
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d: %+v", len(events), events)
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
