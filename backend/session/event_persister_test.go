package session

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/v0lka/c0wrk/backend/project"
)

// captureStore is a minimal SessionStore that records every saved message so
// the persister tests can assert on what gets persisted.
type captureStore struct {
	mu               sync.Mutex
	messages         []ChatMessage
	stepTodoReplaces []stepTodoReplaceCall
}

type stepTodoReplaceCall struct {
	sessionID string
	stepID    string
	msg       ChatMessage
}

func (s *captureStore) SaveSession(_ context.Context, _ SessionInfo) error { return nil }
func (s *captureStore) LoadSession(_ context.Context, _ string) (*SessionInfo, error) {
	return nil, nil
}
func (s *captureStore) ListSessions(_ context.Context) ([]SessionInfo, error) {
	return nil, nil
}
func (s *captureStore) ListSessionsByProject(_ context.Context, _ string) ([]SessionInfo, error) {
	return nil, nil
}
func (s *captureStore) DeleteSession(_ context.Context, _ string) error { return nil }
func (s *captureStore) ArchiveSession(_ context.Context, _ string, _ bool) error {
	return nil
}
func (s *captureStore) PinSession(_ context.Context, _ string, _ bool) error {
	return nil
}
func (s *captureStore) RenameSession(_ context.Context, _, _ string) error { return nil }
func (s *captureStore) UpdateSessionTokens(_ context.Context, _ string, _, _ int, _, _ string, _ float64) error {
	return nil
}
func (s *captureStore) UpdateSessionActivity(_ context.Context, _ string) error { return nil }
func (s *captureStore) SaveMessage(_ context.Context, msg ChatMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.messages = append(s.messages, msg)
	return nil
}
func (s *captureStore) ReplaceStepTodoUpdate(_ context.Context, sessionID, stepID string, msg ChatMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stepTodoReplaces = append(s.stepTodoReplaces, stepTodoReplaceCall{sessionID: sessionID, stepID: stepID, msg: msg})
	return nil
}
func (s *captureStore) LoadMessages(_ context.Context, _ string) ([]ChatMessage, error) {
	return nil, nil
}
func (s *captureStore) LoadSessionHistory(_ context.Context, _ string) ([]ChatMessage, error) {
	return nil, nil
}
func (s *captureStore) DeleteMessages(_ context.Context, _ string) error { return nil }
func (s *captureStore) ResolvePendingMessage(_ context.Context, _, _, _, _ string, _ map[string]any) error {
	return nil
}
func (s *captureStore) SaveTerminalCommand(_ context.Context, _, _ string) error {
	return nil
}
func (s *captureStore) LoadTerminalCommands(_ context.Context, _ string, _ int) ([]TerminalCommand, error) {
	return nil, nil
}
func (s *captureStore) SaveSessionWorkDir(_ context.Context, _ string, _ project.WorkDirectoryRecord) error {
	return nil
}
func (s *captureStore) ListSessionWorkDirs(_ context.Context, _ string) ([]project.WorkDirectoryRecord, error) {
	return nil, nil
}
func (s *captureStore) UpdateSessionWorkDirDescription(_ context.Context, _, _, _ string) error {
	return nil
}
func (s *captureStore) DeleteSessionWorkDir(_ context.Context, _, _ string) error { return nil }
func (s *captureStore) SaveBookmark(_ context.Context, b SessionBookmark) (SessionBookmark, error) {
	return b, nil
}
func (s *captureStore) ListBookmarks(_ context.Context, _ string) ([]SessionBookmark, error) {
	return nil, nil
}
func (s *captureStore) DeleteBookmark(_ context.Context, _, _ string) error { return nil }
func (s *captureStore) RenameBookmark(_ context.Context, _, _, _ string) error {
	return nil
}
func (s *captureStore) Close() error { return nil }

func (s *captureStore) snapshot() []ChatMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := make([]ChatMessage, len(s.messages))
	copy(cp, s.messages)
	return cp
}

