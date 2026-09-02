package session

import (
	"context"
	"testing"
	"time"
)

func TestSessionBookmarkCRUD(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx := context.Background()

	if err := store.SaveSession(ctx, SessionInfo{ID: "s1", ProjectID: testProjectID, Name: "S1", CreatedAt: time.Now().Format(time.RFC3339)}); err != nil {
		t.Fatalf("save session: %v", err)
	}

	b := SessionBookmark{SessionID: "s1", EventKey: "assistant-123", Title: "Answer"}
	saved, err := store.SaveBookmark(ctx, b)
	if err != nil {
		t.Fatalf("save bookmark: %v", err)
	}
	// SaveBookmark returns the persisted record, so generated fields are
	// directly observable without a follow-up read.
	if saved.ID == "" {
		t.Fatal("bookmark ID should be generated")
	}
	if saved.CreatedAt == "" {
		t.Fatal("bookmark CreatedAt should be generated")
	}

	list, err := store.ListBookmarks(ctx, "s1")
	if err != nil {
		t.Fatalf("list bookmarks: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 bookmark, got %d", len(list))
	}
	if list[0].ID == "" {
		t.Fatal("bookmark ID should be generated")
	}
	if list[0].CreatedAt == "" {
		t.Fatal("bookmark CreatedAt should be generated")
	}
	if list[0].EventKey != "assistant-123" || list[0].Title != "Answer" {
		t.Fatalf("bookmark fields mismatch: %+v", list[0])
	}

	if err := store.RenameBookmark(ctx, "s1", list[0].ID, "Renamed"); err != nil {
		t.Fatalf("rename bookmark: %v", err)
	}
	list, _ = store.ListBookmarks(ctx, "s1")
	if list[0].Title != "Renamed" {
		t.Fatalf("expected renamed title, got %q", list[0].Title)
	}

	if err := store.DeleteBookmark(ctx, "s1", list[0].ID); err != nil {
		t.Fatalf("delete bookmark: %v", err)
	}
	list, _ = store.ListBookmarks(ctx, "s1")
	if len(list) != 0 {
		t.Fatalf("expected empty after delete, got %d", len(list))
	}
}

func TestSessionBookmarkIsolationAndCascade(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx := context.Background()

	for _, id := range []string{"s1", "s2"} {
		if err := store.SaveSession(ctx, SessionInfo{ID: id, ProjectID: testProjectID, Name: id, CreatedAt: time.Now().Format(time.RFC3339)}); err != nil {
			t.Fatalf("save session %s: %v", id, err)
		}
	}

	b := SessionBookmark{SessionID: "s1", EventKey: "user-1", Title: "U1"}
	if _, err := store.SaveBookmark(ctx, b); err != nil {
		t.Fatalf("save bookmark: %v", err)
	}

	// Isolation: s2 sees nothing.
	if list, _ := store.ListBookmarks(ctx, "s2"); len(list) != 0 {
		t.Fatalf("expected s2 to have no bookmarks, got %d", len(list))
	}
	// Cross-scope delete must not remove s1's bookmark.
	if err := store.DeleteBookmark(ctx, "s2", b.ID); err != nil {
		t.Fatalf("delete bookmark s2: %v", err)
	}
	if list, _ := store.ListBookmarks(ctx, "s1"); len(list) != 1 {
		t.Fatalf("s1 bookmark should survive cross-scope delete, got %d", len(list))
	}
	// Cascade: deleting the session removes its bookmarks.
	if err := store.DeleteSession(ctx, "s1"); err != nil {
		t.Fatalf("delete session: %v", err)
	}
	if list, _ := store.ListBookmarks(ctx, "s1"); len(list) != 0 {
		t.Fatalf("expected bookmarks cascade-deleted, got %d", len(list))
	}
}

func TestSessionBookmarkOrdering(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx := context.Background()

	if err := store.SaveSession(ctx, SessionInfo{ID: "s1", ProjectID: testProjectID, Name: "S1", CreatedAt: time.Now().Format(time.RFC3339)}); err != nil {
		t.Fatalf("save session: %v", err)
	}

	if _, err := store.SaveBookmark(ctx, SessionBookmark{ID: "b2", SessionID: "s1", EventKey: "e2", Title: "two", CreatedAt: "2024-01-02T00:00:00Z"}); err != nil {
		t.Fatalf("save b2: %v", err)
	}
	if _, err := store.SaveBookmark(ctx, SessionBookmark{ID: "b1", SessionID: "s1", EventKey: "e1", Title: "one", CreatedAt: "2024-01-01T00:00:00Z"}); err != nil {
		t.Fatalf("save b1: %v", err)
	}

	list, err := store.ListBookmarks(ctx, "s1")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 2 || list[0].EventKey != "e1" || list[1].EventKey != "e2" {
		t.Fatalf("unexpected ordering: %+v", list)
	}
}

func TestSessionBookmarkReAddReplaces(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx := context.Background()

	if err := store.SaveSession(ctx, SessionInfo{ID: "s1", ProjectID: testProjectID, Name: "S1", CreatedAt: time.Now().Format(time.RFC3339)}); err != nil {
		t.Fatalf("save session: %v", err)
	}

	if _, err := store.SaveBookmark(ctx, SessionBookmark{ID: "b1", SessionID: "s1", EventKey: "e1", Title: "one", CreatedAt: "2024-01-01T00:00:00Z"}); err != nil {
		t.Fatalf("save b1: %v", err)
	}
	// Re-adding the same event key must replace the row rather than create a
	// duplicate (unique index on session_id + event_key).
	if _, err := store.SaveBookmark(ctx, SessionBookmark{ID: "b2", SessionID: "s1", EventKey: "e1", Title: "two", CreatedAt: "2024-01-02T00:00:00Z"}); err != nil {
		t.Fatalf("save b2: %v", err)
	}

	list, err := store.ListBookmarks(ctx, "s1")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 bookmark after re-add, got %d", len(list))
	}
	if list[0].ID != "b2" || list[0].Title != "two" {
		t.Fatalf("re-add should replace the row: %+v", list[0])
	}
}
