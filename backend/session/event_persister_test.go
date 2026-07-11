package session

import (
	"context"
	"sync"
	"testing"
)

// captureStore is a minimal SessionStore that records every saved message so
// the persister tests can assert on what gets persisted.
type captureStore struct {
	mu       sync.Mutex
	messages []ChatMessage
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