// assistantRows returns the persisted rows with role "assistant".
func (s *captureStore) assistantRows() []ChatMessage {
	var out []ChatMessage
	for _, m := range s.snapshot() {
		if m.Role == "assistant" {
			out = append(out, m)
		}
	}
	return out
}

// TestEventPersister_DedupTaskCompleteAgainstAssistantDone verifies that when
// the implicit text-only finish path emits assistant_done followed by
// task_complete with the SAME output, the persister persists the answer only
// once (the assistant_done row), preventing the final answer from appearing
// twice on session reload.
func TestEventPersister_DedupTaskCompleteAgainstAssistantDone(t *testing.T) {
	store := &captureStore{}
	p := NewEventPersister(store)

	const answer = "The final answer"

	// Simulate the implicit text-only finish event order:
	// assistant_done (streamed) → task_complete (same Output).
	p.Persist(Event{SessionID: "s1", Type: "assistant_done", Data: AssistantDoneEventData{Content: answer}})
	p.Persist(Event{SessionID: "s1", Type: "task_complete", Data: TaskCompleteData{Output: answer, Success: true}})

	rows := store.assistantRows()
	if len(rows) != 1 {
		t.Fatalf("expected 1 assistant row (dedup), got %d: %+v", len(rows), rows)
	}
	if rows[0].Content != answer {
		t.Errorf("expected persisted content %q, got %q", answer, rows[0].Content)
	}
}

// TestEventPersister_KeepsTaskCompleteWhenOutputDiffers verifies that
// task_complete is still persisted when its output differs from the last
// streamed assistant content (the explicit finish-tool path, where the
// streamed thought and the finish answer are different).
func TestEventPersister_KeepsTaskCompleteWhenOutputDiffers(t *testing.T) {
	store := &captureStore{}
	p := NewEventPersister(store)

	p.Persist(Event{SessionID: "s1", Type: "assistant_done", Data: AssistantDoneEventData{Content: "thinking text"}})
	p.Persist(Event{SessionID: "s1", Type: "task_complete", Data: TaskCompleteData{Output: "the real answer", Success: true}})

	rows := store.assistantRows()
	if len(rows) != 2 {
		t.Fatalf("expected 2 assistant rows (no dedup), got %d: %+v", len(rows), rows)
	}
}

// TestEventPersister_MessageReceivedResetsDedupScope verifies that a new user
// message resets the per-session assistant tracking, so a task_complete whose
// output coincidentally matches a PRIOR task's streamed answer is still
// persisted (not falsely deduped across tasks).
func TestEventPersister_MessageReceivedResetsDedupScope(t *testing.T) {
	store := &captureStore{}
	p := NewEventPersister(store)

	const answer = "same text"

	// First task: stream + complete with the same answer (deduped to 1 row).
	p.Persist(Event{SessionID: "s1", Type: "assistant_done", Data: AssistantDoneEventData{Content: answer}})
	p.Persist(Event{SessionID: "s1", Type: "task_complete", Data: TaskCompleteData{Output: answer, Success: true}})

	// New user message resets the tracking.
	p.Persist(Event{SessionID: "s1", Type: "message_received", Data: MessageReceivedData{SessionID: "s1", Text: "next question"}})

	// Second task: task_complete with the same answer but NO preceding
	// assistant_done in this task — must be persisted (not deduped).
	p.Persist(Event{SessionID: "s1", Type: "task_complete", Data: TaskCompleteData{Output: answer, Success: true}})

	rows := store.assistantRows()
	// 1 (first task, deduped) + 1 (second task, kept) = 2
	if len(rows) != 2 {
		t.Fatalf("expected 2 assistant rows after message_received reset, got %d: %+v", len(rows), rows)
	}
}

