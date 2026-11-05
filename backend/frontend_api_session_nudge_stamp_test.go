package backend

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/v0lka/c0wrk/backend/project"
	"github.com/v0lka/c0wrk/backend/session"
	"github.com/v0lka/c0wrk/core"
	"github.com/v0lka/c0wrk/core/prompts"
	"github.com/v0lka/sp4rk/agent"
	"github.com/v0lka/sp4rk/agent/router"
	"github.com/v0lka/sp4rk/llm"
	"github.com/v0lka/sp4rk/memory"
	"github.com/v0lka/sp4rk/orchestration"
	sdktools "github.com/v0lka/sp4rk/tools"
)

// The echo IsNudge flag (session/manager_execution.go) and the persisted
// is_nudge stamp (FrontendAPI.SendMessage → mergeIsNudgeMetadata) are written
// at two sites from the same SendClassification, and the manager-level echo
// tests pin the echo side only. This file is the DB-row half of that
// must-agree invariant: for every classification (fresh / live interjection /
// nudge-resume) the persisted user row's stamp must equal the echo's flag.

// gatedScriptedLLM returns scripted responses in order, then repeats the last
// one forever. When armed, the FIRST Call blocks until release is closed —
// the window a test uses to hold a task in its running state while it drives
// a live interjection into it.
type gatedScriptedLLM struct {
	mu       sync.Mutex
	scripted []*llm.ChatResponse
	idx      int
	gate     chan struct{}
}

func (s *gatedScriptedLLM) Call(_ context.Context, _ llm.ChatRequest) (*llm.ChatResponse, error) {
	s.mu.Lock()
	if g := s.gate; g != nil {
		s.mu.Unlock()
		<-g
		s.mu.Lock()
		s.gate = nil
	}
	defer s.mu.Unlock()
	if len(s.scripted) == 0 {
		return nil, errors.New("gatedScriptedLLM: no responses configured")
	}
	if s.idx < len(s.scripted) {
		resp := s.scripted[s.idx]
		s.idx++
		return resp, nil
	}
	return s.scripted[len(s.scripted)-1], nil
}

// setScript arms the response sequence (call before the first send).
func (s *gatedScriptedLLM) setScript(responses ...*llm.ChatResponse) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.scripted = responses
}

func (s *gatedScriptedLLM) release() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.gate != nil {
		close(s.gate)
	}
}

// routingTestJSONResponse builds a router classification response.
func routingTestJSONResponse(domain string, complexity int) *llm.ChatResponse {
	return &llm.ChatResponse{
		Message: llm.Message{
			Role:    "assistant",
			Content: `{"domain":"` + domain + `","complexity":` + strconv.Itoa(complexity) + `}`,
		},
		StopReason: "end_turn",
	}
}

// finishTestResponse builds an executor response that calls the finish tool.
func finishTestResponse(answer string) *llm.ChatResponse {
	return &llm.ChatResponse{
		Message: llm.Message{
			Role:      "assistant",
			Content:   "Task completed",
			ToolCalls: []llm.ToolCall{{ID: "c1", Name: "finish", Input: json.RawMessage(`{"answer": "` + answer + `"}`)}},
		},
		StopReason: "tool_use",
	}
}

// routingTestFactory builds a real orchestrator WITH a router (the normal
// route → execute path runs end-to-end); the same caller backs both.
func routingTestFactory(caller agent.LLMCaller) session.OrchestratorFactory {
	return func(emitter core.Emitter, _ *slog.Logger, _ string, bbFactory core.BlackboardFactory, _ io.Writer, _ *orchestration.StepDumpTracker) (*core.Orchestrator, error) {
		registry := sdktools.NewToolRegistry()
		cf := func(systemPrompt string, _ llm.ModelMetadata, _ string, _ ...orchestration.PruningOverride) core.ContextManager {
			cw := memory.NewContextWindow(memory.ContextWindowConfig{
				SystemPrompt: systemPrompt,
				ModelMeta:    llm.ModelMetadata{ContextWindow: 128000, OutputLimit: 4096},
			})
			return core.NewCoreContextManager(cw)
		}
		rtr := router.New(caller, router.Config{
			SystemPrompt:  prompts.RouterSystem,
			HistoryWindow: 5,
		})
		return core.NewOrchestrator(core.OrchestratorConfig{}, core.OrchestratorDeps{
			LLM:            caller,
			Router:         rtr,
			BBFactory:      bbFactory,
			ToolExec:       registry,
			ToolRegistry:   registry,
			TokenCounter:   llm.NewSimpleTokenCounter(),
			ContextFactory: cf,
			Emitter:        emitter,
			CircuitBreaker: agent.CircuitBreakerConfig{RepeatNudgeThreshold: 3, RepeatAbortThreshold: 4},
		}), nil
	}
}

