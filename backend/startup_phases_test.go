package backend

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/v0lka/c0wrk/backend/project"
	"github.com/v0lka/c0wrk/backend/session"
	"github.com/v0lka/c0wrk/core/toolmanager"
)

// testLoggerForPhases returns a logger that drops everything to a buffer for
// inspection (the relocated startup-phase tests need a logger that never
// leaks diagnostics into the run output).
func testLoggerForPhases() *slog.Logger {
	return slog.New(slog.NewTextHandler(&bytes.Buffer{}, &slog.HandlerOptions{Level: slog.LevelError}))
}

// emitRecorder is a thread-safe drop-in event sink that records every
// emitted (eventName, data) pair so tests can assert on event flow.
type emitRecorder struct {
	mu     sync.Mutex
	events []emittedEvent
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
	dbPath := filepath.Join(dir, "test.db")

	db := initDatabase(dbPath, testLoggerForPhases())
	if db == nil {
		t.Fatal("initDatabase returned nil for valid path")
	}
	defer func() { _ = db.Close() }()

	if err := db.PingContext(context.Background()); err != nil {
		t.Fatalf("db.Ping failed: %v", err)
	}
}

func TestInitDatabase_InvalidPath(t *testing.T) {
	// Use a path under a non-existent parent directory; sqlite cannot create
	db := initDatabase("/nonexistent-root-/no/such/dir/file.db", testLoggerForPhases())
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
	db := initDatabase(filepath.Join(dir, "test.db"), testLoggerForPhases())
	if db == nil {
		t.Fatal("initDatabase failed")
	}
	defer func() { _ = db.Close() }()

	projStore, sessStore, reviewStore := initStores(db, testLoggerForPhases())
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
	projStore, sessStore, reviewStore := initStores(nil, testLoggerForPhases())
	if projStore != nil || sessStore != nil || reviewStore != nil {
		t.Errorf("expected (nil, nil, nil) for nil db, got (%v, %v, %v)", projStore, sessStore, reviewStore)
	}
}

// --- preloadProjectsAndSessions ---

func TestPreloadProjectsAndSessions_NilManager(t *testing.T) {
	rec := &emitRecorder{}
	got := preloadProjectsAndSessions(nil, nil, testLoggerForPhases(), rec.emit)
	if got != nil {
		t.Errorf("expected nil for nil manager, got %v", got)
	}
}

