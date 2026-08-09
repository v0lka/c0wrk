package review

import (
	"context"
	"testing"
)

func TestCloneReview_FullCopy(t *testing.T) {
	store, db, cleanup := setupTestStore(t)
	defer cleanup()
	ctx := context.Background()

	insertSession(t, db, "src")
	insertSession(t, db, "dst")

	// Source review: status + general + two hunk comments.
	if err := store.SetReviewStatus(ctx, "src", StatusSubmitted); err != nil {
		t.Fatalf("SetReviewStatus: %v", err)
	}
	if err := store.UpsertGeneralComment(ctx, "src", "general body"); err != nil {
		t.Fatalf("UpsertGeneralComment: %v", err)
	}
	id1, err := store.UpsertHunkComment(ctx, "src", "main.go", "hunk-1", "comment one")
	if err != nil {
		t.Fatalf("UpsertHunkComment 1: %v", err)
	}
	if _, err := store.UpsertHunkComment(ctx, "src", "main.go", "hunk-2", "comment two"); err != nil {
		t.Fatalf("UpsertHunkComment 2: %v", err)
	}

	if err := store.CloneReview(ctx, "src", "dst"); err != nil {
		t.Fatalf("CloneReview: %v", err)
	}

	got, err := store.GetReview(ctx, "dst")
	if err != nil {
		t.Fatalf("GetReview dst: %v", err)
	}
	if got.Status != StatusSubmitted {
		t.Errorf("status=%q want %q", got.Status, StatusSubmitted)
	}
	if got.GeneralComment != "general body" {
		t.Errorf("general comment=%q want %q", got.GeneralComment, "general body")
	}
	if len(got.HunkComments) != 2 {
		t.Fatalf("expected 2 hunk comments, got %d", len(got.HunkComments))
	}
	// Hunk comment ids must be regenerated (differ from source).
	for _, hc := range got.HunkComments {
		if hc.ID == id1 {
			t.Error("hunk comment id should be regenerated, not copied verbatim")
		}
		if hc.SessionID != "dst" {
			t.Errorf("hunk comment session_id=%q want dst", hc.SessionID)
		}
	}
}

func TestCloneReview_GeneralIdDeterministic(t *testing.T) {
	store, db, cleanup := setupTestStore(t)
	defer cleanup()
	ctx := context.Background()

	insertSession(t, db, "src")
	insertSession(t, db, "dst")

	if err := store.UpsertGeneralComment(ctx, "src", "original"); err != nil {
		t.Fatalf("UpsertGeneralComment: %v", err)
	}
	if err := store.CloneReview(ctx, "src", "dst"); err != nil {
		t.Fatalf("CloneReview: %v", err)
	}

	// After clone, upserting the general comment must update in place, not
	// create a duplicate row.
	if err := store.UpsertGeneralComment(ctx, "dst", "edited"); err != nil {
		t.Fatalf("UpsertGeneralComment dst: %v", err)
	}
	got, err := store.GetReview(ctx, "dst")
	if err != nil {
		t.Fatalf("GetReview dst: %v", err)
	}
	if got.GeneralComment != "edited" {
		t.Errorf("general comment=%q want edited", got.GeneralComment)
	}

	// Exactly one general row for dst.
	var count int
	if err := store.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM review_comments WHERE session_id = ? AND kind = 'general'`, "dst",
	).Scan(&count); err != nil {
		t.Fatalf("count general: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 general comment row, got %d (duplicate created)", count)
	}
}

// TestCloneReview_FileIdDeterministic verifies that cloned file comments keep
// the deterministic id, so a subsequent UpsertFileComment on the fork updates
// the row in place instead of inserting a duplicate (the row id must match
// fileCommentID(dstSessionID, filePath)).
func TestCloneReview_FileIdDeterministic(t *testing.T) {
	store, db, cleanup := setupTestStore(t)
	defer cleanup()
	ctx := context.Background()

	insertSession(t, db, "src")
	insertSession(t, db, "dst")

	if _, err := store.UpsertFileComment(ctx, "src", "main.go", "original"); err != nil {
		t.Fatalf("UpsertFileComment src: %v", err)
	}
	if err := store.CloneReview(ctx, "src", "dst"); err != nil {
		t.Fatalf("CloneReview: %v", err)
	}

	// After clone, upserting the same file comment on the fork must update in
	// place, not create a duplicate row.
	if _, err := store.UpsertFileComment(ctx, "dst", "main.go", "edited"); err != nil {
		t.Fatalf("UpsertFileComment dst: %v", err)
	}
	got, err := store.GetReview(ctx, "dst")
	if err != nil {
		t.Fatalf("GetReview dst: %v", err)
	}
	if len(got.FileComments) != 1 || got.FileComments[0].Body != "edited" {
		t.Errorf("file comments=%+v want exactly one with body %q", got.FileComments, "edited")
	}

	// Exactly one file row for dst/main.go.
	var count int
	if err := store.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM review_comments WHERE session_id = ? AND kind = 'file' AND file_path = ?`,
		"dst", "main.go",
	).Scan(&count); err != nil {
		t.Fatalf("count file: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 file comment row, got %d (duplicate created)", count)
	}
}

func TestCloneReview_NoSourceData_NoOp(t *testing.T) {
	store, db, cleanup := setupTestStore(t)
	defer cleanup()
	ctx := context.Background()

	insertSession(t, db, "src")
	insertSession(t, db, "dst")

	// No review data for src — clone must be a no-op without error.
	if err := store.CloneReview(ctx, "src", "dst"); err != nil {
		t.Fatalf("CloneReview no-data: %v", err)
	}

	got, err := store.GetReview(ctx, "dst")
	if err != nil {
		t.Fatalf("GetReview dst: %v", err)
	}
	if got.Status != StatusActive {
		t.Errorf("expected default active status, got %q", got.Status)
	}
	if got.GeneralComment != "" || len(got.HunkComments) != 0 {
		t.Errorf("expected empty review, got %+v", got)
	}
}

func TestCloneReview_SourceUntouched(t *testing.T) {
	store, db, cleanup := setupTestStore(t)
	defer cleanup()
	ctx := context.Background()

	insertSession(t, db, "src")
	insertSession(t, db, "dst")

	if err := store.UpsertGeneralComment(ctx, "src", "keep me"); err != nil {
		t.Fatalf("UpsertGeneralComment: %v", err)
	}
	if _, err := store.UpsertHunkComment(ctx, "src", "a.go", "h1", "c1"); err != nil {
		t.Fatalf("UpsertHunkComment: %v", err)
	}

	before, err := store.GetReview(ctx, "src")
	if err != nil {
		t.Fatalf("GetReview src before: %v", err)
	}
	if err := store.CloneReview(ctx, "src", "dst"); err != nil {
		t.Fatalf("CloneReview: %v", err)
	}
	after, err := store.GetReview(ctx, "src")
	if err != nil {
		t.Fatalf("GetReview src after: %v", err)
	}

	if after.GeneralComment != before.GeneralComment {
		t.Errorf("source general comment changed: %q -> %q", before.GeneralComment, after.GeneralComment)
	}
	if len(after.HunkComments) != len(before.HunkComments) {
		t.Errorf("source hunk count changed: %d -> %d", len(before.HunkComments), len(after.HunkComments))
	}
}