// nudgeSweepDiagCapture swallows the manager's warn/error records into a
// buffer (printing nothing) so the test output stays clean, and its assert
// pins exactly what may appear: the harness's db close races the completion
// epilogue's BEST-EFFORT stale-task sweep (logged-only by design), which can
// observe "sql: database is closed" when cleanup wins. Any other warning, or
// more than one record, fails the test.
type nudgeSweepDiagCapture struct {
	mu  sync.Mutex
	rec []string
}

func (c *nudgeSweepDiagCapture) Enabled(_ context.Context, level slog.Level) bool {
	return level >= slog.LevelWarn
}

func (c *nudgeSweepDiagCapture) Handle(_ context.Context, r slog.Record) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.rec = append(c.rec, r.Message)
	return nil
}

func (c *nudgeSweepDiagCapture) WithAttrs(attrs []slog.Attr) slog.Handler { return c }
func (c *nudgeSweepDiagCapture) WithGroup(name string) slog.Handler       { return c }

func (c *nudgeSweepDiagCapture) assertOnlyKnownSweepRace(t *testing.T) {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, msg := range c.rec {
		if msg != "stale-task sweep: unfinished lookup failed" {
			t.Errorf("unexpected manager diagnostic: %q", msg)
		}
	}
	if len(c.rec) > 1 {
		t.Errorf("stale-task sweep race diagnostic fired %d times, want at most 1", len(c.rec))
	}
}

// nudgeStampHarness wires a FrontendAPI + real session manager (scripted LLM)
// + real SQLite stores, and captures the manager's UI events.
type nudgeStampHarness struct {
	api          *FrontendAPI
	store        *session.SQLiteSessionStore
	events       chan session.Event
	caller       *gatedScriptedLLM
	projectID    string
	agentDir     string
	dbClose      func() error
	shutdownOnce sync.Once
}

func newNudgeStampHarness(t *testing.T, gateFirstCall bool) *nudgeStampHarness {
	return newNudgeStampHarnessWithFactory(t, gateFirstCall, nil)
}

