package review

import (
	"context"
	"database/sql"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// openTestDB opens an in-memory SQLite database with the pragmas required for
// FK cascades (foreign_keys) and WAL mode (matching backend.OpenDatabase).
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}
	db.SetMaxOpenConns(1)
	for _, pragma := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA foreign_keys=ON",
	} {
		if _, err := db.ExecContext(context.Background(), pragma); err != nil {
			_ = db.Close()
			t.Fatalf("failed to apply %q: %v", pragma, err)
		}
	}
	return db
}

// createSessionsTable mimics the sessions table owned by the session store so
// that the review tables' FK references resolve and ON DELETE CASCADE can be
// exercised. Only the referenced id column is required for FK semantics.
func createSessionsTable(t *testing.T, db *sql.DB) {
	t.Helper()
	_, err := db.ExecContext(context.Background(), `
		CREATE TABLE IF NOT EXISTS sessions (
			id TEXT PRIMARY KEY
		)`)
	if err != nil {
		t.Fatalf("failed to create sessions table: %v", err)
	}
}

// insertSession inserts a session row so review rows have a valid FK parent.
func insertSession(t *testing.T, db *sql.DB, id string) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(),
		`INSERT INTO sessions (id) VALUES (?)`, id); err != nil {
		t.Fatalf("failed to insert session %q: %v", id, err)
	}
}

// setupTestStore returns a store backed by a fresh in-memory db with the
// sessions table pre-created, plus a cleanup func.
func setupTestStore(t *testing.T) (store *SQLiteReviewStore, db *sql.DB, cleanup func()) {
	t.Helper()
	db = openTestDB(t)
	createSessionsTable(t, db)
	store, err := NewSQLiteReviewStore(db)
	if err != nil {
		_ = db.Close()
		t.Fatalf("failed to create review store: %v", err)
	}
	cleanup = func() { _ = db.Close() }
	return
}

func TestCreateTablesIdempotent(t *testing.T) {
	db := openTestDB(t)
	defer func() { _ = db.Close() }()
	createSessionsTable(t, db)

	if _, err := NewSQLiteReviewStore(db); err != nil {
		t.Fatalf("first NewSQLiteReviewStore: %v", err)
	}
	// A second construction must not error on existing tables.
	if _, err := NewSQLiteReviewStore(db); err != nil {
		t.Fatalf("second NewSQLiteReviewStore (idempotent): %v", err)
	}
}

func TestGetReviewEmptyDefaultsToActive(t *testing.T) {
	store, _, cleanup := setupTestStore(t)
	defer cleanup()

	rev, err := store.GetReview(context.Background(), "sess-1")
	if err != nil {
		t.Fatalf("GetReview: %v", err)
	}
	if rev.Status != StatusActive {
		t.Errorf("status = %q, want %q", rev.Status, StatusActive)
	}
	if rev.GeneralComment != "" {
		t.Errorf("general comment = %q, want empty", rev.GeneralComment)
	}
	if rev.HunkComments == nil {
		t.Fatal("HunkComments should be non-nil for a session with no comments")
	}
	if len(rev.HunkComments) != 0 {
		t.Errorf("hunk comments len = %d, want 0", len(rev.HunkComments))
	}
}