// TestEventPersister_SessionDeletedClearsDedupTracking verifies that a
// session_deleted event removes the per-session assistant tracking entry,
// preventing unbounded growth of lastAssistantContent in the long-lived
// persister singleton. After deletion, a task_complete whose output matches
// a prior task's streamed answer is persisted (not falsely deduped).
func TestEventPersister_SessionDeletedClearsDedupTracking(t *testing.T) {
	store := &captureStore{}
	p := NewEventPersister(store)

	const answer = "same text"

	// First task: stream + complete with the same answer (deduped to 1 row).
	p.Persist(Event{SessionID: "s1", Type: "assistant_done", Data: AssistantDoneEventData{Content: answer}})
	p.Persist(Event{SessionID: "s1", Type: "task_complete", Data: TaskCompleteData{Output: answer, Success: true}})

	// Session deleted — tracking entry must be removed.
	p.Persist(Event{SessionID: "s1", Type: "session_deleted", Data: map[string]any{"session_id": "s1"}})

	// A task_complete with the same answer but no preceding assistant_done
	// in this "session" must be persisted (not deduped against the stale
	// tracking entry that should have been cleared).
	p.Persist(Event{SessionID: "s1", Type: "task_complete", Data: TaskCompleteData{Output: answer, Success: true}})

	rows := store.assistantRows()
	// 1 (first task, deduped) + 1 (after deletion, kept) = 2
	if len(rows) != 2 {
		t.Fatalf("expected 2 assistant rows after session_deleted reset, got %d: %+v", len(rows), rows)
	}
}

// TestEventPersister_EmptyOutputTaskCompletePersistsPlaceholder verifies the
// empty-output guard still persists a "[Task completed]" placeholder so
// session continuations see the full conversation history.
func TestEventPersister_EmptyOutputTaskCompletePersistsPlaceholder(t *testing.T) {
	store := &captureStore{}
	p := NewEventPersister(store)

	p.Persist(Event{SessionID: "s1", Type: "task_complete", Data: TaskCompleteData{Output: "", Success: true}})

	rows := store.assistantRows()
	if len(rows) != 1 {
		t.Fatalf("expected 1 assistant row, got %d", len(rows))
	}
	if rows[0].Content != "[Task completed]" {
		t.Errorf("expected placeholder, got %q", rows[0].Content)
	}
}

// TestEventPersister_JudgePhaseEventsAreTransient verifies strict-judge (Smart
// Approve) phase telemetry is never persisted: the labels describe a live
// judge run that predates any confirmation card; replaying them on reload
// would resurrect stale "judge working" state.
func TestEventPersister_JudgePhaseEventsAreTransient(t *testing.T) {
	store := &captureStore{}
	p := NewEventPersister(store)

	p.Persist(Event{SessionID: "s1", Type: "tool_judge_started", Data: map[string]any{"tool": "bash_exec"}})
	p.Persist(Event{SessionID: "s1", Type: "tool_judge_finished", Data: map[string]any{"tool": "bash_exec"}})

	if rows := store.snapshot(); len(rows) != 0 {
		t.Fatalf("expected 0 persisted rows for judge phase events, got %d: %+v", len(rows), rows)
	}
}

// TestEventPersister_ToolConfirmIsTransient verifies tool_confirm events are
// never persisted, now that the desktop confirm callback routes them through
// the emitter pipeline (which includes the persister). Pending confirmations
// are process-local: a persisted row could never be resolved after a restart
// and would render as a dead confirmation card on reload.
func TestEventPersister_ToolConfirmIsTransient(t *testing.T) {
	store := &captureStore{}
	p := NewEventPersister(store)

	p.Persist(Event{SessionID: "s1", Type: "tool_confirm", Data: ToolConfirmPayload{
		ConfirmID: "c1", Tool: "bash_exec", Args: "{}",
	}})

	if rows := store.snapshot(); len(rows) != 0 {
		t.Fatalf("expected 0 persisted rows for tool_confirm, got %d: %+v", len(rows), rows)
	}
}