// newNudgeStampHarnessWithFactory is newNudgeStampHarness with an optional
// factory replacement (e.g. a goal-capable orchestrator for goal-loop tests);
// the wrapper receives the default factory and the scripted caller.
func newNudgeStampHarnessWithFactory(t *testing.T, gateFirstCall bool, wrapFactory func(inner session.OrchestratorFactory, caller agent.LLMCaller) session.OrchestratorFactory) *nudgeStampHarness {
	t.Helper()

	// A FILE-backed db pinned to one connection: the task runner goroutine
	// hits the store concurrently with the RPC goroutine, and :memory: with
	// a multi-connection pool gives every connection its own empty database
	// ("no such table: tasks" mid-test). Mirrors the
	// TestSwitchProject_PersistsLastActiveProjectIDAcrossReopen pattern.
	dbPath := filepath.Join(t.TempDir(), "nudge-stamp.db")
	dbConn, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	dbConn.SetMaxOpenConns(1)
	for _, pragma := range []string{"PRAGMA journal_mode=WAL", "PRAGMA foreign_keys=ON"} {
		if _, err := dbConn.ExecContext(context.Background(), pragma); err != nil {
			_ = dbConn.Close()
			t.Fatalf("apply %q: %v", pragma, err)
		}
	}

	projectStore, err := project.NewSQLiteProjectStore(dbConn)
	if err != nil {
		_ = dbConn.Close()
		t.Fatalf("project store: %v", err)
	}
	store, err := session.NewSQLiteSessionStore(dbConn)
	if err != nil {
		_ = dbConn.Close()
		t.Fatalf("session store: %v", err)
	}
	agentDir := t.TempDir()
	projectManager := project.NewManager(projectStore, agentDir, nil)
	createdProject, err := projectManager.CreateProject("Nudge Stamp Project", "")
	if err != nil {
		_ = dbConn.Close()
		t.Fatalf("create project: %v", err)
	}

	caller := &gatedScriptedLLM{scripted: []*llm.ChatResponse{
		routingTestJSONResponse("software_engineering", 1),
		finishTestResponse("done"),
	}}
	if gateFirstCall {
		caller.gate = make(chan struct{})
	}
	events := make(chan session.Event, 200)
	diag := &nudgeSweepDiagCapture{}
	factory := routingTestFactory(caller)
	if wrapFactory != nil {
		factory = wrapFactory(factory, caller)
	}
	manager := session.NewManager(factory, func(e session.Event) { events <- e }, agentDir)
	manager.SetLogger(slog.New(diag))
	manager.SetSessionStore(store)
	manager.SetTaskStore(store)
	manager.SetProjectResolver(func(projectID string) (string, error) {
		p, err := projectManager.GetProject(projectID)
		if err != nil || p == nil {
			return "", err
		}
		return p.WorkspacePath, nil
	})

	api := &FrontendAPI{
		app:   &Application{manager: manager},
		store: store,
	}
	api.seedPublished.Store(true)

	h := &nudgeStampHarness{
		api:       api,
		store:     store,
		events:    events,
		caller:    caller,
		projectID: createdProject.ID,
		agentDir:  agentDir,
		dbClose:   func() error { return dbConn.Close() },
	}
	t.Cleanup(func() {
		h.shutdownOnce.Do(func() { manager.Shutdown() })
		if err := h.dbClose(); err != nil {
			t.Errorf("close db: %v", err)
		}
		diag.assertOnlyKnownSweepRace(t)
	})
	return h
}

