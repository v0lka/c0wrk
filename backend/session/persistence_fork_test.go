package session

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/v0lka/c0wrk/backend/project"
)

// seedSessionForFork populates a session with messages, terminal commands, work
// directories, a completed task (with steps/facts/attachments/trajectory) and
// returns the source session id and the source task id.
func seedSessionForFork(t *testing.T, store *SQLiteSessionStore, name string) (sessionID, taskID string) {
	t.Helper()
	ctx := context.Background()

	sessionID = "fork-source"
	if err := store.SaveSession(ctx, SessionInfo{
		ID: sessionID, ProjectID: testProjectID, Name: name,
		CreatedAt:        time.Now().Add(-time.Hour).Format(time.RFC3339),
		LastActiveAt:     time.Now().Add(-time.Minute).Format(time.RFC3339),
		TotalInputTokens: 1234, TotalOutputTokens: 567, Model: "gpt-test", Family: "openai", FillPercent: 42.5,
	}); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}

	// Messages with tool_calls/metadata to verify JSON is preserved verbatim.
	reasoning := "let me think"
	toolCalls := json.RawMessage(`[{"id":"call_1","function":{"name":"bash"}}]`)
	if err := store.SaveMessage(ctx, ChatMessage{
		SessionID: sessionID, Role: "user", Content: "hello",
		Metadata: json.RawMessage(`{}`), CreatedAt: time.Now().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("SaveMessage user: %v", err)
	}
	if err := store.SaveMessage(ctx, ChatMessage{
		SessionID: sessionID, Role: "assistant", Content: "response",
		ReasoningContent: &reasoning, ToolCalls: &toolCalls,
		Metadata: json.RawMessage(`{"k":"v"}`), CreatedAt: time.Now().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("SaveMessage assistant: %v", err)
	}

	// Terminal command.
	if err := store.SaveTerminalCommand(ctx, sessionID, "ls -la"); err != nil {
		t.Fatalf("SaveTerminalCommand: %v", err)
	}

	// Work directory.
	if err := store.SaveSessionWorkDir(ctx, sessionID, project.WorkDirectoryRecord{
		ID: "wdir-1", Path: "/tmp/extra", Description: "extra root", CreatedAt: time.Now().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("SaveSessionWorkDir: %v", err)
	}

	// Completed task + children.
	taskID = "task-src-1"
	completedAt := time.Now()
	if err := store.SaveTask(ctx, TaskRecord{
		ID: taskID, SessionID: sessionID, OriginalRequest: "do work",
		RoutingDecision: json.RawMessage(`{"route":"code"}`), Plan: json.RawMessage(`{"steps":2}`),
		Reflections: json.RawMessage(`["r1"]`), FinalOutput: "done", AttemptCount: 1,
		Status: "completed", CreatedAt: completedAt.Add(-time.Minute), CompletedAt: &completedAt,
	}); err != nil {
		t.Fatalf("SaveTask: %v", err)
	}
	if err := store.SaveTaskStep(ctx, taskID, TaskStepRecord{
		StepID: "step_1", TaskID: taskID, Summary: "did step 1", FullOutput: "output-1",
		Steps: json.RawMessage(`[]`), CreatedAt: completedAt,
	}); err != nil {
		t.Fatalf("SaveTaskStep: %v", err)
	}
	if err := store.SaveFacts(ctx, taskID, json.RawMessage(`[{"kw":"a","v":"b"}]`)); err != nil {
		t.Fatalf("SaveFacts: %v", err)
	}
	if err := store.SaveAttachments(ctx, taskID, json.RawMessage(`[{"path":"x.txt"}]`)); err != nil {
		t.Fatalf("SaveAttachments: %v", err)
	}
	if err := store.SaveTrajectory(ctx, taskID, json.RawMessage(`[{"action":"run"}]`)); err != nil {
		t.Fatalf("SaveTrajectory: %v", err)
	}

	return sessionID, taskID
}

func TestForkSession_FullCopyAndRemapping(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx := context.Background()

	srcID, srcTaskID := seedSessionForFork(t, store, "Original")

	fork, err := store.ForkSession(ctx, srcID, nil)
	if err != nil {
		t.Fatalf("ForkSession: %v", err)
	}
	if fork == nil {
		t.Fatal("fork is nil")
	}
	if fork.ID == srcID {
		t.Error("fork session id must differ from source")
	}
	if fork.Name != "Original (fork 1)" {
		t.Errorf("fork name = %q, want %q", fork.Name, "Original (fork 1)")
	}
	if fork.ProjectID != testProjectID {
		t.Errorf("fork project = %q, want %q", fork.ProjectID, testProjectID)
	}
	// Fresh runtime accounting.
	if fork.Archived {
		t.Error("fork should not be archived")
	}
	if fork.TotalInputTokens != 0 || fork.TotalOutputTokens != 0 || fork.FillPercent != 0 || fork.Model != "" || fork.Family != "" {
		t.Errorf("fork runtime counters not reset: %+v", fork)
	}

	// Messages copied with identical content/JSON.
	srcMsgs, _ := store.LoadMessages(ctx, srcID)
	forkMsgs, err := store.LoadMessages(ctx, fork.ID)
	if err != nil {
		t.Fatalf("LoadMessages fork: %v", err)
	}
	if len(forkMsgs) != len(srcMsgs) {
		t.Fatalf("message count: fork=%d src=%d", len(forkMsgs), len(srcMsgs))
	}
	// Verify role/content preserved (ids are autoincrement, must differ).
	for i, m := range forkMsgs {
		if m.Role != srcMsgs[i].Role || m.Content != srcMsgs[i].Content {
			t.Errorf("msg %d mismatch: role=%q content=%q", i, m.Role, m.Content)
		}
		if m.SessionID != fork.ID {
			t.Errorf("msg %d session_id=%q want %q", i, m.SessionID, fork.ID)
		}
		if m.ID == srcMsgs[i].ID {
			t.Errorf("msg %d id must differ from source", i)
		}
	}
	// Assistant message reasoning/tool_calls preserved.
	var assistant *ChatMessage
	for i := range forkMsgs {
		if forkMsgs[i].Role == "assistant" {
			assistant = &forkMsgs[i]
		}
	}
	if assistant == nil || assistant.ReasoningContent == nil || *assistant.ReasoningContent != "let me think" {
		t.Error("assistant reasoning_content not preserved")
	}
	if assistant == nil || assistant.ToolCalls == nil {
		t.Error("assistant tool_calls not preserved")
	}

	// Terminal commands copied.
	forkCmds, err := store.LoadTerminalCommands(ctx, fork.ID, 10)
	if err != nil {
		t.Fatalf("LoadTerminalCommands fork: %v", err)
	}
	if len(forkCmds) != 1 || forkCmds[0].Command != "ls -la" {
		t.Errorf("terminal commands not copied: %+v", forkCmds)
	}

	// Work directories copied with new id.
	srcDirs, _ := store.ListSessionWorkDirs(ctx, srcID)
	forkDirs, err := store.ListSessionWorkDirs(ctx, fork.ID)
	if err != nil {
		t.Fatalf("ListSessionWorkDirs fork: %v", err)
	}
	if len(forkDirs) != 1 || forkDirs[0].Path != srcDirs[0].Path {
		t.Errorf("work dirs not copied: %+v", forkDirs)
	}
	if forkDirs[0].ID == srcDirs[0].ID {
		t.Error("work dir id must be regenerated")
	}

	// Tasks remapped: new task id, same content, children present.
	srcTaskIDs := mustListTaskIDs(ctx, t, store, srcID)
	forkTaskIDs := mustListTaskIDs(ctx, t, store, fork.ID)
	if len(forkTaskIDs) != 1 {
		t.Fatalf("expected 1 forked task, got %d", len(forkTaskIDs))
	}
	newTaskID := forkTaskIDs[0]
	if newTaskID == srcTaskID {
		t.Error("forked task id must differ from source task id")
	}
	if newTaskID == srcTaskIDs[0] {
		t.Error("task id collision between fork and source")
	}

	forkTask, err := store.LoadTask(ctx, newTaskID)
	if err != nil || forkTask == nil {
		t.Fatalf("LoadTask fork: %v", err)
	}
	if forkTask.SessionID != fork.ID {
		t.Errorf("forked task session_id=%q want %q", forkTask.SessionID, fork.ID)
	}
	if forkTask.OriginalRequest != "do work" || forkTask.Status != "completed" {
		t.Errorf("forked task content mismatch: %+v", forkTask)
	}
	if forkTask.Plan == nil || string(forkTask.Plan) != `{"steps":2}` {
		t.Errorf("forked task plan not preserved: %s", string(forkTask.Plan))
	}

	// Steps remapped onto new task id.
	steps, err := store.LoadTaskSteps(ctx, newTaskID)
	if err != nil {
		t.Fatalf("LoadTaskSteps fork: %v", err)
	}
	if len(steps) != 1 || steps[0].Summary != "did step 1" || steps[0].TaskID != newTaskID {
		t.Errorf("forked task steps not remapped: %+v", steps)
	}

	// Facts/attachments/trajectory remapped.
	facts, err := store.LoadFacts(ctx, newTaskID)
	if err != nil || facts == nil || string(facts) != `[{"kw":"a","v":"b"}]` {
		t.Errorf("forked facts not remapped: %s (err %v)", string(facts), err)
	}
	att, err := store.LoadAttachments(ctx, newTaskID)
	if err != nil || att == nil || string(att) != `[{"path":"x.txt"}]` {
		t.Errorf("forked attachments not remapped: %s (err %v)", string(att), err)
	}
	traj, err := store.LoadTrajectory(ctx, newTaskID)
	if err != nil || traj == nil || string(traj) != `[{"action":"run"}]` {
		t.Errorf("forked trajectory not remapped: %s (err %v)", string(traj), err)
	}
}

func TestForkSession_NameCollision(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx := context.Background()

	srcID, _ := seedSessionForFork(t, store, "Collide")

	f1, err := store.ForkSession(ctx, srcID, nil)
	if err != nil {
		t.Fatalf("first fork: %v", err)
	}
	if f1.Name != "Collide (fork 1)" {
		t.Fatalf("first fork name=%q", f1.Name)
	}

	f2, err := store.ForkSession(ctx, srcID, nil)
	if err != nil {
		t.Fatalf("second fork: %v", err)
	}
	if f2.Name != "Collide (fork 2)" {
		t.Errorf("second fork name=%q, want %q", f2.Name, "Collide (fork 2)")
	}

	// Forking the fork should also produce a unique name.
	f3, err := store.ForkSession(ctx, f1.ID, nil)
	if err != nil {
		t.Fatalf("third fork: %v", err)
	}
	if f3.Name != "Collide (fork 1) (fork 1)" {
		t.Errorf("fork-of-fork name=%q, want %q", f3.Name, "Collide (fork 1) (fork 1)")
	}
}

func TestForkSession_SourceUntouched(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx := context.Background()

	srcID, _ := seedSessionForFork(t, store, "Keep")

	srcBefore, _ := store.LoadSession(ctx, srcID)
	srcMsgsBefore, _ := store.LoadMessages(ctx, srcID)
	srcTasksBefore := mustListTaskIDs(ctx, t, store, srcID)

	if _, err := store.ForkSession(ctx, srcID, nil); err != nil {
		t.Fatalf("ForkSession: %v", err)
	}

	srcAfter, _ := store.LoadSession(ctx, srcID)
	srcMsgsAfter, _ := store.LoadMessages(ctx, srcID)
	srcTasksAfter := mustListTaskIDs(ctx, t, store, srcID)

	if srcAfter.Name != srcBefore.Name {
		t.Errorf("source name changed: %q -> %q", srcBefore.Name, srcAfter.Name)
	}
	if len(srcMsgsAfter) != len(srcMsgsBefore) {
		t.Errorf("source message count changed: %d -> %d", len(srcMsgsBefore), len(srcMsgsAfter))
	}
	if len(srcTasksAfter) != len(srcTasksBefore) {
		t.Errorf("source task count changed: %d -> %d", len(srcTasksBefore), len(srcTasksAfter))
	}
}

func TestForkSession_NonexistentSource(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	fork, err := store.ForkSession(context.Background(), "does-not-exist", nil)
	if err == nil {
		t.Fatal("expected error for nonexistent source")
	}
	if fork != nil {
		t.Error("expected nil fork on error")
	}
}

func TestForkSession_EmptySession(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx := context.Background()

	// A session with no messages/tasks/dirs/work-dirs should fork cleanly.
	if err := store.SaveSession(ctx, SessionInfo{
		ID: "empty-src", ProjectID: testProjectID, Name: "Empty", CreatedAt: time.Now().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}

	fork, err := store.ForkSession(ctx, "empty-src", nil)
	if err != nil {
		t.Fatalf("ForkSession empty: %v", err)
	}
	if fork.Name != "Empty (fork 1)" {
		t.Errorf("fork name=%q", fork.Name)
	}
	if msgs, _ := store.LoadMessages(ctx, fork.ID); len(msgs) != 0 {
		t.Errorf("expected 0 messages, got %d", len(msgs))
	}
	if ids := mustListTaskIDs(ctx, t, store, fork.ID); len(ids) != 0 {
		t.Errorf("expected 0 tasks, got %d", len(ids))
	}
}

// TestForkSession_ReviewCloneError_RollsBack verifies that when the review
// cloner (run inside the fork transaction) fails, the entire fork is rolled
// back: no new session, messages, or tasks are created.
func TestForkSession_ReviewCloneError_RollsBack(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx := context.Background()

	srcID, _ := seedSessionForFork(t, store, "Atomic")

	sessionsBefore, err := store.ListSessionsByProject(ctx, testProjectID)
	if err != nil {
		t.Fatalf("ListSessionsByProject before: %v", err)
	}
	msgsBefore, _ := store.LoadMessages(ctx, srcID)

	// A cloner that always fails must abort the whole fork.
	failingCloner := ForkReviewCloner(func(_ context.Context, _ *sql.Tx, _, _ string) error {
		return errors.New("review clone failed")
	})

	fork, err := store.ForkSession(ctx, srcID, failingCloner)
	if err == nil {
		t.Fatal("expected error when review clone fails")
	}
	if fork != nil {
		t.Error("expected nil fork on review-clone failure")
	}

	// No new session row should exist.
	sessionsAfter, err := store.ListSessionsByProject(ctx, testProjectID)
	if err != nil {
		t.Fatalf("ListSessionsByProject after: %v", err)
	}
	if len(sessionsAfter) != len(sessionsBefore) {
		t.Errorf("session count changed after rollback: before=%d after=%d", len(sessionsBefore), len(sessionsAfter))
	}

	// Source untouched.
	msgsAfter, _ := store.LoadMessages(ctx, srcID)
	if len(msgsAfter) != len(msgsBefore) {
		t.Errorf("source message count changed: before=%d after=%d", len(msgsBefore), len(msgsAfter))
	}
}

// mustListTaskIDs returns the task ids for a session, failing the test on error.
func mustListTaskIDs(ctx context.Context, t *testing.T, store *SQLiteSessionStore, sessionID string) []string {
	t.Helper()
	rows, err := store.db.QueryContext(ctx,
		`SELECT id FROM tasks WHERE session_id = ? ORDER BY created_at`, sessionID)
	if err != nil {
		t.Fatalf("query tasks: %v", err)
	}
	defer func() { _ = rows.Close() }()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan task id: %v", err)
		}
		ids = append(ids, id)
	}
	return ids
}