// TestEventPersister_UIStateEventsAreTransient verifies that UI-only state
// events emitted with a SessionID (attachments:changed, session pin/archive
// toggles) are NOT persisted. These carry no conversational content; their
// raw JSON metadata payload would otherwise leak into session_messages as an
// event_unknown row whose content is the JSON blob (rendering as garbage text
// on reload).
func TestEventPersister_UIStateEventsAreTransient(t *testing.T) {
	store := &captureStore{}
	p := NewEventPersister(store)

	events := []Event{
		{SessionID: "s1", Type: "attachments:changed", Data: AttachmentsChangedData{
			Attachments: []AttachmentInfo{
				{ID: "att-1", OriginalName: "report.pdf", Format: "pdf", SizeBytes: 1000},
			},
		}},
		{SessionID: "s1", Type: "attachments:changed", Data: AttachmentsChangedData{Attachments: []AttachmentInfo{}}},
		{SessionID: "s1", Type: "session_pinned", Data: SessionPinnedData{ID: "s1", Pinned: true}},
		{SessionID: "s1", Type: "session_unpinned", Data: SessionPinnedData{ID: "s1", Pinned: false}},
		{SessionID: "s1", Type: "session_archived", Data: SessionArchivedData{ID: "s1", Archived: true}},
		{SessionID: "s1", Type: "session_unarchived", Data: SessionArchivedData{ID: "s1", Archived: false}},
	}
	for _, evt := range events {
		p.Persist(evt)
	}

	if rows := store.snapshot(); len(rows) != 0 {
		t.Fatalf("expected 0 persisted rows for transient UI state events, got %d: %+v", len(rows), rows)
	}
}

// TestEventPersister_E2SStateTransient verifies that e2s_state — the UI-only
// execution-state Σ snapshot emitted with a SessionID after every E2S turn —
// is never persisted. The frontend panel renders it live only (no persisted
// restore), so a persisted row would store the Σ JSON as an event_unknown
// message that renders as garbage on reload and consumes a paged-history
// slot, plus a per-turn "unknown event type" schema-drift warning.
func TestEventPersister_E2SStateTransient(t *testing.T) {
	store := &captureStore{}
	p := NewEventPersister(store)

	p.Persist(Event{
		SessionID: "s1",
		Type:      "e2s_state",
		Data: map[string]any{
			"turn":        3,
			"total_turns": 10,
			"state":       map[string]any{"objective": "ship it", "status": "in_progress"},
		},
	})

	if rows := store.snapshot(); len(rows) != 0 {
		t.Fatalf("expected 0 persisted rows for e2s_state, got %d: %+v", len(rows), rows)
	}
}

// TestEventPersister_GoalStatusPersisted_GoalProgressTransient verifies that a
// goal_status snapshot survives a reload (role "goal_status", full metadata) so
// the frontend can rebuild the goal store and re-render the turn-transition
// notice, while goal_progress remains live-only telemetry and is dropped.
func TestEventPersister_GoalStatusPersisted_GoalProgressTransient(t *testing.T) {
	store := &captureStore{}
	p := NewEventPersister(store)

	p.Persist(Event{
		SessionID: "s1",
		Type:      "goal_status",
		Data: map[string]any{
			"status":    "met",
			"turn":      2,
			"condition": "ship it",
			"max_turns": 5,
			"verdict":   "met",
			"reason":    "tests green",
		},
	})
	p.Persist(Event{
		SessionID: "s1",
		Type:      "goal_progress",
		Data: map[string]any{
			"turn":      2,
			"max_turns": 5,
			"condition": "ship it",
		},
	})

	rows := store.snapshot()
	if len(rows) != 1 {
		t.Fatalf("expected 1 persisted row (goal_status only), got %d: %+v", len(rows), rows)
	}
	if rows[0].Role != "goal_status" {
		t.Errorf("expected role goal_status, got %q", rows[0].Role)
	}
	// The persister stores the JSON metadata; the frontend reads it from the
	// metadata field (not content). Just verify the row is non-empty so the
	// reload path has a payload to reconstruct.
	if rows[0].Content == "" {
		t.Error("expected non-empty goal_status content (metadata JSON)")
	}
}