// newSession creates a manager session plus its persisted row (FK target).
func (h *nudgeStampHarness) newSession(t *testing.T, name string) string {
	t.Helper()
	info, err := h.api.app.manager.CreateSession(h.projectID, t.TempDir())
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := h.store.SaveSession(context.Background(), session.SessionInfo{
		ID: info.ID, ProjectID: h.projectID, Name: name,
		CreatedAt: time.Now().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}
	return info.ID
}

// waitForEventType drains the event channel until the wanted type arrives.
func waitForEventType(t *testing.T, events chan session.Event, eventType string, timeout time.Duration) session.Event {
	t.Helper()
	deadline := time.After(timeout)
	seen := []string{}
	for {
		select {
		case evt := <-events:
			seen = append(seen, evt.Type)
			if evt.Type == eventType {
				return evt
			}
		case <-deadline:
			t.Fatalf("timeout waiting for %s event; saw %v", eventType, seen)
		}
	}
}

// userRowIsNudge loads the persisted rows and reports the user row's
// is_nudge stamp (false when the row or the key is absent).
func (h *nudgeStampHarness) userRowIsNudge(t *testing.T, sessionID, content string) bool {
	t.Helper()
	msgs, err := h.store.LoadMessages(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("LoadMessages: %v", err)
	}
	for _, m := range msgs {
		if m.Role != "user" || m.Content != content {
			continue
		}
		var md map[string]any
		if len(m.Metadata) > 0 {
			if err := json.Unmarshal(m.Metadata, &md); err != nil {
				t.Fatalf("user row metadata: %v", err)
			}
		}
		v, _ := md["is_nudge"].(bool)
		return v
	}
	t.Fatalf("no persisted user row with content %q", content)
	return false
}

func TestSendMessage_NudgeStampMatchesEchoClassification(t *testing.T) {
	const steer = "steer the running task"

	t.Run("fresh task carries no stamp and a flagless echo", func(t *testing.T) {
		h := newNudgeStampHarness(t, false)
		sessID := h.newSession(t, "fresh")

		if err := h.api.SendMessage(sessID, "do a thing", nil, nil, nil, "", "", false, "", false, false, "", nil); err != nil {
			t.Fatalf("SendMessage: %v", err)
		}
		evt := waitForEventType(t, h.events, "message_received", 5*time.Second)
		echo, ok := evt.Data.(session.MessageReceivedData)
		if !ok {
			t.Fatalf("echo data type = %T", evt.Data)
		}
		if echo.IsNudge {
			t.Error("echo is_nudge = true, want absent on a fresh send")
		}
		if h.userRowIsNudge(t, sessID, "do a thing") {
			t.Error("fresh user row carries the is_nudge stamp, want none")
		}
		// Join the task goroutine before cleanup: the stale-task sweep runs
		// in it right after task_complete, and it must not race the db close.
		waitForEventType(t, h.events, "task_complete", 10*time.Second)
	})

	t.Run("live interjection stamps the row and the echo", func(t *testing.T) {
		h := newNudgeStampHarness(t, true)
		sessID := h.newSession(t, "live")

		// The gated first LLM call keeps the task running until the live
		// send has been classified.
		go func() {
			_ = h.api.SendMessage(sessID, "long running task", nil, nil, nil, "", "", false, "", false, false, "", nil)
		}()
		waitForEventType(t, h.events, "task_started", 5*time.Second)

		if err := h.api.SendMessage(sessID, steer, nil, nil, nil, "", "", false, "", false, false, "", nil); err != nil {
			t.Fatalf("live SendMessage: %v", err)
		}
		evt := waitForEventType(t, h.events, "message_received", 5*time.Second)
		echo, ok := evt.Data.(session.MessageReceivedData)
		if !ok {
			t.Fatalf("echo data type = %T", evt.Data)
		}
		// The fresh send's echo may arrive before or after the live one;
		// keep draining until the live row's echo is seen.
		for echo.Text != steer {
			evt = waitForEventType(t, h.events, "message_received", 5*time.Second)
			echo, ok = evt.Data.(session.MessageReceivedData)
			if !ok {
				t.Fatalf("echo data type = %T", evt.Data)
			}
		}
		if !echo.IsNudge {
			t.Error("live echo is_nudge absent, want true")
		}
		if !h.userRowIsNudge(t, sessID, steer) {
			t.Error("live user row missing the is_nudge stamp")
		}

		h.caller.release()
		waitForEventType(t, h.events, "task_complete", 10*time.Second)
	})

	t.Run("nudge-resume stamps the row and the echo", func(t *testing.T) {
		h := newNudgeStampHarness(t, false)
		sessID := h.newSession(t, "paused")

		// The resume path re-enters the executor directly (no re-routing).
		h.caller.setScript(finishTestResponse("resumed-and-finished"))
		if err := h.store.SaveTask(context.Background(), session.TaskRecord{
			ID: "task-nudge-stamp", SessionID: sessID, OriginalRequest: "long running task",
			RoutingDecision: json.RawMessage(`{}`), Plan: json.RawMessage(`{}`),
			Reflections: json.RawMessage(`[]`), Status: "paused", CreatedAt: time.Now(),
		}); err != nil {
			t.Fatalf("SaveTask: %v", err)
		}
		// A resume needs prior executor state to continue from; one canned
		// step mirrors the manager-level paused-task test's trajectory.
		traj, err := json.Marshal([]agent.Step{{
			Thought:     "step one",
			Action:      llm.ToolCall{ID: "pc1", Name: "read_file", Input: json.RawMessage(`{}`)},
			Observation: "PRIOR-1",
		}})
		if err != nil {
			t.Fatalf("marshal trajectory: %v", err)
		}
		if err := h.store.SaveTrajectory(context.Background(), "task-nudge-stamp", traj); err != nil {
			t.Fatalf("SaveTrajectory: %v", err)
		}

		if err := h.api.SendMessage(sessID, "continue from the checkpoint", nil, nil, nil, "", "", false, "", false, false, "", nil); err != nil {
			t.Fatalf("SendMessage: %v", err)
		}
		evt := waitForEventType(t, h.events, "message_received", 5*time.Second)
		echo, ok := evt.Data.(session.MessageReceivedData)
		if !ok {
			t.Fatalf("echo data type = %T", evt.Data)
		}
		if echo.Text != "continue from the checkpoint" {
			t.Errorf("echo text = %q, want the nudge", echo.Text)
		}
		if !echo.IsNudge {
			t.Error("nudge-resume echo is_nudge absent, want true")
		}
		if !h.userRowIsNudge(t, sessID, "continue from the checkpoint") {
			t.Error("nudge-resume user row missing the is_nudge stamp")
		}
		waitForEventType(t, h.events, "task_complete", 10*time.Second)
	})
}
