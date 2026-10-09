package session

// Regression tests for the C1_backend_session cluster of the mass-bugfix
// review (findings #3, #12, #13, #18, #48, #59, #60, #67, #90, #97, #100,
// #114, #116, #133, #144, #145, #187). Each test names the finding it pins.

import (
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"hash/crc32"
	"image"
	"image/png"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/core"
	goalpkg "github.com/v0lka/c0wrk/core/goal"
	"github.com/v0lka/sp4rk/llm"
	"github.com/v0lka/sp4rk/orchestration"
)

// -----------------------------------------------------------------------------
// #3 — ReplaceStepTodoUpdate must not return a pooled connection with an open
// transaction when the INSERT fails: the deferred ROLLBACK observes the OUTER
// err, so the INSERT failure has to land there (the DELETEs are rolled back
// and no write lock leaks into the pool).
// -----------------------------------------------------------------------------

func TestReplaceStepTodoUpdate_InsertFailureRollsBackAndUnwedgesWriters(t *testing.T) {
	dbPath := filepath.Join(runtimeTempDir(t), "replace-step-tx.db")
	dsn := "file:" + dbPath + "?_pragma=busy_timeout(2000)&_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	// One connection: the follow-up write below is guaranteed to reuse the
	// very connection the failed call poisoned under the old shadowing bug.
	db.SetMaxOpenConns(1)
	defer func() { _ = db.Close() }()
	for _, p := range []string{"PRAGMA journal_mode=WAL", "PRAGMA foreign_keys=ON"} {
		if _, err := db.ExecContext(context.Background(), p); err != nil {
			t.Fatalf("pragma %s: %v", p, err)
		}
	}
	createProjectsTable(t, db)
	insertTestProject(t, db, testProjectID)
	store, err := NewSQLiteSessionStore(db)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}

	sid := "replace-tx-session"
	if err := store.SaveSession(context.Background(), SessionInfo{
		ID: sid, ProjectID: testProjectID, Name: "tx", CreatedAt: time.Now().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("save session: %v", err)
	}

	msg := func(content string) ChatMessage {
		return ChatMessage{
			SessionID: sid, Role: "step_todo_update", Content: content,
			Metadata:  json.RawMessage(`{"step_id":"step_1"}`),
			CreatedAt: time.Now().UTC().Format(time.RFC3339),
		}
	}
	if err := store.ReplaceStepTodoUpdate(context.Background(), sid, "step_1", msg("original checklist")); err != nil {
		t.Fatalf("initial replace: %v", err)
	}

	// Inject an INSERT-only failure: the DELETEs above it still succeed.
	if _, err := db.ExecContext(context.Background(),
		`CREATE TRIGGER fail_step_insert BEFORE INSERT ON session_messages
		 BEGIN SELECT RAISE(ABORT, 'injected insert failure'); END;`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}
	replaceErr := store.ReplaceStepTodoUpdate(context.Background(), sid, "step_1", msg("updated checklist"))
	if _, err := db.ExecContext(context.Background(), `DROP TRIGGER fail_step_insert`); err != nil {
		t.Fatalf("drop trigger: %v", err)
	}
	if replaceErr == nil {
		t.Fatal("expected the injected INSERT failure to surface")
	}

	// The stale row must still be there (the DELETEs rolled back with the
	// aborted transaction).
	loadCtx, loadCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer loadCancel()
	rows, err := db.QueryContext(loadCtx,
		`SELECT content FROM session_messages WHERE session_id = ? AND role = 'step_todo_update'`, sid)
	if err != nil {
		t.Fatalf("query checklist rows: %v", err)
	}
	defer func() { _ = rows.Close() }()
	var contents []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			t.Fatalf("scan: %v", err)
		}
		contents = append(contents, c)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	if len(contents) != 1 || contents[0] != "original checklist" {
		t.Fatalf("stale checklist row not preserved by rollback: %v", contents)
	}

	// The connection must come back to the pool WITHOUT an open write
	// transaction: a fresh write must commit and be visible from an
	// independent reader pool. Under the old bug the INSERT above left
	// BEGIN IMMEDIATE open on the only pooled connection, so this write
	// executed inside that uncommitted transaction and never became visible.
	if err := store.SaveMessage(context.Background(), msg("sentinel")); err != nil {
		t.Fatalf("post-failure save: %v", err)
	}
	reader, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open reader: %v", err)
	}
	defer func() { _ = reader.Close() }()
	var n int
	if err := reader.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM session_messages WHERE session_id = ? AND content = 'sentinel'`, sid).Scan(&n); err != nil {
		t.Fatalf("reader query: %v", err)
	}
	if n != 1 {
		t.Fatalf("post-failure write did not commit (open transaction leaked into the pool): rows=%d", n)
	}
}

// -----------------------------------------------------------------------------
// #12 — emitSessionTokens reads planStepID/retryAttempt via emitEvent; those
// are written under e.mu by SetCurrentStepID/SetRetryAttempt from the
// conductor goroutine while token emissions arrive from subagent goroutines.
// -----------------------------------------------------------------------------

func TestEmitSessionTokens_ConcurrentWithStepScopeWrites(t *testing.T) {
	em := NewEventEmitter("emitter-race-session", func(Event) {})
	const iterations = 400
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < iterations; i++ {
			em.SetCurrentStepID("step_inline")
		}
	}()
	for i := 0; i < iterations; i++ {
		em.EmitSessionTokens(10, 20, "test-model", "test-family")
	}
	<-done
}

// -----------------------------------------------------------------------------
// #13 — m.logLevel is written under m.mu by SetLogLevel (Wails RPC goroutine)
// and must be read under the same lock everywhere.
// -----------------------------------------------------------------------------

func TestLogLevelReadsSynchronizedWithSetLogLevel(t *testing.T) {
	m, _, _ := testManager(t)
	m.SetLogLevel("WARN")
	if got := m.logLevelValue(); got != "WARN" {
		t.Fatalf("logLevelValue = %q, want WARN", got)
	}
	const iterations = 200
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < iterations; i++ {
			m.SetLogLevel("DEBUG")
			m.SetLogLevel("INFO")
		}
	}()
	logger, logFile, err := m.createSessionLogger(testProjectID, "loglevel-race-session")
	if err == nil {
		_ = logger
		if logFile != nil {
			_ = logFile.Close()
		}
	}
	for i := 0; i < iterations; i++ {
		_ = m.logLevelValue()
	}
	<-done
}

// -----------------------------------------------------------------------------
// #18 — pruning must not delete a per-path mutex that is currently held (its
// refcount pins the whole Lock→Unlock window); Unlock must release the exact
// mutex that was acquired.
// -----------------------------------------------------------------------------

func TestFileCoherencePruneKeepsHeldEntry(t *testing.T) {
	tr := NewFileCoherenceTracker(nil)
	const p = "/ws/never-read-before.txt"

	// First write on a path no session ever read: the mutex exists but the
	// path is referenced by neither snapshots nor activity — the exact window
	// in which the old prune deleted held entries.
	tr.Lock(p)
	if _, ok := tr.fileMutexes[p]; !ok {
		t.Fatal("entry missing after Lock")
	}
	// PurgeSession of an unrelated session prunes unreferenced entries.
	tr.PurgeSession("some-other-session")
	if _, ok := tr.fileMutexes[p]; !ok {
		t.Fatal("prune deleted a held per-path mutex")
	}

	// Exclusivity must survive: a second Lock must block until Unlock.
	acquired := make(chan struct{})
	go func() {
		tr.Lock(p)
		close(acquired)
		tr.Unlock(p)
	}()
	// Bounded negative wait (the closeAndWait-bool pattern of
	// background_test.go): within a short budget the second Lock must NOT
	// have proceeded — it must still be blocked by the held mutex.
	if signalWithin(acquired, 50*time.Millisecond) {
		t.Fatal("second Lock on the same path proceeded while the first holder had not unlocked")
	}
	tr.Unlock(p)
	select {
	case <-acquired:
	case <-time.After(2 * time.Second):
		t.Fatal("Unlock did not release the held mutex — waiter blocked forever")
	}

	// After the last Unlock the entry becomes prunable again.
	tr.PurgeSession("some-other-session")
	if _, ok := tr.fileMutexes[p]; ok {
		t.Fatal("unreferenced entry not pruned after release")
	}
}

// -----------------------------------------------------------------------------
// #48 (corrected) — attachImage across a symlinked session images directory:
// a PRE-EXISTING link whose target escapes the agent directory is REFUSED —
// the finding's documented trigger (a planted <sid>/images → <outside>
// symlink) must fail closed instead of redirecting the image write out of
// ~/.c0wrk; a link resolving WITHIN the agent tree still resolves as
// operator intent, and a dangling or swapped-in link is still refused.
// -----------------------------------------------------------------------------

func TestAttachImageRefusesEscapingSymlinkedImagesDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("os.Symlink requires privileges on windows")
	}
	m, _, agentDir := testManager(t)
	info, err := m.CreateSession(testProjectID, testWorkspacePath(t))
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	m.mu.RLock()
	sess := m.sessions[info.ID]
	m.mu.RUnlock()
	if sess == nil {
		t.Fatal("session not in manager")
	}

	src := filepath.Join(runtimeTempDir(t), "src.png")
	if err := writeTinyPNG(src); err != nil {
		t.Fatalf("write source png: %v", err)
	}

	sid := "attach-image-session"
	sessionDir := config.SessionDir(agentDir, testProjectID, sid)
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	imagesDir := filepath.Join(sessionDir, "images")
	external := runtimeTempDir(t)
	if err := os.Symlink(external, imagesDir); err != nil {
		t.Fatalf("plant symlink: %v", err)
	}

	if err := m.attachImage(sess, sid, src, imagesDir, ""); err == nil {
		t.Fatal("expected attachImage to refuse an images dir symlink escaping the agent directory")
	}
	entries, err := os.ReadDir(external)
	if err != nil {
		t.Fatalf("read external dir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("the escaped link target was written to: %v", entries)
	}

	// Control: an in-boundary operator link (resolving inside the agent
	// tree) still resolves as intent.
	inBoundaryTarget := filepath.Join(agentDir, "images-intent")
	if err := os.MkdirAll(inBoundaryTarget, 0o755); err != nil {
		t.Fatalf("mkdir in-boundary target: %v", err)
	}
	intentDir := filepath.Join(sessionDir, "images-intent")
	if err := os.Symlink(inBoundaryTarget, intentDir); err != nil {
		t.Fatalf("plant in-boundary symlink: %v", err)
	}
	if err := m.attachImage(sess, sid, src, intentDir, ""); err != nil {
		t.Fatalf("expected attachImage to resolve an in-boundary symlinked images dir, got: %v", err)
	}
	images, err := os.ReadDir(inBoundaryTarget)
	if err != nil || len(images) != 1 {
		t.Fatalf("expected exactly one persisted image inside the in-boundary target, got %v (err=%v)", images, err)
	}

	// Control: a real directory works.
	realDir := filepath.Join(sessionDir, "images-real")
	if err := m.attachImage(sess, sid, src, realDir, ""); err != nil {
		t.Fatalf("attachImage into a real directory failed: %v", err)
	}
	images, err = os.ReadDir(realDir)
	if err != nil || len(images) != 1 {
		t.Fatalf("expected exactly one persisted image, got %v (err=%v)", images, err)
	}
}

func writeTinyPNG(path string) error {
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	return png.Encode(f, img)
}

// -----------------------------------------------------------------------------
// #59 (corrected) — session log/dump paths are fixed paths under ~/.c0wrk:
// a PRE-EXISTING symlinked logs/dumps/steps DIRECTORY whose target escapes
// the agent tree is REFUSED (the finding's documented plant-at-<sid>/logs
// trigger must fail closed); a link resolving WITHIN the agent tree still
// resolves as operator intent, and a symlink AT a dump FILE is still refused
// by the no-follow open.
// -----------------------------------------------------------------------------

func TestCreateSessionLoggerRefusesEscapingSymlinkedLogDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("os.Symlink requires privileges on windows")
	}
	m, _, agentDir := testManager(t)
	sid := "logger-symlink-session"
	sessionDir := config.SessionDir(agentDir, testProjectID, sid)
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	target := runtimeTempDir(t)
	if err := os.Symlink(target, config.SessionLogsDir(agentDir, testProjectID, sid)); err != nil {
		t.Fatalf("plant symlink: %v", err)
	}
	logger, logFile, err := m.createSessionLogger(testProjectID, sid)
	if err == nil {
		t.Fatal("expected createSessionLogger to refuse a logs dir symlink escaping the agent directory")
	}
	if logFile != nil {
		_ = logFile.Close()
	}
	entries, err := os.ReadDir(target)
	if err != nil {
		t.Fatalf("read escaped target: %v", err)
	}
	if logger != nil || len(entries) != 0 {
		t.Fatal("the escaped link target must stay untouched")
	}

	// Control: an in-boundary operator link still resolves as intent.
	inBoundary := filepath.Join(sessionDir, "logs-intent")
	if err := os.MkdirAll(inBoundary, 0o755); err != nil {
		t.Fatalf("mkdir in-boundary target: %v", err)
	}
	sessionDir2 := config.SessionDir(agentDir, testProjectID, sid+"-2")
	if err := os.MkdirAll(sessionDir2, 0o755); err != nil {
		t.Fatalf("mkdir second session dir: %v", err)
	}
	if err := os.Symlink(inBoundary, config.SessionLogsDir(agentDir, testProjectID, sid+"-2")); err != nil {
		t.Fatalf("plant in-boundary symlink: %v", err)
	}
	logger2, logFile2, err := m.createSessionLogger(testProjectID, sid+"-2")
	if err != nil {
		t.Fatalf("expected createSessionLogger to resolve an in-boundary symlinked logs dir, got: %v", err)
	}
	if logFile2 != nil {
		_ = logFile2.Close()
	}
	if logger2 == nil {
		t.Fatal("expected a logger for the in-boundary link")
	}
	entries2, err := os.ReadDir(inBoundary)
	if err != nil || len(entries2) == 0 {
		t.Fatalf("expected the session log inside the in-boundary target (err=%v)", err)
	}
}

func TestOpenDumpArtifactsSymlinkContract(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("os.Symlink requires privileges on windows")
	}
	m, _, agentDir := testManager(t)
	sid := "dump-symlink-session"
	sessionDir := config.SessionDir(agentDir, testProjectID, sid)
	dumpsDir := filepath.Join(sessionDir, "dumps")
	capture := newCaptureLogger()
	m.SetLogger(capture.logger())

	// 1. Symlinked dumps directory escaping the agent tree → REFUSED; the
	// dump artifact stays out of the link's target.
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	dumpsTarget := runtimeTempDir(t)
	if err := os.Symlink(dumpsTarget, dumpsDir); err != nil {
		t.Fatalf("plant dumps symlink: %v", err)
	}
	dumpFile, tracker := m.openDumpArtifacts(testProjectID, sid)
	if dumpFile != nil || tracker != nil {
		t.Fatal("expected the escaping dumps symlink to be refused")
	}
	if !capture.contains("failed to create dumps directory") {
		t.Fatalf("expected a 'failed to create dumps directory' warning, got: %s", capture.snapshot())
	}
	dumpsEntries, err := os.ReadDir(dumpsTarget)
	if err != nil || len(dumpsEntries) != 0 {
		t.Fatalf("the escaped dumps target was written to (err=%v)", err)
	}
	if err := os.Remove(dumpsDir); err != nil {
		t.Fatalf("remove dumps symlink: %v", err)
	}
	capture.reset()

	// 2. Symlink at the dump file itself → refused with a warning.
	if err := os.MkdirAll(dumpsDir, 0o755); err != nil {
		t.Fatalf("mkdir dumps: %v", err)
	}
	if err := os.Symlink(filepath.Join(runtimeTempDir(t), "victim"), config.SessionDumpPath(agentDir, testProjectID, sid)); err != nil {
		t.Fatalf("plant dump-file symlink: %v", err)
	}
	dumpFile, tracker = m.openDumpArtifacts(testProjectID, sid)
	if dumpFile != nil || tracker != nil {
		t.Fatal("dump file opened through a symlink")
	}
	if !capture.contains("failed to create LLM dump file") {
		t.Fatalf("expected a 'failed to create LLM dump file' warning, got: %s", capture.snapshot())
	}
	if err := os.Remove(config.SessionDumpPath(agentDir, testProjectID, sid)); err != nil {
		t.Fatalf("remove dump symlink: %v", err)
	}
	capture.reset()

	// 3. Symlinked steps subdirectory escaping the agent tree → REFUSED;
	// the step dump tracker is disabled instead of operating outside
	// ~/.c0wrk.
	stepsTarget := runtimeTempDir(t)
	if err := os.Symlink(stepsTarget, config.SessionStepDumpDir(agentDir, testProjectID, sid)); err != nil {
		t.Fatalf("plant steps symlink: %v", err)
	}
	dumpFile, tracker = m.openDumpArtifacts(testProjectID, sid)
	if tracker != nil {
		t.Fatal("expected the escaping steps symlink to be refused")
	}
	if !capture.contains("failed to create step dump directory") {
		t.Fatalf("expected a 'failed to create step dump directory' warning, got: %s", capture.snapshot())
	}
	// The dump FILE itself still exists from phase 2's real dumps dir; only
	// the tracker (steps dir) is disabled.
	if dumpFile == nil {
		t.Fatal("expected the dump file to survive a refused steps directory")
	}
	_ = dumpFile.Close()
	if fi, statErr := os.Stat(stepsTarget); statErr != nil || !fi.IsDir() {
		t.Fatalf("escaped steps target is not a directory: %v", statErr)
	}
	entries3, err := os.ReadDir(stepsTarget)
	if err != nil || len(entries3) != 0 {
		t.Fatalf("the escaped steps target was written to (err=%v)", err)
	}
}

// captureLogger collects slog output so deliberately-expected warnings are
// asserted at their source instead of polluting the test log.
type captureLogger struct {
	mu  sync.Mutex
	buf strings.Builder
}

func newCaptureLogger() *captureLogger { return &captureLogger{} }

func (c *captureLogger) logger() *slog.Logger {
	return slog.New(slog.NewTextHandler(&capWriter{c}, nil))
}

func (c *captureLogger) contains(substr string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return strings.Contains(c.buf.String(), substr)
}

func (c *captureLogger) snapshot() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.String()
}

func (c *captureLogger) reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.buf.Reset()
}

type capWriter struct{ c *captureLogger }

func (w *capWriter) Write(p []byte) (int, error) {
	w.c.mu.Lock()
	defer w.c.mu.Unlock()
	return w.c.buf.Write(p)
}

// -----------------------------------------------------------------------------
// #60 — image decoding must be guarded BEFORE the full decode: a header-
// declared oversized allocation and an over-cap file must fail with errors,
// not by allocating gigabytes.
// -----------------------------------------------------------------------------

func TestProcessImageRejectsHugeDeclaredDimensions(t *testing.T) {
	// Minimal PNG whose IHDR declares 65535×65535 — the pixel buffer is
	// allocated from the header before any pixel data is read.
	var buf strings.Builder
	buf.WriteString(string([]byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}))
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:4], 65535)
	binary.BigEndian.PutUint32(ihdr[4:8], 65535)
	ihdr[8] = 8 // bit depth
	ihdr[9] = 2 // color type: RGB
	appendChunk(&buf, "IHDR", ihdr)
	path := filepath.Join(runtimeTempDir(t), "huge-declared.png")
	if err := os.WriteFile(path, []byte(buf.String()), 0o644); err != nil {
		t.Fatalf("write crafted png: %v", err)
	}

	_, _, _, _, err := processImage(path)
	if err == nil {
		t.Fatal("processImage accepted a header-declared 65535x65535 image")
	}
	if !strings.Contains(err.Error(), "pixel limit") {
		t.Fatalf("expected the pre-decode dimension rejection, got: %v", err)
	}
}

func appendChunk(buf *strings.Builder, typ string, data []byte) {
	length := make([]byte, 4)
	binary.BigEndian.PutUint32(length, uint32(len(data)))
	buf.WriteString(string(length))
	buf.WriteString(typ)
	buf.WriteString(string(data))
	crc := crc32.ChecksumIEEE(append([]byte(typ), data...))
	crcBytes := make([]byte, 4)
	binary.BigEndian.PutUint32(crcBytes, crc)
	buf.WriteString(string(crcBytes))
}

func TestReadImageFileCappedRejectsOversizedFile(t *testing.T) {
	path := filepath.Join(runtimeTempDir(t), "big.bin")
	if err := os.WriteFile(path, []byte(strings.Repeat("x", 2048)), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if _, err := readImageFileCapped(path, 1024); err == nil {
		t.Fatal("readImageFileCapped accepted a file larger than its limit")
	}
	data, err := readImageFileCapped(path, 4096)
	if err != nil || len(data) != 2048 {
		t.Fatalf("within-limit read failed: len=%d err=%v", len(data), err)
	}
}

// -----------------------------------------------------------------------------
// #67 — the per-step dump tracker is a session-layer resource: DeleteSession
// and Shutdown must CloseAll it (the orchestrator's Cleanup deliberately does
// not).
// -----------------------------------------------------------------------------

func TestDeleteSessionClosesStepDumpTracker(t *testing.T) {
	var captured *orchestration.StepDumpTracker
	m := NewManager(
		func(_ core.Emitter, _ *slog.Logger, _ string, _ core.BlackboardFactory, _ io.Writer, sdt *orchestration.StepDumpTracker) (*core.Orchestrator, error) {
			captured = sdt
			return nil, nil
		},
		func(Event) {}, runtimeTempDir(t))
	t.Cleanup(m.Shutdown)

	info, err := m.CreateSession(testProjectID, testWorkspacePath(t))
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if captured == nil {
		t.Fatal("factory did not receive a step dump tracker (DEBUG is the default level)")
	}
	m.mu.RLock()
	sess := m.sessions[info.ID]
	m.mu.RUnlock()
	if sess == nil || sess.stepDumpTracker != captured {
		t.Fatal("session does not own the tracker the factory received")
	}

	if w := captured.OpenStepDump("step_1"); w == nil {
		t.Fatal("OpenStepDump failed before deletion")
	}

	if err := m.DeleteSession(info.ID); err != nil {
		t.Fatalf("delete session: %v", err)
	}
	if w := captured.OpenStepDump("step_2"); w != nil {
		t.Fatal("step dump tracker still open after DeleteSession — one *os.File leaks per executed step")
	}
}

// -----------------------------------------------------------------------------
// #90 / #97 — read-only display RPCs must resolve the session MEMORY-ONLY;
// a store-only session must be served from the store without the lazy restore
// (no ghost session inserted into m.sessions).
// -----------------------------------------------------------------------------

func newBlackboardFixtureManager(t *testing.T) (mgr *Manager, sid string) {
	store, sid, cleanup := setupTestStoreWithSession(t)
	t.Cleanup(cleanup)
	mgr, _, _ = testManager(t)
	mgr.SetSessionStore(store)
	mgr.SetTaskStore(store)
	mgr.SetProjectResolver(func(string) (string, error) { return runtimeTempDir(t), nil })
	return mgr, sid
}

func TestGetBlackboardStateDoesNotRestoreStoreOnlySession(t *testing.T) {
	m, sid := newBlackboardFixtureManager(t)
	adapter := NewTaskStoreAdapter(m.taskStore)
	if err := adapter.PersistNewTask("task-bb-state", sid, "original request"); err != nil {
		t.Fatalf("persist task: %v", err)
	}

	state, err := m.GetBlackboardState(sid)
	if err != nil {
		t.Fatalf("GetBlackboardState: %v", err)
	}
	if state == nil || state.TaskState == nil || state.TaskState.TaskID != "task-bb-state" {
		t.Fatalf("expected the latest task via the store fallback, got %+v", state)
	}
	if len(m.sessions) != 0 {
		t.Fatalf("GetBlackboardState lazily restored a store-only session (ghost): %d live", len(m.sessions))
	}

	// A session with no tasks at all degrades to (nil, nil), not an error.
	empty, err := m.GetBlackboardState("no-such-session")
	if err != nil || empty != nil {
		t.Fatalf("unknown session: state=%v err=%v", empty, err)
	}
	if len(m.sessions) != 0 {
		t.Fatal("unknown session id resurrected state")
	}
}

func TestGetSessionAttachmentsMemoryOnly(t *testing.T) {
	m, sid := newBlackboardFixtureManager(t)

	got, err := m.GetSessionAttachments(sid)
	if err != nil {
		t.Fatalf("GetSessionAttachments: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected no staged attachments for a non-resident session, got %v", got)
	}
	if len(m.sessions) != 0 {
		t.Fatalf("GetSessionAttachments lazily restored a store-only session (ghost): %d live", len(m.sessions))
	}
}

// -----------------------------------------------------------------------------
// #100 — DeleteSession must cancel and join the in-flight manual-compaction
// flow (bounded), and the flow's tail must not auto-resume a session flagged
// deleting.
// -----------------------------------------------------------------------------

func TestDeleteSessionDuringCompactionJoinsFlowAndLeavesNoGhost(t *testing.T) {
	m, sess, _, events, _ := newCompactionTestManager(t)

	if err := m.CompactSessionContext(context.Background(), sess.ID, "sliding_window"); err != nil {
		t.Fatalf("CompactSessionContext: %v", err)
	}
	waitForCompactionEvent(t, events, "compaction_started")

	// Delete mid-compaction: the compaction join must bound the flow (no
	// deadlock, no use-after-teardown), and the flow must not re-insert the
	// deleted session.
	if err := m.DeleteSession(sess.ID); err != nil {
		t.Fatalf("DeleteSession during compaction: %v", err)
	}
	waitForCompactionEvent(t, events, "compaction_finished")
	if len(m.sessions) != 0 {
		t.Fatalf("deleted session re-inserted by the compaction flow (ghost): %d live", len(m.sessions))
	}
}

type countingCancelStore struct {
	mockTaskStoreForResumable
	mu       sync.Mutex
	cancels  int
	pauses   int
	reactivs int
}

func (s *countingCancelStore) CancelTask(_ context.Context, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cancels++
	return nil
}

func (s *countingCancelStore) PauseTask(_ context.Context, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pauses++
	return nil
}

func (s *countingCancelStore) ReactivateTask(_ context.Context, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reactivs++
	return nil
}

func TestRunSessionCompactionSkipsAutoResumeWhenDeleting(t *testing.T) {
	m, sess, orch, _, _ := newCompactionTestManagerWithHistory(t, 1, nil)
	ts := &countingCancelStore{mockTaskStoreForResumable: mockTaskStoreForResumable{
		unfinished: &TaskRecord{ID: "task-unpaused", SessionID: sess.ID, Status: "paused"},
	}}
	m.SetTaskStore(ts)

	sess.mu.Lock()
	sess.pauseOwner = pauseOwnerCompaction
	sess.deleting = true
	sess.mu.Unlock()

	compCtx, cancel := context.WithCancel(context.Background())
	compactDone := make(chan struct{})
	m.runSessionCompaction(compCtx, cancel, compactDone, sess.ID, sess, orch, "sliding_window", nil, true)
	select {
	case <-compactDone:
	default:
		t.Fatal("runSessionCompaction did not close its done channel")
	}
	ts.mu.Lock()
	defer ts.mu.Unlock()
	if ts.cancels != 0 || ts.pauses != 0 || ts.reactivs != 0 {
		t.Fatalf("compaction flow resumed a deleting session: cancels=%d pauses=%d reactivations=%d",
			ts.cancels, ts.pauses, ts.reactivs)
	}
}

// -----------------------------------------------------------------------------
// #114 — ArchiveSession must fix the toggle target once, under the first
// lock: two concurrent archive calls for the same pre-state must net exactly
// one flip to archived.
// -----------------------------------------------------------------------------

// archiveBarrierStore deterministically interleaves two concurrent
// ArchiveSession calls: GetUnfinishedTask (called inside the heavy stop block,
// after the first critical section captured the toggle target) waits until
// BOTH callers have entered, so both read the same initial Archived value —
// exactly the interleave the two-section-toggle bug needs.
type archiveBarrierStore struct {
	mockTaskStoreForResumable
	mu      sync.Mutex
	entered int
	release chan struct{}
}

func newArchiveBarrierStore() *archiveBarrierStore {
	return &archiveBarrierStore{release: make(chan struct{})}
}

func (s *archiveBarrierStore) GetUnfinishedTask(_ context.Context, _ string) (*TaskRecord, error) {
	s.mu.Lock()
	s.entered++
	if s.entered == 2 {
		close(s.release)
	}
	s.mu.Unlock()
	select {
	case <-s.release:
	case <-time.After(5 * time.Second):
		// Watchdog (guard-exempt shape): the timeout is a test-premise
		// failure, not expected behavior — abort loudly instead of
		// degrading into a confusing late assertion.
		panic("archive barrier was not reached by the second caller — the interleaving this test pins never happened")
	}
	return nil, nil
}

func TestArchiveSessionConcurrentDuplicatesStayArchived(t *testing.T) {
	m, _, _ := testManager(t)
	info, err := m.CreateSession(testProjectID, testWorkspacePath(t))
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	m.mu.RLock()
	sess := m.sessions[info.ID]
	m.mu.RUnlock()
	m.SetTaskStore(newArchiveBarrierStore())

	var wg sync.WaitGroup
	wg.Add(2)
	for i := 0; i < 2; i++ {
		go func() {
			defer wg.Done()
			if err := m.ArchiveSession(info.ID); err != nil {
				t.Errorf("ArchiveSession: %v", err)
			}
		}()
	}
	wg.Wait()

	sess.mu.Lock()
	archived := sess.Archived
	sess.mu.Unlock()
	if !archived {
		t.Fatal("two concurrent archive calls flipped Archived back to false — manager desynced from the store")
	}

	// Sequential toggles keep their normal toggle semantics.
	if err := m.ArchiveSession(info.ID); err != nil {
		t.Fatalf("unarchive: %v", err)
	}
	sess.mu.Lock()
	archived = sess.Archived
	sess.mu.Unlock()
	if archived {
		t.Fatal("sequential archive did not toggle")
	}
}

// -----------------------------------------------------------------------------
// #116 / #145 — session store reads on the restore and list paths must carry
// a deadline (the shared SQLite pool turns an unbounded context into an
// indefinite hang).
// -----------------------------------------------------------------------------

type deadlineCaptureSessionStore struct {
	mockSessionStoreForRestore
	mu              sync.Mutex
	listByProjectBd bool
	listAllBd       bool
	loadMessagesBd  bool
}

func (s *deadlineCaptureSessionStore) ListSessionsByProject(ctx context.Context, _ string) ([]SessionInfo, error) {
	_, ok := ctx.Deadline()
	s.mu.Lock()
	s.listByProjectBd = ok
	s.mu.Unlock()
	return nil, nil
}

func (s *deadlineCaptureSessionStore) ListSessions(ctx context.Context) ([]SessionInfo, error) {
	_, ok := ctx.Deadline()
	s.mu.Lock()
	s.listAllBd = ok
	s.mu.Unlock()
	return nil, nil
}

func (s *deadlineCaptureSessionStore) LoadMessages(ctx context.Context, _ string) ([]ChatMessage, error) {
	_, ok := ctx.Deadline()
	s.mu.Lock()
	s.loadMessagesBd = ok
	s.mu.Unlock()
	return nil, nil
}

type deadlineCaptureTaskStore struct {
	mockTaskStoreForResumable
	mu           sync.Mutex
	latestBd     bool
	saveTaskBd   bool
	unfinishedBd bool
}

func (s *deadlineCaptureTaskStore) GetLatestTaskID(ctx context.Context, _ string) (string, error) {
	_, ok := ctx.Deadline()
	s.mu.Lock()
	s.latestBd = ok
	s.mu.Unlock()
	return "", nil
}

func (s *deadlineCaptureTaskStore) GetUnfinishedTask(ctx context.Context, _ string) (*TaskRecord, error) {
	_, ok := ctx.Deadline()
	s.mu.Lock()
	s.unfinishedBd = ok
	s.mu.Unlock()
	return nil, nil
}

func (s *deadlineCaptureTaskStore) SaveTask(ctx context.Context, _ TaskRecord) error {
	_, ok := ctx.Deadline()
	s.mu.Lock()
	s.saveTaskBd = ok
	s.mu.Unlock()
	return nil
}

func TestListSessionStoreReadsAreBounded(t *testing.T) {
	m, _, _ := testManager(t)
	store := &deadlineCaptureSessionStore{mockSessionStoreForRestore: *newMockSessionStore()}
	m.SetSessionStore(store)

	if _, err := m.ListSessionsByProject("proj"); err != nil {
		t.Fatalf("ListSessionsByProject: %v", err)
	}
	if _, err := m.ListSessionsAll(); err != nil {
		t.Fatalf("ListSessionsAll: %v", err)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if !store.listByProjectBd || !store.listAllBd {
		t.Fatalf("list reads lost their deadline: byProject=%v all=%v", store.listByProjectBd, store.listAllBd)
	}
}

func TestRestoreStoreReadsAreBounded(t *testing.T) {
	m, _, _ := testManager(t)
	store := &deadlineCaptureSessionStore{mockSessionStoreForRestore: *newMockSessionStore()}
	if err := store.SaveSession(context.Background(), SessionInfo{
		ID: "bounded-restore-session", ProjectID: testProjectID, Name: "n", CreatedAt: time.Now().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("save session: %v", err)
	}
	taskStore := &deadlineCaptureTaskStore{}
	m.SetSessionStore(store)
	m.SetTaskStore(taskStore)
	m.SetProjectResolver(func(string) (string, error) { return runtimeTempDir(t), nil })
	// The restore wires task persistence into the orchestrator, so the factory
	// must return a real one (unlike testManager's nil placeholder).
	m.SetFactory(func(_ core.Emitter, _ *slog.Logger, _ string, _ core.BlackboardFactory, _ io.Writer, _ *orchestration.StepDumpTracker) (*core.Orchestrator, error) {
		cfg := core.OrchestratorConfig{Model: "test-model"}
		deps := core.OrchestratorDeps{
			TokenCounter:  llm.NewSimpleTokenCounter(),
			ModelRegistry: llm.NewModelRegistry(map[string]llm.ModelMetadata{"test-model": {ContextWindow: 1000, OutputLimit: 100}}),
		}
		return core.NewOrchestrator(cfg, deps), nil
	})

	sess, err := m.getOrRestoreSession("bounded-restore-session")
	if err != nil || sess == nil {
		t.Fatalf("restore failed: sess=%v err=%v", sess, err)
	}
	store.mu.Lock()
	loadMessagesBd := store.loadMessagesBd
	store.mu.Unlock()
	taskStore.mu.Lock()
	defer taskStore.mu.Unlock()
	if !loadMessagesBd || !taskStore.latestBd {
		t.Fatalf("restore follow-up reads lost their deadline: messages=%v latestTask=%v", loadMessagesBd, taskStore.latestBd)
	}
}

// -----------------------------------------------------------------------------
// #133 — DeleteSession must stop the deleted session's blackboard persistence
// workers (a paused task never reaches a terminal finalizer).
// -----------------------------------------------------------------------------

func TestStopSessionBlackboards(t *testing.T) {
	m, _, _ := testManager(t)
	pb1 := NewPersistentBlackboard("task-1", "session-1", nil, nil)
	pb2 := NewPersistentBlackboard("task-2", "session-2", nil, nil)
	m.trackBlackboard(pb1)
	m.trackBlackboard(pb2)

	m.stopSessionBlackboards("session-1")

	if !pb1.persistenceWorkerStopped() {
		t.Fatal("deleted session's persistence worker still running")
	}
	if pb2.persistenceWorkerStopped() {
		t.Fatal("unrelated session's persistence worker was stopped")
	}
	m.mu.Lock()
	remaining := len(m.blackboards)
	m.mu.Unlock()
	if remaining != 1 {
		t.Fatalf("expected 1 tracked blackboard after the stop, got %d", remaining)
	}
}

// -----------------------------------------------------------------------------
// #144 — TaskStoreAdapter store calls must carry a deadline: the
// core.TaskPersistence interface takes no ctx, so the adapter bounds each
// call itself.
// -----------------------------------------------------------------------------

func TestTaskAdapterBoundsStoreCalls(t *testing.T) {
	ts := &deadlineCaptureTaskStore{}
	adapter := NewTaskStoreAdapter(ts)
	if err := adapter.PersistNewTask("t1", "s1", "req"); err != nil {
		t.Fatalf("PersistNewTask: %v", err)
	}
	if _, err := adapter.GetUnfinishedTaskID("s1"); err != nil {
		t.Fatalf("GetUnfinishedTaskID: %v", err)
	}
	if _, err := adapter.GetLatestTaskID("s1"); err != nil {
		t.Fatalf("GetLatestTaskID: %v", err)
	}
	ts.mu.Lock()
	defer ts.mu.Unlock()
	if !ts.saveTaskBd || !ts.unfinishedBd || !ts.latestBd {
		t.Fatalf("adapter store calls lost their deadline: save=%v unfinished=%v latest=%v",
			ts.saveTaskBd, ts.unfinishedBd, ts.latestBd)
	}
}

// -----------------------------------------------------------------------------
// #187 — a forced Stop must terminalize the run's goal state BEFORE/consistently
// with persisting the cancellation, so the cancelled run is not resumable.
// -----------------------------------------------------------------------------

func TestForceTerminateStuckTaskTerminalizesGoal(t *testing.T) {
	store, sid, cleanup := setupTestStoreWithSession(t)
	t.Cleanup(cleanup)
	m, _, _ := testManager(t)
	m.SetTaskStore(store)
	m.SetSessionStore(store)

	adapter := NewTaskStoreAdapter(store)
	if err := adapter.PersistNewTask("task-forced", sid, "original request"); err != nil {
		t.Fatalf("persist task: %v", err)
	}
	gs := &goalpkg.GoalState{Condition: "done", VerifyClause: "test -f done", Status: goalpkg.StatusActive}
	if err := adapter.PersistGoalState("task-forced", gs); err != nil {
		t.Fatalf("persist goal: %v", err)
	}

	m.forceTerminateStuckTask(sid)

	stored, err := adapter.LoadGoalState("task-forced")
	if err != nil {
		t.Fatalf("load goal: %v", err)
	}
	if stored == nil || !stored.Status.IsTerminal() {
		t.Fatalf("forced cancel left the goal state non-terminal: %+v", stored)
	}
	tid, err := adapter.GetUnfinishedTaskID(sid)
	if err != nil || tid != "" {
		t.Fatalf("task row still unfinished after forced cancel: id=%q err=%v", tid, err)
	}
}

// compile-time guards for the stubs' interface satisfaction.
var (
	_ SessionStore = (*deadlineCaptureSessionStore)(nil)
	_ TaskStore    = (*deadlineCaptureTaskStore)(nil)
	_ TaskStore    = (*countingCancelStore)(nil)
	_ TaskStore    = (*archiveBarrierStore)(nil)
)

// signalWithin reports whether the channel fires within d (bounded negative
// wait: the test asserts ABSENCE of an event inside the budget without
// sleeping the scheduler — the closeAndWait(bool) pattern of
// background_test.go).
func signalWithin(c <-chan struct{}, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-c:
		return true
	case <-timer.C:
		return false
	}
}