// TestEventPersister_StepTodoUpdateReplacesPreviousRow verifies the new
// step_todo_update semantics end-to-end through a real store: an update for a
// step_id deletes that step's previous checklist row and inserts the new one at
// the current stream position (so the row MOVES to the tail), guaranteeing
// exactly one row per step — no duplicates — however many updates are emitted.
func TestEventPersister_StepTodoUpdateReplacesPreviousRow(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	const sid = "step-todo-replace"
	seedPagedSession(t, store, sid)

	p := NewEventPersister(store)
	emit := func(stepID, text string) {
		p.Persist(Event{
			SessionID: sid,
			Type:      "step_todo_update",
			Data: map[string]any{
				"step_id": stepID,
				"items":   []map[string]any{{"text": text, "checked": false}},
			},
		})
	}

	// step_1 → step_2 → step_1: the second step_1 update must delete the first
	// step_1 row and re-insert it AFTER step_2, proving the position changed (an
	// in-place UPDATE would have pinned step_1 before step_2).
	emit("step_1", "v1")
	emit("step_2", "x1")
	emit("step_1", "v2")

	rows, err := store.LoadMessages(context.Background(), sid)
	if err != nil {
		t.Fatalf("LoadMessages: %v", err)
	}
	var todos []ChatMessage
	for _, m := range rows {
		if m.Role == "step_todo_update" {
			todos = append(todos, m)
		}
	}
	if len(todos) != 2 {
		t.Fatalf("expected 2 step_todo_update rows (one per step, no duplicates), got %d: %+v", len(todos), todos)
	}
	// Position changed: the surviving step_1 row now follows step_2.
	if got := stepIDFromMetadata(t, todos[0]); got != "step_2" {
		t.Errorf("first checklist row step_id = %q, want %q (the stale step_1 row must have been replaced, not kept in place)", got, "step_2")
	}
	if got := stepIDFromMetadata(t, todos[1]); got != "step_1" {
		t.Errorf("second checklist row step_id = %q, want %q", got, "step_1")
	}
	// The surviving step_1 row carries the LATEST payload only (v2, not v1).
	if !strings.Contains(todos[1].Content, "v2") {
		t.Errorf("step_1 row should reflect the latest update v2, got %q", todos[1].Content)
	}
	if strings.Contains(todos[1].Content, "v1") {
		t.Errorf("stale v1 payload must not survive the replace, got %q", todos[1].Content)
	}
}

// TestEventPersister_StepTodoUpdateRoutedToReplace verifies that
// step_todo_update events reach the store through ReplaceStepTodoUpdate and
// NEVER through the generic SaveMessage path — the routing that keeps checklist
// updates collapsing onto one row per step instead of accumulating a row per
// tool call.
func TestEventPersister_StepTodoUpdateRoutedToReplace(t *testing.T) {
	store := &captureStore{}
	p := NewEventPersister(store)

	emit := func(stepID string) {
		p.Persist(Event{
			SessionID: "s1",
			Type:      "step_todo_update",
			Data: map[string]any{
				"step_id": stepID,
				"items":   []map[string]any{{"text": "a", "checked": false}},
			},
		})
	}

	emit("step_1")
	emit("step_1")
	emit("")
	emit("step_2")

	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.messages) != 0 {
		t.Fatalf("step_todo_update must not go through SaveMessage, got %d rows: %+v", len(store.messages), store.messages)
	}
	if len(store.stepTodoReplaces) != 4 {
		t.Fatalf("expected 4 ReplaceStepTodoUpdate calls, got %d", len(store.stepTodoReplaces))
	}
	if got := store.stepTodoReplaces[0].stepID; got != "step_1" {
		t.Errorf("first call step_id: got %q", got)
	}
	if got := store.stepTodoReplaces[2].stepID; got != "" {
		t.Errorf("third call step_id (standalone): got %q", got)
	}
	if store.stepTodoReplaces[0].msg.Role != "step_todo_update" {
		t.Errorf("replaced message role: got %q", store.stepTodoReplaces[0].msg.Role)
	}
}

// stepIDFromMetadata unmarshals a persisted message's metadata and returns its
// step_id (empty when absent).
func stepIDFromMetadata(t *testing.T, msg ChatMessage) string {
	t.Helper()
	var meta map[string]any
	if err := json.Unmarshal(msg.Metadata, &meta); err != nil {
		t.Fatalf("unmarshal metadata: %v", err)
	}
	sid, _ := meta["step_id"].(string)
	return sid
}