func TestUpsertGeneralCommentRoundTrip(t *testing.T) {
	store, db, cleanup := setupTestStore(t)
	defer cleanup()
	insertSession(t, db, "sess-1")

	if err := store.UpsertGeneralComment(context.Background(), "sess-1", "looks good overall"); err != nil {
		t.Fatalf("UpsertGeneralComment: %v", err)
	}
	rev, err := store.GetReview(context.Background(), "sess-1")
	if err != nil {
		t.Fatalf("GetReview: %v", err)
	}
	if rev.GeneralComment != "looks good overall" {
		t.Errorf("general comment = %q, want %q", rev.GeneralComment, "looks good overall")
	}

	// Upsert replaces the existing general comment (no duplicate rows).
	if err := store.UpsertGeneralComment(context.Background(), "sess-1", "revised take"); err != nil {
		t.Fatalf("UpsertGeneralComment (replace): %v", err)
	}
	rev, err = store.GetReview(context.Background(), "sess-1")
	if err != nil {
		t.Fatalf("GetReview (replace): %v", err)
	}
	if rev.GeneralComment != "revised take" {
		t.Errorf("general comment = %q, want %q", rev.GeneralComment, "revised take")
	}

	// Exactly one general row should exist.
	var n int
	if err := db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM review_comments WHERE session_id = ? AND kind = 'general'`, "sess-1").Scan(&n); err != nil {
		t.Fatalf("count general: %v", err)
	}
	if n != 1 {
		t.Errorf("general rows = %d, want 1", n)
	}

	// Empty body clears the general comment.
	if err := store.UpsertGeneralComment(context.Background(), "sess-1", ""); err != nil {
		t.Fatalf("UpsertGeneralComment (clear): %v", err)
	}
	rev, err = store.GetReview(context.Background(), "sess-1")
	if err != nil {
		t.Fatalf("GetReview (clear): %v", err)
	}
	if rev.GeneralComment != "" {
		t.Errorf("general comment after clear = %q, want empty", rev.GeneralComment)
	}
}

func TestUpsertHunkCommentRoundTrip(t *testing.T) {
	store, db, cleanup := setupTestStore(t)
	defer cleanup()
	insertSession(t, db, "sess-1")

	id1, err := store.UpsertHunkComment(context.Background(), "sess-1", "main.go", "h1", "nit: rename")
	if err != nil {
		t.Fatalf("UpsertHunkComment: %v", err)
	}
	if id1 == "" {
		t.Fatal("returned id should not be empty on insert")
	}

	// Upserting the same (file, hunk) returns the same id and updates the body.
	id2, err := store.UpsertHunkComment(context.Background(), "sess-1", "main.go", "h1", "actually fine")
	if err != nil {
		t.Fatalf("UpsertHunkComment (update): %v", err)
	}
	if id2 != id1 {
		t.Errorf("upsert id changed: got %q, want %q", id2, id1)
	}

	// A second hunk comment is stored separately.
	if _, err := store.UpsertHunkComment(context.Background(), "sess-1", "util.go", "h2", "consider helper"); err != nil {
		t.Fatalf("UpsertHunkComment (second): %v", err)
	}

	rev, err := store.GetReview(context.Background(), "sess-1")
	if err != nil {
		t.Fatalf("GetReview: %v", err)
	}
	if len(rev.HunkComments) != 2 {
		t.Fatalf("hunk comments len = %d, want 2", len(rev.HunkComments))
	}

	// The updated body is reflected.
	var sawUpdated bool
	for _, hc := range rev.HunkComments {
		if hc.ID == id1 {
			sawUpdated = true
			if hc.Body != "actually fine" {
				t.Errorf("hunk body = %q, want %q", hc.Body, "actually fine")
			}
			if hc.FilePath != "main.go" || hc.HunkID != "h1" {
				t.Errorf("hunk scope = (%q,%q), want (main.go,h1)", hc.FilePath, hc.HunkID)
			}
		}
	}
	if !sawUpdated {
		t.Error("updated hunk comment not found in results")
	}

	// Empty body deletes the hunk comment.
	if _, err := store.UpsertHunkComment(context.Background(), "sess-1", "main.go", "h1", ""); err != nil {
		t.Fatalf("UpsertHunkComment (clear): %v", err)
	}
	rev, err = store.GetReview(context.Background(), "sess-1")
	if err != nil {
		t.Fatalf("GetReview (clear): %v", err)
	}
	if len(rev.HunkComments) != 1 {
		t.Errorf("hunk comments after clear = %d, want 1", len(rev.HunkComments))
	}

	// Defensive: ensure only one hunk row ever exists per (session, file, hunk).
	var n int
	if err := db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM review_comments WHERE session_id = ? AND kind = 'hunk' AND file_path = 'main.go' AND hunk_id = 'h1'`,
		"sess-1").Scan(&n); err != nil {
		t.Fatalf("count hunk: %v", err)
	}
	if n != 0 {
		t.Errorf("hunk rows for cleared scope = %d, want 0", n)
	}
}