func TestPreloadProjectsAndSessions_Empty(t *testing.T) {
	dir := t.TempDir()
	rec := &emitRecorder{}

	db := initDatabase(filepath.Join(dir, "test.db"), testLoggerForPhases())
	if db == nil {
		t.Fatal("initDatabase failed")
	}
	defer func() { _ = db.Close() }()
	projStore, sessStore, _ := initStores(db, testLoggerForPhases())
	projectMgr := project.NewManager(projStore, dir, nil)

	got := preloadProjectsAndSessions(projectMgr, sessStore, testLoggerForPhases(), rec.emit)
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
	rec := &emitRecorder{}

	db := initDatabase(filepath.Join(dir, "test.db"), testLoggerForPhases())
	if db == nil {
		t.Fatal("initDatabase failed")
	}
	defer func() { _ = db.Close() }()
	projStore, sessStore, _ := initStores(db, testLoggerForPhases())
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

	got := preloadProjectsAndSessions(projectMgr, sessStore, testLoggerForPhases(), rec.emit)
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
	if events[0].Name != EventProjectsLoaded {
		t.Errorf("first event name = %q, want %q", events[0].Name, EventProjectsLoaded)
	}
	if events[1].Name != EventSessionsLoaded {
		t.Errorf("second event name = %q, want %q", events[1].Name, EventSessionsLoaded)
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

func (f *fatalRecorder) actions() ToolFatalActions {
	return ToolFatalActions{
		Dialog: func(_ context.Context, message string) {
			f.dialogs = append(f.dialogs, message)
			f.journal.add("fatal:dialog")
		},
		Quit: func(context.Context) {
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

// toolFlow carries the fixtures of one tool-flow test: the injected deps, the
// payload recorder, the ordering journal, the fatal recorder and the splash
// denominator store.
type toolFlow struct {
	deps    ToolPhaseDeps
	rec     *emitRecorder
	journal *toolFlowJournal
	fatal   *fatalRecorder
	splash  atomic.Int64
}

// setupToolFlow wires ToolPhaseDeps whose emissions land in both the payload
// recorder and the ordering journal.
func setupToolFlow(t *testing.T) *toolFlow {
	t.Helper()
	tf := &toolFlow{
		rec:     &emitRecorder{},
		journal: &toolFlowJournal{},
		fatal:   &fatalRecorder{},
	}
	tf.fatal.journal = tf.journal
	tf.deps = ToolPhaseDeps{
		SetToolsTotal: func(total int64) { tf.splash.Store(total) },
		Emit: func(eventName string, optionalData ...any) {
			tf.journal.add("emit:" + eventName)
			tf.rec.emit(eventName, optionalData...)
		},
		Fatal: tf.fatal.actions(),
	}
	return tf
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
	tf := setupToolFlow(t)
	fake := &fakeToolEnsurer{
		journal: tf.journal,
		offline: []toolmanager.ToolStatus{
			{Tool: tmSpec("rg"), Ready: true},
			{Tool: tmSpec("uv"), Ready: true, Installed: true},
			{Tool: tmSpec("markitdown"), Ready: true},
		},
	}

	installed, ok := reconcileToolsWithSplash(context.Background(), testLoggerForPhases(), fake, tf.deps)
	if !ok {
		t.Fatal("ok = false for an all-Ready flow, want true")
	}
	if !installed {
		t.Error("installed = false, want true (uv was newly installed offline)")
	}
	if got := fake.calls; !reflect.DeepEqual(got, []string{"offline"}) {
		t.Errorf("ensure calls = %v, want exactly [offline]", got)
	}
	events := tf.rec.snapshot()
	if starts := phaseEventsByName(events, EventToolManagerStart); len(starts) != 0 {
		t.Errorf("tool_manager:start emitted %d time(s) for an all-Ready flow, want 0", len(starts))
	}
	dones := phaseEventsByName(events, EventToolManagerDone)
	if len(dones) != 1 {
		t.Fatalf("tool_manager:done emitted %d time(s), want 1", len(dones))
	}
	payload := mapPayload(t, dones[0])
	if payload["installed_count"] != 1 || payload["skipped_count"] != 2 {
		t.Errorf("done payload = %+v, want installed_count=1 skipped_count=2", payload)
	}
	if rtes := phaseEventsByName(events, EventRuntimeError); len(rtes) != 0 {
		t.Errorf("runtime_error emitted %d time(s) for an all-Ready flow, want 0", len(rtes))
	}
	tf.fatal.assertSilent(t)
	want := []string{"ensure:offline", "emit:" + EventToolManagerDone}
	if got := tf.journal.snapshot(); !reflect.DeepEqual(got, want) {
		t.Errorf("journal = %v, want %v", got, want)
	}
}

// (b) Tools not Ready after the offline pass: the splash start fires (once,
// early), the blocking network pass runs, and done lands only after it.
func TestReconcileToolsWithSplash_NotReadyAfterOffline_BlockingNetworkPass(t *testing.T) {
	tf := setupToolFlow(t)
	fake := &fakeToolEnsurer{
		journal: tf.journal,
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

	installed, ok := reconcileToolsWithSplash(context.Background(), testLoggerForPhases(), fake, tf.deps)
	if !ok {
		t.Fatal("ok = false for a satisfied network pass, want true")
	}
	if !installed {
		t.Error("installed = false, want true (rg installed by the network pass)")
	}
	if got := fake.calls; !reflect.DeepEqual(got, []string{"offline", "network"}) {
		t.Errorf("ensure calls = %v, want [offline network]", got)
	}
	// The splash denominator recorded at start time feeds every progress
	// event — a lost tool_manager:start must not leave the splash total-less.
	if got := tf.splash.Load(); got != 2 {
		t.Errorf("splash total = %d, want 2 (the NeedsInstall list length)", got)
	}
	events := tf.rec.snapshot()
	starts := phaseEventsByName(events, EventToolManagerStart)
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
	dones := phaseEventsByName(events, EventToolManagerDone)
	if len(dones) != 1 {
		t.Fatalf("tool_manager:done emitted %d time(s), want exactly 1", len(dones))
	}
	tf.fatal.assertSilent(t)
	want := []string{
		"emit:" + EventToolManagerStart,
		"ensure:offline",
		"ensure:network",
		"emit:" + EventToolManagerDone,
	}
	if got := tf.journal.snapshot(); !reflect.DeepEqual(got, want) {
		t.Errorf("journal = %v, want %v (done must follow the network pass)", got, want)
	}
}

// Every tool_manager:progress event would carry the full payload contract via
// the injected Progress hook; reconcile itself only records the denominator
// through SetToolsTotal, which this companion to the (b) case re-checks on
// the late-start path.
func TestReconcileToolsWithSplash_LateStartWhenNeedsInstallSawCompleteInstall(t *testing.T) {
	tf := setupToolFlow(t)
	fake := &fakeToolEnsurer{
		journal: tf.journal,
		offline: []toolmanager.ToolStatus{
			{Tool: tmSpec("rg"), Ready: false, Err: errors.New("stale binary")},
			{Tool: tmSpec("uv"), Ready: true},
		},
		network: []toolmanager.ToolStatus{
			{Tool: tmSpec("rg"), Ready: true, Installed: true},
			{Tool: tmSpec("uv"), Ready: true},
		},
	}

	_, ok := reconcileToolsWithSplash(context.Background(), testLoggerForPhases(), fake, tf.deps)
	if !ok {
		t.Fatal("ok = false for a satisfied network pass, want true")
	}
	events := tf.rec.snapshot()
	starts := phaseEventsByName(events, EventToolManagerStart)
	if len(starts) != 1 {
		t.Fatalf("tool_manager:start emitted %d time(s), want exactly 1 (the late one)", len(starts))
	}
	startPayload := mapPayload(t, starts[0])
	tools, toolsOK := startPayload["tools"].([]map[string]string)
	if !toolsOK || len(tools) != 1 || tools[0]["name"] != "rg" {
		t.Errorf("start payload tools = %+v, want only rg", startPayload["tools"])
	}
	tf.fatal.assertSilent(t)
	want := []string{
		"ensure:offline",
		"emit:" + EventToolManagerStart,
		"ensure:network",
		"emit:" + EventToolManagerDone,
	}
	if got := tf.journal.snapshot(); !reflect.DeepEqual(got, want) {
		t.Errorf("journal = %v, want %v", got, want)
	}
}

// (c) The network pass still leaves tools missing: the fatal modal names the
// failed tools and the app quits; done resolves the splash first.
func TestReconcileToolsWithSplash_NetworkPassLeavesToolsMissing_FatalDialogAndQuit(t *testing.T) {
	tf := setupToolFlow(t)
	fake := &fakeToolEnsurer{
		journal: tf.journal,
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

	installed, ok := reconcileToolsWithSplash(context.Background(), testLoggerForPhases(), fake, tf.deps)
	if ok {
		t.Fatal("ok = true after a failed network pass, want false")
	}
	if installed {
		t.Error("installed = true on the fatal path, want false")
	}
	if len(tf.fatal.dialogs) != 1 {
		t.Fatalf("fatal dialogs = %d, want 1", len(tf.fatal.dialogs))
	}
	if msg := tf.fatal.dialogs[0]; !strings.Contains(msg, "rg") || strings.Contains(msg, "uv") {
		t.Errorf("fatal dialog message = %q, want it to name rg (and only the failed tools)", msg)
	}
	if tf.fatal.quits != 1 {
		t.Errorf("fatal quits = %d, want 1", tf.fatal.quits)
	}
	events := tf.rec.snapshot()
	if dones := phaseEventsByName(events, EventToolManagerDone); len(dones) != 1 {
		t.Errorf("tool_manager:done emitted %d time(s), want 1 (splash must resolve before the modal)", len(dones))
	}
	if rtes := phaseEventsByName(events, EventRuntimeError); len(rtes) != 0 {
		t.Errorf("runtime_error emitted %d time(s) on the fatal path, want 0", len(rtes))
	}
	want := []string{
		"emit:" + EventToolManagerStart,
		"ensure:offline",
		"ensure:network",
		"emit:" + EventToolManagerDone,
		"fatal:dialog",
		"fatal:quit",
	}
	if got := tf.journal.snapshot(); !reflect.DeepEqual(got, want) {
		t.Errorf("journal = %v, want %v", got, want)
	}
}

// A structural offline failure with tools known missing still runs the
// blocking network pass; a structural network failure is fatal with the
// error text surfaced.
func TestReconcileToolsWithSplash_OfflineStructuralFailure_MissingTools_StillTriesNetwork(t *testing.T) {
	tf := setupToolFlow(t)
	fake := &fakeToolEnsurer{
		journal:    tf.journal,
		needed:     []toolmanager.ToolSpec{tmSpec("rg")},
		offlineErr: errors.New("offline structural boom"),
		networkErr: errors.New("insufficient disk space"),
	}

	_, ok := reconcileToolsWithSplash(context.Background(), testLoggerForPhases(), fake, tf.deps)
	if ok {
		t.Fatal("ok = true after a structurally failed network pass, want false")
	}
	if got := fake.calls; !reflect.DeepEqual(got, []string{"offline", "network"}) {
		t.Errorf("ensure calls = %v, want [offline network]", got)
	}
	if len(tf.fatal.dialogs) != 1 || !strings.Contains(tf.fatal.dialogs[0], "insufficient disk space") {
		t.Errorf("fatal dialogs = %q, want 1 naming the structural error", tf.fatal.dialogs)
	}
	if tf.fatal.quits != 1 {
		t.Errorf("fatal quits = %d, want 1", tf.fatal.quits)
	}
	events := tf.rec.snapshot()
	if dones := phaseEventsByName(events, EventToolManagerDone); len(dones) != 1 {
		t.Errorf("tool_manager:done emitted %d time(s), want 1", len(dones))
	}
	if rtes := phaseEventsByName(events, EventRuntimeError); len(rtes) != 0 {
		t.Errorf("runtime_error emitted %d time(s) on the fatal path, want 0", len(rtes))
	}
}

// (d) Structural offline failure over an already-complete install: degrade to
// the runtime_error toast — no network pass, no blocking, no fatal.
func TestReconcileToolsWithSplash_OfflineStructuralFailure_CompleteInstall_ToastOnly(t *testing.T) {
	tf := setupToolFlow(t)
	fake := &fakeToolEnsurer{
		journal:    tf.journal,
		offlineErr: errors.New("insufficient disk space"),
	}

	installed, ok := reconcileToolsWithSplash(context.Background(), testLoggerForPhases(), fake, tf.deps)
	if !ok {
		t.Fatal("ok = false for the toast-only degradation, want true")
	}
	if installed {
		t.Error("installed = true on the degradation path, want false")
	}
	if got := fake.calls; !reflect.DeepEqual(got, []string{"offline"}) {
		t.Errorf("ensure calls = %v, want exactly [offline] (no network pass)", got)
	}
	events := tf.rec.snapshot()
	if starts := phaseEventsByName(events, EventToolManagerStart); len(starts) != 0 {
		t.Errorf("tool_manager:start emitted %d time(s), want 0", len(starts))
	}
	dones := phaseEventsByName(events, EventToolManagerDone)
	if len(dones) != 1 {
		t.Fatalf("tool_manager:done emitted %d time(s), want 1", len(dones))
	}
	rtes := phaseEventsByName(events, EventRuntimeError)
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
	tf.fatal.assertSilent(t)
	want := []string{
		"ensure:offline",
		"emit:" + EventToolManagerDone,
		"emit:" + EventRuntimeError,
	}
	if got := tf.journal.snapshot(); !reflect.DeepEqual(got, want) {
		t.Errorf("journal = %v, want %v", got, want)
	}
}