// TestEventPersister_PauseCheckpointsPersisted verifies that the cooperative
// pause events (plan_step_paused, subagent_paused) are persisted with their
// own roles and full metadata (step_id, duration, error) so a paused run
// survives an app restart: on reload the paused checkpoint reappears as a
// recognizable row instead of a generic event_unknown JSON blob.
func TestEventPersister_PauseCheckpointsPersisted(t *testing.T) {
	store := &captureStore{}
	p := NewEventPersister(store)

	p.Persist(Event{
		SessionID: "s1",
		Type:      "plan_step_paused",
		Data: map[string]any{
			"step_id":            "step_2",
			"duration":           int64(2500),
			"progress":           0.25,
			"current_step_index": -1,
			"completed_count":    1,
			"total_count":        4,
			"error":              "awaiting plan review",
		},
	})
	p.Persist(Event{
		SessionID: "s1",
		Type:      "subagent_paused",
		Data: map[string]any{
			"step_id":  "step_3",
			"duration": int64(1200),
		},
	})

	rows := store.snapshot()
	if len(rows) != 2 {
		t.Fatalf("expected 2 persisted rows, got %d: %+v", len(rows), rows)
	}

	if rows[0].Role != "plan_step_paused" {
		t.Errorf("plan_step_paused role: got %q", rows[0].Role)
	}
	var planMeta struct {
		StepID  string `json:"step_id"`
		Error   string `json:"error"`
		Seconds int64  `json:"duration"`
	}
	if err := json.Unmarshal(rows[0].Metadata, &planMeta); err != nil {
		t.Fatalf("plan_step_paused metadata is not valid JSON: %v", err)
	}
	if planMeta.StepID != "step_2" {
		t.Errorf("plan_step_paused metadata step_id: got %q", planMeta.StepID)
	}
	if planMeta.Error != "awaiting plan review" {
		t.Errorf("plan_step_paused metadata error: got %q", planMeta.Error)
	}
	if planMeta.Seconds != 2500 {
		t.Errorf("plan_step_paused metadata duration: got %d", planMeta.Seconds)
	}

	if rows[1].Role != "subagent_paused" {
		t.Errorf("subagent_paused role: got %q", rows[1].Role)
	}
	var subMeta struct {
		StepID   string `json:"step_id"`
		Duration int64  `json:"duration"`
	}
	if err := json.Unmarshal(rows[1].Metadata, &subMeta); err != nil {
		t.Fatalf("subagent_paused metadata is not valid JSON: %v", err)
	}
	if subMeta.StepID != "step_3" {
		t.Errorf("subagent_paused metadata step_id: got %q", subMeta.StepID)
	}
	if subMeta.Duration != 1200 {
		t.Errorf("subagent_paused metadata duration: got %d", subMeta.Duration)
	}
}

// TestEventPersister_ServicePhaseGatesPersistence verifies that only the
// "orchestration" service phase is persisted (as a "status" chat row). The
// per-task "Routing request..." boilerplate now carries phase "routing" and must
// stay transient — persisting it would resurrect a redundant chat row on reload.
func TestEventPersister_ServicePhaseGatesPersistence(t *testing.T) {
	t.Run("routing phase is transient", func(t *testing.T) {
		store := &captureStore{}
		p := NewEventPersister(store)

		p.Persist(Event{SessionID: "s1", Type: "service", Data: map[string]any{
			"content": "Routing request...", "phase": "routing",
		}})

		if rows := store.snapshot(); len(rows) != 0 {
			t.Fatalf("expected 0 persisted rows for a routing-phase service event, got %d: %+v", len(rows), rows)
		}
	})

	t.Run("orchestration phase is persisted as a status row", func(t *testing.T) {
		store := &captureStore{}
		p := NewEventPersister(store)

		p.Persist(Event{SessionID: "s1", Type: "service", Data: map[string]any{
			"content": "Queued message could not start a follow-up task", "phase": "orchestration",
		}})

		rows := store.snapshot()
		if len(rows) != 1 {
			t.Fatalf("expected 1 persisted row for an orchestration-phase service event, got %d: %+v", len(rows), rows)
		}
		if rows[0].Role != "status" {
			t.Errorf("role = %q, want %q", rows[0].Role, "status")
		}
	})
}