func TestGeneralAndHunkCommentsCoexist(t *testing.T) {
	store, db, cleanup := setupTestStore(t)
	defer cleanup()
	insertSession(t, db, "sess-1")

	if err := store.UpsertGeneralComment(context.Background(), "sess-1", "overall"); err != nil {
		t.Fatalf("UpsertGeneralComment: %v", err)
	}
	if _, err := store.UpsertHunkComment(context.Background(), "sess-1", "a.go", "h1", "hunk nit"); err != nil {
		t.Fatalf("UpsertHunkComment: %v", err)
	}

	rev, err := store.GetReview(context.Background(), "sess-1")
	if err != nil {
		t.Fatalf("GetReview: %v", err)
	}
	if rev.GeneralComment != "overall" {
		t.Errorf("general = %q, want overall", rev.GeneralComment)
	}
	if len(rev.HunkComments) != 1 || rev.HunkComments[0].Body != "hunk nit" {
		t.Errorf("unexpected hunk comments: %+v", rev.HunkComments)
	}
}

func TestDeleteComment(t *testing.T) {
	store, db, cleanup := setupTestStore(t)
	defer cleanup()
	insertSession(t, db, "sess-1")

	id, err := store.UpsertHunkComment(context.Background(), "sess-1", "main.go", "h1", "fix me")
	if err != nil {
		t.Fatalf("UpsertHunkComment: %v", err)
	}
	if err := store.UpsertGeneralComment(context.Background(), "sess-1", "general"); err != nil {
		t.Fatalf("UpsertGeneralComment: %v", err)
	}

	if err := store.DeleteComment(context.Background(), id); err != nil {
		t.Fatalf("DeleteComment: %v", err)
	}
	rev, err := store.GetReview(context.Background(), "sess-1")
	if err != nil {
		t.Fatalf("GetReview: %v", err)
	}
	if len(rev.HunkComments) != 0 {
		t.Errorf("hunk comments after delete = %d, want 0", len(rev.HunkComments))
	}
	if rev.GeneralComment != "general" {
		t.Errorf("general should be untouched, got %q", rev.GeneralComment)
	}

	// Deleting a non-existent id is not an error.
	if err := store.DeleteComment(context.Background(), "does-not-exist"); err != nil {
		t.Errorf("deleting non-existent id should not error: %v", err)
	}
}

func TestSetReviewStatusRoundTrip(t *testing.T) {
	store, db, cleanup := setupTestStore(t)
	defer cleanup()
	insertSession(t, db, "sess-1")

	cases := []ReviewStatus{StatusActive, StatusSubmitted, StatusApproved, StatusActive}
	for _, st := range cases {
		if err := store.SetReviewStatus(context.Background(), "sess-1", st); err != nil {
			t.Fatalf("SetReviewStatus(%q): %v", st, err)
		}
		rev, err := store.GetReview(context.Background(), "sess-1")
		if err != nil {
			t.Fatalf("GetReview after status %q: %v", st, err)
		}
		if rev.Status != st {
			t.Errorf("status = %q, want %q", rev.Status, st)
		}
		if rev.UpdatedAt == "" {
			t.Errorf("UpdatedAt should be set for status %q", st)
		}
	}

	// Invalid status is rejected.
	if err := store.SetReviewStatus(context.Background(), "sess-1", "bogus"); err == nil {
		t.Error("SetReviewStatus with invalid status should error")
	}

	// Exactly one status row after multiple upserts.
	var n int
	if err := db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM review_state WHERE session_id = ?`, "sess-1").Scan(&n); err != nil {
		t.Fatalf("count state: %v", err)
	}
	if n != 1 {
		t.Errorf("state rows = %d, want 1", n)
	}
}

func TestClearCommentsPreservesStatus(t *testing.T) {
	store, db, cleanup := setupTestStore(t)
	defer cleanup()
	insertSession(t, db, "sess-1")

	if err := store.UpsertGeneralComment(context.Background(), "sess-1", "general"); err != nil {
		t.Fatalf("UpsertGeneralComment: %v", err)
	}
	if _, err := store.UpsertHunkComment(context.Background(), "sess-1", "a.go", "h1", "hunk"); err != nil {
		t.Fatalf("UpsertHunkComment: %v", err)
	}
	if err := store.SetReviewStatus(context.Background(), "sess-1", StatusSubmitted); err != nil {
		t.Fatalf("SetReviewStatus: %v", err)
	}

	if err := store.ClearComments(context.Background(), "sess-1"); err != nil {
		t.Fatalf("ClearComments: %v", err)
	}
	rev, err := store.GetReview(context.Background(), "sess-1")
	if err != nil {
		t.Fatalf("GetReview: %v", err)
	}
	if rev.GeneralComment != "" || len(rev.HunkComments) != 0 {
		t.Errorf("comments not cleared: %+v", rev)
	}
	// Status must survive ClearComments.
	if rev.Status != StatusSubmitted {
		t.Errorf("status = %q, want %q", rev.Status, StatusSubmitted)
	}
}

func TestClearReviewResetsEverything(t *testing.T) {
	store, db, cleanup := setupTestStore(t)
	defer cleanup()
	insertSession(t, db, "sess-1")

	if err := store.UpsertGeneralComment(context.Background(), "sess-1", "general"); err != nil {
		t.Fatalf("UpsertGeneralComment: %v", err)
	}
	if _, err := store.UpsertHunkComment(context.Background(), "sess-1", "a.go", "h1", "hunk"); err != nil {
		t.Fatalf("UpsertHunkComment: %v", err)
	}
	if err := store.SetReviewStatus(context.Background(), "sess-1", StatusApproved); err != nil {
		t.Fatalf("SetReviewStatus: %v", err)
	}

	if err := store.ClearReview(context.Background(), "sess-1"); err != nil {
		t.Fatalf("ClearReview: %v", err)
	}
	rev, err := store.GetReview(context.Background(), "sess-1")
	if err != nil {
		t.Fatalf("GetReview: %v", err)
	}
	if rev.GeneralComment != "" || len(rev.HunkComments) != 0 {
		t.Errorf("comments not cleared: %+v", rev)
	}
	if rev.Status != StatusActive {
		t.Errorf("status = %q, want reset to %q", rev.Status, StatusActive)
	}

	// No state row should remain.
	var n int
	if err := db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM review_state WHERE session_id = ?`, "sess-1").Scan(&n); err != nil {
		t.Fatalf("count state: %v", err)
	}
	if n != 0 {
		t.Errorf("state rows after ClearReview = %d, want 0", n)
	}
}

