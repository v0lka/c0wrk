package session

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/core"
	"github.com/v0lka/c0wrk/sdk/orchestration"
)

// ---------------------------------------------------------------------------
// convertChatMessagesToLLM
// ---------------------------------------------------------------------------

// TestConvertChatMessagesToLLM_CollapsesAssistantRuns verifies that
// consecutive assistant rows (intermediate step outputs + final task output)
// are collapsed to the most recent one, matching the live in-memory history
// which keeps only the final output per exchange.
func TestConvertChatMessagesToLLM_CollapsesAssistantRuns(t *testing.T) {
	m := &Manager{}
	msgs := []ChatMessage{
		{Role: "user", Content: "build the API"},
		{Role: "assistant", Content: "step 1 output"},
		{Role: "assistant", Content: "step 2 output"},
		{Role: "assistant", Content: "final task output"},
		{Role: "user", Content: "add tests"},
		{Role: "assistant", Content: "tests added"},
	}

	history := m.convertChatMessagesToLLM(msgs)
	if len(history) != 4 {
		t.Fatalf("expected 4 messages after collapsing, got %d: %+v", len(history), history)
	}
	if history[1].Content != "final task output" {
		t.Errorf("expected collapsed assistant run to keep the final output, got %q", history[1].Content)
	}
	if history[3].Content != "tests added" {
		t.Errorf("expected second exchange assistant message, got %q", history[3].Content)
	}
}

// TestConvertChatMessagesToLLM_ErrorAndCancelledNotes verifies that persisted
// "error" and "task_cancelled" rows are converted to the same assistant notes
// that the orchestrator records live, so failed and cancelled exchanges
// survive a restart identically.
func TestConvertChatMessagesToLLM_ErrorAndCancelledNotes(t *testing.T) {
	m := &Manager{}
	msgs := []ChatMessage{
		{Role: "user", Content: "failing request"},
		{Role: "error", Content: `{"session_id":"s1","error":"planning failed: boom"}`, Metadata: json.RawMessage(`{"session_id":"s1","error":"planning failed: boom"}`)},
		{Role: "user", Content: "cancelled request"},
		{Role: "task_cancelled", Content: `{"session_id":"s1"}`, Metadata: json.RawMessage(`{"session_id":"s1"}`)},
	}

	history := m.convertChatMessagesToLLM(msgs)
	if len(history) != 4 {
		t.Fatalf("expected 4 messages, got %d: %+v", len(history), history)
	}
	if history[1].Role != "assistant" || history[1].Content != core.HistoryNoteFailed("planning failed: boom") {
		t.Errorf("expected failure note, got %+v", history[1])
	}
	if history[3].Role != "assistant" || history[3].Content != core.HistoryNoteCancelled {
		t.Errorf("expected cancellation note, got %+v", history[3])
	}
}

// TestConvertChatMessagesToLLM_ErrorNoteReplacesPartialOutput verifies that a
// failure note collapses preceding intermediate assistant outputs, matching
// the live history where a failed exchange records only user + failure note.
func TestConvertChatMessagesToLLM_ErrorNoteReplacesPartialOutput(t *testing.T) {
	m := &Manager{}
	msgs := []ChatMessage{
		{Role: "user", Content: "multi step task"},
		{Role: "assistant", Content: "step 1 partial output"},
		{Role: "error", Content: `{"error":"step 2 failed"}`, Metadata: json.RawMessage(`{"error":"step 2 failed"}`)},
	}

	history := m.convertChatMessagesToLLM(msgs)
	if len(history) != 2 {
		t.Fatalf("expected 2 messages, got %d: %+v", len(history), history)
	}
	if history[1].Content != core.HistoryNoteFailed("step 2 failed") {
		t.Errorf("expected failure note to replace partial output, got %q", history[1].Content)
	}
}

// TestConvertChatMessagesToLLM_PreprocessesUserText verifies that the raw
// stored user text (with @file markers) is normalized the same way the
// orchestrator preprocessed it live.
func TestConvertChatMessagesToLLM_PreprocessesUserText(t *testing.T) {
	m := &Manager{}
	msgs := []ChatMessage{
		{Role: "user", Content: "explain @main.go please"},
		{Role: "assistant", Content: "explained"},
	}

	history := m.convertChatMessagesToLLM(msgs)
	if len(history) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(history))
	}
	want := "explain fileref://main.go please"
	if history[0].Content != want {
		t.Errorf("expected preprocessed user text %q, got %q", want, history[0].Content)
	}
}

