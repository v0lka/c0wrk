package session

import (
	"context"
	"encoding/json"
	"testing"
)

// savePagedMessage persists one message with an explicit created_at so the
// (created_at, id) order is deterministic.
func savePagedMessage(t *testing.T, store *SQLiteSessionStore, sessionID, role, content, createdAt string) {
	t.Helper()
	if err := store.SaveMessage(context.Background(), ChatMessage{
		SessionID: sessionID,
		Role:      role,
		Content:   content,
		Metadata:  json.RawMessage(`{}`),
		CreatedAt: createdAt,
	}); err != nil {
		t.Fatalf("SaveMessage(%s): %v", content, err)
	}
}

// seedPagedSession creates the session row a history read is scoped to, so
// message inserts satisfy the foreign key.
func seedPagedSession(t *testing.T, store *SQLiteSessionStore, sessionID string) {
	t.Helper()
	if err := store.SaveSession(context.Background(), SessionInfo{
		ID:        sessionID,
		ProjectID: testProjectID,
		Name:      "History",
		CreatedAt: "2024-01-01T00:00:00Z",
	}); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}
}

// TestLoadSessionHistory_ContentRowsAscending verifies the single-call full
// history read: every content row is returned in ascending (created_at, id)
// order, while the thinking/step_done activity roles never surface (they are
// persisted but produce no display item).
func TestLoadSessionHistory_ContentRowsAscending(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	const sid = "history-asc"
	seedPagedSession(t, store, sid)

	savePagedMessage(t, store, sid, "user", "u1", "2024-01-01T10:00:00Z")
	savePagedMessage(t, store, sid, "thinking", "noise-1", "2024-01-01T10:00:01Z")
	savePagedMessage(t, store, sid, "assistant", "a1", "2024-01-01T10:00:02Z")
	savePagedMessage(t, store, sid, "step_done", "noise-2", "2024-01-01T10:00:03Z")
	savePagedMessage(t, store, sid, "tool_call", "t1", "2024-01-01T10:00:04Z")
	savePagedMessage(t, store, sid, "user", "u2", "2024-01-01T10:00:05Z")

	got, err := store.LoadSessionHistory(context.Background(), sid)
	if err != nil {
		t.Fatalf("LoadSessionHistory: %v", err)
	}
	want := []string{"u1", "a1", "t1", "u2"}
	if len(got) != len(want) {
		t.Fatalf("got %d rows, want %d (thinking/step_done excluded): %+v", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i].Content != w {
			t.Fatalf("row %d content = %q, want %q", i, got[i].Content, w)
		}
	}
	for i := 1; i < len(got); i++ {
		if got[i].CreatedAt < got[i-1].CreatedAt {
			t.Errorf("rows not ascending: %q before %q", got[i-1].CreatedAt, got[i].CreatedAt)
		}
	}
	for _, m := range got {
		if m.Role == "thinking" || m.Role == "step_done" {
			t.Errorf("non-content role %q must be excluded from history", m.Role)
		}
	}
}

// TestLoadSessionHistory_EmptySessionReturnsEmptyNonNil verifies an empty
// session — and one whose only rows are non-content activity roles — yields a
// non-nil empty slice, so the caller-facing contract is "nothing to show",
// never nil.
func TestLoadSessionHistory_EmptySessionReturnsEmptyNonNil(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	const sid = "history-empty"
	seedPagedSession(t, store, sid)

	got, err := store.LoadSessionHistory(context.Background(), sid)
	if err != nil {
		t.Fatalf("LoadSessionHistory (empty session): %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil empty slice for an empty session")
	}
	if len(got) != 0 {
		t.Fatalf("expected 0 rows, got %d: %+v", len(got), got)
	}

	// A session whose only rows are the filtered activity roles is likewise
	// empty — and still non-nil.
	savePagedMessage(t, store, sid, "thinking", "noise", "2024-01-01T10:00:00Z")
	savePagedMessage(t, store, sid, "step_done", "noise", "2024-01-01T10:00:01Z")

	got, err = store.LoadSessionHistory(context.Background(), sid)
	if err != nil {
		t.Fatalf("LoadSessionHistory (activity-only session): %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil empty slice for an activity-only session")
	}
	if len(got) != 0 {
		t.Fatalf("expected 0 rows after filtering activity roles, got %d: %+v", len(got), got)
	}
}