func TestCascadeOnSessionDelete(t *testing.T) {
	store, db, cleanup := setupTestStore(t)
	defer cleanup()
	insertSession(t, db, "sess-1")
	insertSession(t, db, "sess-2")

	// Populate both sessions.
	if err := store.UpsertGeneralComment(context.Background(), "sess-1", "g1"); err != nil {
		t.Fatalf("UpsertGeneralComment: %v", err)
	}
	if _, err := store.UpsertHunkComment(context.Background(), "sess-1", "a.go", "h1", "h1"); err != nil {
		t.Fatalf("UpsertHunkComment: %v", err)
	}
	if err := store.SetReviewStatus(context.Background(), "sess-1", StatusSubmitted); err != nil {
		t.Fatalf("SetReviewStatus: %v", err)
	}
	if err := store.UpsertGeneralComment(context.Background(), "sess-2", "g2"); err != nil {
		t.Fatalf("UpsertGeneralComment: %v", err)
	}

	// Delete session 1; its review rows must cascade away, session 2 untouched.
	if _, err := db.ExecContext(context.Background(), `DELETE FROM sessions WHERE id = ?`, "sess-1"); err != nil {
		t.Fatalf("delete session: %v", err)
	}

	rev, err := store.GetReview(context.Background(), "sess-1")
	if err != nil {
		t.Fatalf("GetReview sess-1 after cascade: %v", err)
	}
	if rev.GeneralComment != "" || len(rev.HunkComments) != 0 {
		t.Errorf("sess-1 review should be empty after cascade: %+v", rev)
	}
	if rev.Status != StatusActive {
		t.Errorf("sess-1 status = %q, want %q after cascade", rev.Status, StatusActive)
	}

	rev2, err := store.GetReview(context.Background(), "sess-2")
	if err != nil {
		t.Fatalf("GetReview sess-2 after cascade: %v", err)
	}
	if rev2.GeneralComment != "g2" {
		t.Errorf("sess-2 general = %q, want g2 (should be untouched)", rev2.GeneralComment)
	}

	// Verify the DB has no orphaned rows for sess-1.
	var n int
	if err := db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM review_comments WHERE session_id = ?`, "sess-1").Scan(&n); err != nil {
		t.Fatalf("count orphaned comments: %v", err)
	}
	if n != 0 {
		t.Errorf("orphaned review_comments for deleted session = %d, want 0", n)
	}
}

// TestSurvivesReopen verifies that data persists across closing the store and
// re-opening a new store handle on the same on-disk database file.
func TestSurvivesReopen(t *testing.T) {
	dir := t.TempDir()
	dbPath := dir + "/review_test.db"

	func() {
		db, err := sql.Open("sqlite", dbPath)
		if err != nil {
			t.Fatalf("open db: %v", err)
		}
		defer func() { _ = db.Close() }()
		db.SetMaxOpenConns(1)
		for _, pragma := range []string{"PRAGMA journal_mode=WAL", "PRAGMA foreign_keys=ON"} {
			if _, err := db.ExecContext(context.Background(), pragma); err != nil {
				t.Fatalf("pragma %q: %v", pragma, err)
			}
		}
		createSessionsTable(t, db)
		insertSession(t, db, "sess-1")

		store, err := NewSQLiteReviewStore(db)
		if err != nil {
			t.Fatalf("NewSQLiteReviewStore: %v", err)
		}
		if err := store.UpsertGeneralComment(context.Background(), "sess-1", "persistent"); err != nil {
			t.Fatalf("UpsertGeneralComment: %v", err)
		}
		id, err := store.UpsertHunkComment(context.Background(), "sess-1", "a.go", "h1", "hunk-persist")
		if err != nil {
			t.Fatalf("UpsertHunkComment: %v", err)
		}
		if err := store.SetReviewStatus(context.Background(), "sess-1", StatusApproved); err != nil {
			t.Fatalf("SetReviewStatus: %v", err)
		}
		_ = id
	}()

	// Reopen a fresh DB handle + store and confirm everything survived.
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("reopen db: %v", err)
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)
	for _, pragma := range []string{"PRAGMA journal_mode=WAL", "PRAGMA foreign_keys=ON"} {
		if _, err := db.ExecContext(context.Background(), pragma); err != nil {
			t.Fatalf("reopen pragma %q: %v", pragma, err)
		}
	}

	store, err := NewSQLiteReviewStore(db)
	if err != nil {
		t.Fatalf("reopen NewSQLiteReviewStore: %v", err)
	}
	rev, err := store.GetReview(context.Background(), "sess-1")
	if err != nil {
		t.Fatalf("reopen GetReview: %v", err)
	}
	if rev.GeneralComment != "persistent" {
		t.Errorf("general = %q, want persistent", rev.GeneralComment)
	}
	if len(rev.HunkComments) != 1 || rev.HunkComments[0].Body != "hunk-persist" {
		t.Errorf("hunk comments after reopen: %+v", rev.HunkComments)
	}
	if rev.Status != StatusApproved {
		t.Errorf("status = %q, want %q", rev.Status, StatusApproved)
	}

	// Sanity: timestamps are parseable RFC 3339.
	if _, err := time.Parse(time.RFC3339, rev.UpdatedAt); err != nil {
		t.Errorf("UpdatedAt %q is not RFC 3339: %v", rev.UpdatedAt, err)
	}
}

func TestValidationErrors(t *testing.T) {
	store, _, cleanup := setupTestStore(t)
	defer cleanup()

	ctx := context.Background()
	if err := store.UpsertGeneralComment(ctx, "", "x"); err == nil {
		t.Error("UpsertGeneralComment with empty session should error")
	}
	if _, err := store.UpsertHunkComment(ctx, "s", "", "h", "x"); err == nil {
		t.Error("UpsertHunkComment with empty file path should error")
	}
	if _, err := store.UpsertHunkComment(ctx, "s", "f", "", "x"); err == nil {
		t.Error("UpsertHunkComment with empty hunk id should error")
	}
	if err := store.DeleteComment(ctx, ""); err == nil {
		t.Error("DeleteComment with empty id should error")
	}
	if err := store.SetReviewStatus(ctx, "", StatusActive); err == nil {
		t.Error("SetReviewStatus with empty session should error")
	}
	if err := store.ClearComments(ctx, ""); err == nil {
		t.Error("ClearComments with empty session should error")
	}
	if err := store.ClearReview(ctx, ""); err == nil {
		t.Error("ClearReview with empty session should error")
	}
}
