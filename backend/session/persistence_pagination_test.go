package session

import (
	"context"
	"encoding/json"
	"testing"
)

// savePagedMessage persists one message with an explicit created_at so the
// (created_at, id) keyset order is deterministic.
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

func seedPagedSession(t *testing.T, store *SQLiteSessionStore, sessionID string) {
	t.Helper()
	if err := store.SaveSession(context.Background(), SessionInfo{
		ID:        sessionID,
		ProjectID: testProjectID,
		Name:      "Pagination",
		CreatedAt: "2024-01-01T00:00:00Z",
	}); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}
}

// TestLoadMessagesPage_WalksBackwardsWithoutOverlapOrGaps verifies that paging
// from the newest page to the oldest yields every content row exactly once, in
// ascending order, with the correct hasMore flags.
func TestLoadMessagesPage_WalksBackwardsWithoutOverlapOrGaps(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	const sid = "page-walk"
	seedPagedSession(t, store, sid)

	// Seven content rows, one per second so the created_at keyset is distinct.
	stamps := []string{
		"2024-01-01T10:00:00Z", "2024-01-01T10:00:01Z", "2024-01-01T10:00:02Z",
		"2024-01-01T10:00:03Z", "2024-01-01T10:00:04Z", "2024-01-01T10:00:05Z",
		"2024-01-01T10:00:06Z",
	}
	contents := []string{"m1", "m2", "m3", "m4", "m5", "m6", "m7"}
	for i := range stamps {
		savePagedMessage(t, store, sid, "user", contents[i], stamps[i])
	}

	var got []string
	var cursor *MessageCursor
	pageCount := 0
	for {
		page, hasMore, err := store.LoadMessagesPage(context.Background(), sid, 3, cursor)
		if err != nil {
			t.Fatalf("LoadMessagesPage: %v", err)
		}
		if len(page) == 0 {
			t.Fatalf("page %d unexpectedly empty (hasMore=%v)", pageCount, hasMore)
		}
		// Pages arrive oldest-first WITHIN the page; we walk from the tail
		// backwards, so prepend each page to reconstruct ascending order.
		if page[0].CreatedAt > page[len(page)-1].CreatedAt {
			t.Fatalf("page %d not ascending: %s > %s", pageCount, page[0].CreatedAt, page[len(page)-1].CreatedAt)
		}
		pageContents := make([]string, 0, len(page))
		for _, m := range page {
			pageContents = append(pageContents, m.Content)
		}
		got = append(pageContents, got...)
		pageCount++
		if !hasMore {
			break
		}
		oldest := page[0]
		cursor = &MessageCursor{CreatedAt: oldest.CreatedAt, ID: oldest.ID}
		if pageCount > 10 {
			t.Fatal("pagination did not terminate")
		}
	}

	want := []string{"m1", "m2", "m3", "m4", "m5", "m6", "m7"}
	if len(got) != len(want) {
		t.Fatalf("collected %d rows, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("row %d: got %q, want %q (full: %v)", i, got[i], want[i], got)
		}
	}
}

// TestLoadMessagesPage_NewestFirstPage checks the shape of the first (newest)
// page and its hasMore flag.
func TestLoadMessagesPage_NewestFirstPage(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	const sid = "page-newest"
	seedPagedSession(t, store, sid)

	for _, ts := range []string{"2024-01-01T10:00:00Z", "2024-01-01T10:00:01Z", "2024-01-01T10:00:02Z"} {
		savePagedMessage(t, store, sid, "user", ts, ts)
	}

	page, hasMore, err := store.LoadMessagesPage(context.Background(), sid, 2, nil)
	if err != nil {
		t.Fatalf("LoadMessagesPage: %v", err)
	}
	if hasMore != true {
		t.Errorf("hasMore: got %v, want true (one older row remains)", hasMore)
	}
	if len(page) != 2 {
		t.Fatalf("len(page): got %d, want 2", len(page))
	}
	if page[0].CreatedAt != "2024-01-01T10:00:01Z" || page[1].CreatedAt != "2024-01-01T10:00:02Z" {
		t.Errorf("newest two rows wrong: %s, %s", page[0].CreatedAt, page[1].CreatedAt)
	}
	// The page must not be restricted to the cursor's own row set: the extra
	// probe row (limit+1) is trimmed.
	if page[len(page)-1].ID == 0 {
		t.Error("expected non-zero ids")
	}
}