// blockingStore blocks SaveMessage on gate until it is closed, so a test can
// prove that Persist does not run the store write on the caller's goroutine.
type blockingStore struct {
	captureStore
	gate chan struct{}
}

func (s *blockingStore) SaveMessage(ctx context.Context, msg ChatMessage) error {
	<-s.gate
	return s.captureStore.SaveMessage(ctx, msg)
}

// TestEventPersister_AsyncWriterDoesNotBlockOnStore verifies acceptance
// criterion 2: once StartWriter is called, Persist returns without running the
// SQLite write on the emitting goroutine. The store write is held open, yet
// Persist must still return promptly.
func TestEventPersister_AsyncWriterDoesNotBlockOnStore(t *testing.T) {
	store := &blockingStore{gate: make(chan struct{})}
	p := NewEventPersister(store)
	p.StartWriter()
	defer p.Close()

	done := make(chan struct{})
	go func() {
		p.Persist(Event{SessionID: "s1", Type: "assistant_done", Data: AssistantDoneEventData{Content: "hi"}})
		close(done)
	}()

	select {
	case <-done:
		// Persist returned while the store write is still blocked → the write
		// ran on the single-writer goroutine, not the caller's.
	case <-time.After(2 * time.Second):
		t.Fatal("Persist blocked on the store write (ran synchronously on the caller)")
	}

	close(store.gate)
	p.Flush()

	if rows := store.snapshot(); len(rows) != 1 {
		t.Fatalf("expected the queued write to complete after unblocking, got %d rows", len(rows))
	}
}

// TestEventPersister_CloseDrainsQueuedWrites verifies acceptance criterion 3:
// queued writes are not lost — closing the persister drains everything.
func TestEventPersister_CloseDrainsQueuedWrites(t *testing.T) {
	store := &captureStore{}
	p := NewEventPersister(store)
	p.StartWriter()

	const n = 200
	for i := 0; i < n; i++ {
		p.Persist(Event{SessionID: "s1", Type: "tool_call", Data: map[string]any{"i": i}})
	}
	p.Close() // must drain, not drop

	if rows := store.snapshot(); len(rows) != n {
		t.Fatalf("expected %d persisted rows after Close, got %d", n, len(rows))
	}
}

// TestEventPersister_AsyncWriterPreservesOrderAndDedup verifies the async path
// keeps the synchronous dedup semantics: the dedup decision is made on the
// caller in emit order, so task_complete is still collapsed against the
// preceding assistant_done.
func TestEventPersister_AsyncWriterPreservesOrderAndDedup(t *testing.T) {
	store := &captureStore{}
	p := NewEventPersister(store)
	p.StartWriter()
	defer p.Close()

	const answer = "final answer"
	p.Persist(Event{SessionID: "s1", Type: "assistant_done", Data: AssistantDoneEventData{Content: answer}})
	p.Persist(Event{SessionID: "s1", Type: "task_complete", Data: TaskCompleteData{Output: answer, Success: true}})
	p.Flush()

	if rows := store.assistantRows(); len(rows) != 1 {
		t.Fatalf("expected 1 assistant row (dedup through the writer), got %d: %+v", len(rows), rows)
	}
}

// TestEventPersister_FlushWithoutWriterIsNoop verifies Flush is safe when no
// writer has been started (the synchronous, test/non-desktop mode).
func TestEventPersister_FlushWithoutWriterIsNoop(t *testing.T) {
	store := &captureStore{}
	p := NewEventPersister(store)

	p.Persist(Event{SessionID: "s1", Type: "assistant_done", Data: AssistantDoneEventData{Content: "sync"}})
	p.Flush()
	p.Close()

	if rows := store.snapshot(); len(rows) != 1 {
		t.Fatalf("expected 1 synchronously persisted row, got %d", len(rows))
	}
}
