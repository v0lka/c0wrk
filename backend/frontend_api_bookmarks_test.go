package backend

import (
	"context"
	"testing"
	"time"

	"github.com/v0lka/c0wrk/backend/project"
	"github.com/v0lka/c0wrk/backend/session"
)

// newBookmarkTestAPI builds a FrontendAPI with a real session store seeded with
// one session, enough to exercise the bookmark RPC methods.
func newBookmarkTestAPI(t *testing.T) (*FrontendAPI, *session.SQLiteSessionStore, interface{ Close() error }) {
	t.Helper()
	ctx := context.Background()

	db := openProjectSwitchTestDB(t)

	projStore, err := project.NewSQLiteProjectStore(db)
	if err != nil {
		_ = db.Close()
		t.Fatalf("project store: %v", err)
	}
	sessStore, err := session.NewSQLiteSessionStore(db)
	if err != nil {
		_ = db.Close()
		t.Fatalf("session store: %v", err)
	}

	pm := project.NewManager(projStore, t.TempDir(), nil)
	created, err := pm.CreateProject("Bookmarks", "")
	if err != nil {
		_ = db.Close()
		t.Fatalf("create project: %v", err)
	}
	if err := sessStore.SaveSession(ctx, session.SessionInfo{
		ID: "sess-1", ProjectID: created.ID, Name: "Sess", CreatedAt: time.Now().Format(time.RFC3339),
	}); err != nil {
		_ = db.Close()
		t.Fatalf("save session: %v", err)
	}

	return &FrontendAPI{store: sessStore}, sessStore, db
}

func TestFrontendAPI_Bookmarks(t *testing.T) {
	api, _, db := newBookmarkTestAPI(t)
	defer func() { _ = db.Close() }()

	list, err := api.ListBookmarks("sess-1")
	if err != nil {
		t.Fatalf("list bookmarks: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("expected empty list, got %d", len(list))
	}

	b, err := api.AddBookmark("sess-1", "assistant-1", "Answer")
	if err != nil {
		t.Fatalf("add bookmark: %v", err)
	}
	if b.ID == "" || b.EventKey != "assistant-1" || b.Title != "Answer" {
		t.Fatalf("unexpected bookmark: %+v", b)
	}

	list, err = api.ListBookmarks("sess-1")
	if err != nil {
		t.Fatalf("list bookmarks: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 bookmark, got %d", len(list))
	}

	if err := api.RenameBookmark("sess-1", b.ID, "Renamed"); err != nil {
		t.Fatalf("rename bookmark: %v", err)
	}
	list, _ = api.ListBookmarks("sess-1")
	if list[0].Title != "Renamed" {
		t.Fatalf("expected renamed title, got %q", list[0].Title)
	}

	if err := api.DeleteBookmark("sess-1", b.ID); err != nil {
		t.Fatalf("delete bookmark: %v", err)
	}
	list, _ = api.ListBookmarks("sess-1")
	if len(list) != 0 {
		t.Fatalf("expected empty after delete, got %d", len(list))
	}
}

func TestFrontendAPI_AddBookmark_Validation(t *testing.T) {
	api, _, db := newBookmarkTestAPI(t)
	defer func() { _ = db.Close() }()

	if _, err := api.AddBookmark("sess-1", "   ", "Title"); err == nil {
		t.Fatal("expected error for empty event key")
	}
	if err := api.RenameBookmark("sess-1", "x", "   "); err == nil {
		t.Fatal("expected error for empty title")
	}
}