// TestConvertChatMessagesToLLM_SkipsNonConversationalRoles verifies that
// tool/status/thought rows do not leak into the conversation history.
func TestConvertChatMessagesToLLM_SkipsNonConversationalRoles(t *testing.T) {
	m := &Manager{}
	msgs := []ChatMessage{
		{Role: "user", Content: "do it"},
		{Role: "thought", Content: "thinking"},
		{Role: "tool_call", Content: "{}"},
		{Role: "status", Content: "Routing request..."},
		{Role: "assistant", Content: "done"},
	}

	history := m.convertChatMessagesToLLM(msgs)
	if len(history) != 2 {
		t.Fatalf("expected 2 messages, got %d: %+v", len(history), history)
	}
	if history[0].Role != "user" || history[1].Role != "assistant" {
		t.Errorf("unexpected roles: %+v", history)
	}
}

// ---------------------------------------------------------------------------
// lastCompletedTaskID restoration
// ---------------------------------------------------------------------------

// fakeTaskStoreForRestore implements TaskStore for restoration tests. Only
// GetLatestTaskID is functional; other methods are inherited from the
// embedded nil interface and panic if called (they must not be called during
// session restoration).
type fakeTaskStoreForRestore struct {
	TaskStore
	latestTaskID string
	latestErr    error
}

func (f *fakeTaskStoreForRestore) GetLatestTaskID(_ context.Context, _ string) (string, error) {
	return f.latestTaskID, f.latestErr
}

// restoreTestManagerWithTaskStore builds a Manager wired with a mock session
// store, a fake task store, and a factory producing a minimal real
// orchestrator (required because session restoration calls orchestrator
// setters when a task store is configured).
func restoreTestManagerWithTaskStore(t *testing.T, ts TaskStore) (*Manager, *mockSessionStoreForRestore) {
	t.Helper()

	agentDir := t.TempDir()
	factory := func(_ core.Emitter, _ *slog.Logger, _ string, _ core.BlackboardFactory, _ io.Writer, _ *orchestration.StepDumpTracker) (*core.Orchestrator, error) {
		return core.NewOrchestrator(core.OrchestratorConfig{}, core.OrchestratorDeps{}), nil
	}

	mgr := NewManager(factory, func(Event) {}, agentDir)

	store := newMockSessionStore()
	mgr.SetSessionStore(store)
	mgr.SetTaskStore(ts)
	mgr.SetProjectResolver(func(projectID string) (string, error) {
		return config.ProjectDir(agentDir, projectID), nil
	})

	return mgr, store
}

// TestRestoreSession_LastTaskIDRestored verifies that a lazily restored
// session picks up the latest task ID from the task store, so the next user
// message takes the continuation path (which receives conversation history)
// instead of planning from scratch after a backend restart.
func TestRestoreSession_LastTaskIDRestored(t *testing.T) {
	ts := &fakeTaskStoreForRestore{latestTaskID: "task-42"}
	mgr, store := restoreTestManagerWithTaskStore(t, ts)
	seedSession(t, store, "restore-task-1", testProjectID, "Session With Task", false)

	sess, ok := mgr.GetSession("restore-task-1")
	if !ok {
		t.Fatal("GetSession should restore the session")
	}
	if sess.lastCompletedTaskID != "task-42" {
		t.Errorf("expected restored lastCompletedTaskID %q, got %q", "task-42", sess.lastCompletedTaskID)
	}
}

// TestRestoreSession_LastTaskIDAbsent verifies that restoration without prior
// tasks leaves the continuation anchor empty.
func TestRestoreSession_LastTaskIDAbsent(t *testing.T) {
	ts := &fakeTaskStoreForRestore{latestTaskID: ""}
	mgr, store := restoreTestManagerWithTaskStore(t, ts)
	seedSession(t, store, "restore-task-2", testProjectID, "Session Without Task", false)

	sess, ok := mgr.GetSession("restore-task-2")
	if !ok {
		t.Fatal("GetSession should restore the session")
	}
	if sess.lastCompletedTaskID != "" {
		t.Errorf("expected empty lastCompletedTaskID, got %q", sess.lastCompletedTaskID)
	}
}

// TestRestoreSession_LastTaskIDStoreError verifies that a task store error
// during restoration is tolerated (session restored, anchor left empty).
func TestRestoreSession_LastTaskIDStoreError(t *testing.T) {
	ts := &fakeTaskStoreForRestore{latestErr: errors.New("db locked")}
	mgr, store := restoreTestManagerWithTaskStore(t, ts)
	seedSession(t, store, "restore-task-3", testProjectID, "Session Store Error", false)

	sess, ok := mgr.GetSession("restore-task-3")
	if !ok {
		t.Fatal("GetSession should restore the session despite task store error")
	}
	if sess.lastCompletedTaskID != "" {
		t.Errorf("expected empty lastCompletedTaskID on store error, got %q", sess.lastCompletedTaskID)
	}
}
