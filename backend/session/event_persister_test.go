package session

import (
	"context"
	"sync"
	"testing"

	"github.com/v0lka/c0wrk/backend/project"
)

// captureStore is a minimal SessionStore that records every saved message so
// the persister tests can assert on what gets persisted.
type captureStore struct {
	mu              sync.Mutex
	messages        []ChatMessage
	stepTodoUpdates []stepTodoUpsertCall
}

type stepTodoUpsertCall struct {
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
func (s *captureStore) UpsertStepTodoUpdate(_ context.Context, sessionID, stepID string, msg ChatMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stepTodoUpdates = append(s.stepTodoUpdates, stepTodoUpsertCall{sessionID: sessionID, stepID: stepID, msg: msg})
	return nil
}
func (s *captureStore) LoadMessages(_ context.Context, _ string) ([]ChatMessage, error) {
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
func (s *captureStore) Close() error                                              { return nil }

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

// TestEventPersister_StepTodoUpdateRoutedToUpsert verifies that step_todo_update
// events are persisted via UpsertStepTodoUpdate (not the generic SaveMessage
// path), so checklist updates for the same step_id collapse to a single row in
// the store instead of accumulating one row per update.
func TestEventPersister_StepTodoUpdateRoutedToUpsert(t *testing.T) {
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
	if len(store.stepTodoUpdates) != 4 {
		t.Fatalf("expected 4 UpsertStepTodoUpdate calls, got %d", len(store.stepTodoUpdates))
	}
	if got := store.stepTodoUpdates[0].stepID; got != "step_1" {
		t.Errorf("first call step_id: got %q", got)
	}
	if got := store.stepTodoUpdates[2].stepID; got != "" {
		t.Errorf("third call step_id (standalone): got %q", got)
	}
	if store.stepTodoUpdates[0].msg.Role != "step_todo_update" {
		t.Errorf("upserted message role: got %q", store.stepTodoUpdates[0].msg.Role)
	}
}