// TestLoadMessagesPage_ExcludesNonContentRoles verifies thinking/step_done
// activity rows never reach the paged read path (they never render), while the
// full LoadMessages path is unchanged.
func TestLoadMessagesPage_ExcludesNonContentRoles(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	const sid = "page-roles"
	seedPagedSession(t, store, sid)

	savePagedMessage(t, store, sid, "user", "hello", "2024-01-01T10:00:00Z")
	savePagedMessage(t, store, sid, "thinking", "step 1", "2024-01-01T10:00:01Z")
	savePagedMessage(t, store, sid, "assistant", "answer", "2024-01-01T10:00:02Z")
	savePagedMessage(t, store, sid, "step_done", "done", "2024-01-01T10:00:03Z")

	page, hasMore, err := store.LoadMessagesPage(context.Background(), sid, 10, nil)
	if err != nil {
		t.Fatalf("LoadMessagesPage: %v", err)
	}
	if hasMore {
		t.Error("hasMore: got true, want false")
	}
	if len(page) != 2 {
		t.Fatalf("len(page): got %d, want 2 (thinking/step_done excluded)", len(page))
	}
	if page[0].Role != "user" || page[1].Role != "assistant" {
		t.Errorf("roles: got %q, %q; want user, assistant", page[0].Role, page[1].Role)
	}

	// LoadMessages (unpaged, used by history restore) still returns everything.
	all, err := store.LoadMessages(context.Background(), sid)
	if err != nil {
		t.Fatalf("LoadMessages: %v", err)
	}
	if len(all) != 4 {
		t.Errorf("LoadMessages should be unaffected: got %d, want 4", len(all))
	}
}

// TestLoadMessagesPage_SameSecondTieBreak pages rows that share a created_at;
// the id must break the tie so pages neither overlap nor skip.
func TestLoadMessagesPage_SameSecondTieBreak(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	const sid = "page-tie"
	seedPagedSession(t, store, sid)

	const ts = "2024-01-01T10:00:00Z"
	for _, c := range []string{"a", "b", "c"} {
		savePagedMessage(t, store, sid, "user", c, ts)
	}

	page1, hasMore1, err := store.LoadMessagesPage(context.Background(), sid, 2, nil)
	if err != nil {
		t.Fatalf("LoadMessagesPage page1: %v", err)
	}
	if !hasMore1 || len(page1) != 2 {
		t.Fatalf("page1: len=%d hasMore=%v, want 2/true", len(page1), hasMore1)
	}
	if page1[0].Content != "b" || page1[1].Content != "c" {
		t.Fatalf("page1 contents: got %q,%q; want b,c", page1[0].Content, page1[1].Content)
	}

	cursor := &MessageCursor{CreatedAt: page1[0].CreatedAt, ID: page1[0].ID}
	page2, hasMore2, err := store.LoadMessagesPage(context.Background(), sid, 2, cursor)
	if err != nil {
		t.Fatalf("LoadMessagesPage page2: %v", err)
	}
	if hasMore2 {
		t.Error("page2 hasMore: got true, want false")
	}
	if len(page2) != 1 || page2[0].Content != "a" {
		t.Fatalf("page2: got %+v, want [a]", page2)
	}
}

// TestLoadMessagesPage_ZeroLimitIsEmpty guards the defensive early return.
func TestLoadMessagesPage_ZeroLimitIsEmpty(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	const sid = "page-zero"
	seedPagedSession(t, store, sid)
	savePagedMessage(t, store, sid, "user", "hello", "2024-01-01T10:00:00Z")

	page, hasMore, err := store.LoadMessagesPage(context.Background(), sid, 0, nil)
	if err != nil {
		t.Fatalf("LoadMessagesPage: %v", err)
	}
	if len(page) != 0 || hasMore {
		t.Errorf("zero limit: got len=%d hasMore=%v, want 0/false", len(page), hasMore)
	}
}

// TestMessageCursorRoundTrip covers the opaque-token encode/decode, including
// the empty ("newest page") and malformed cases.
func TestMessageCursorRoundTrip(t *testing.T) {
	if c, err := DecodeMessageCursor(""); err != nil || c != nil {
		t.Fatalf("empty cursor: got (%v, %v), want (nil, nil)", c, err)
	}

	in := MessageCursor{CreatedAt: "2024-01-01T10:00:00Z", ID: 42}
	out, err := DecodeMessageCursor(EncodeMessageCursor(in))
	if err != nil {
		t.Fatalf("DecodeMessageCursor: %v", err)
	}
	if out == nil || out.ID != in.ID || out.CreatedAt != in.CreatedAt {
		t.Fatalf("round trip: got %+v, want %+v", out, in)
	}

	for _, bad := range []string{"no-separator", "2024-01-01T10:00:00Z|", "|123"} {
		if _, err := DecodeMessageCursor(bad); err == nil {
			t.Errorf("DecodeMessageCursor(%q): expected error", bad)
		}
	}
}
