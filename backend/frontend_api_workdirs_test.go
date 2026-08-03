package backend

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/v0lka/c0wrk/backend/project"
	"github.com/v0lka/c0wrk/backend/session"
)

// workDirsHarness wires a FrontendAPI with real in-memory project + session
// stores, a real project row, and a real session row (satisfying the FK
// constraints on the work-directory tables).
type workDirsHarness struct {
	ctx          context.Context
	db           *sql.DB
	projStore    *project.SQLiteProjectStore
	sessionStore *session.SQLiteSessionStore
	api          *FrontendAPI
	projectID    string
	sessionID    string
	events       []string // captured emitEvent names (single-goroutine)
}

func (h *workDirsHarness) emitCount(name string) int {
	n := 0
	for _, e := range h.events {
		if e == name {
			n++
		}
	}
	return n
}

func newWorkDirsHarness(t *testing.T) *workDirsHarness {
	t.Helper()
	ctx := context.Background()
	db := openProjectSwitchTestDB(t)

	projStore, err := project.NewSQLiteProjectStore(db)
	if err != nil {
		_ = db.Close()
		t.Fatalf("failed to create project store: %v", err)
	}
	sessionStore, err := session.NewSQLiteSessionStore(db)
	if err != nil {
		_ = db.Close()
		t.Fatalf("failed to create session store: %v", err)
	}

	agentDir := t.TempDir()
	projectManager := project.NewManager(projStore, agentDir, nil)
	created, err := projectManager.CreateProject("Work Dirs Project", "")
	if err != nil {
		_ = db.Close()
		t.Fatalf("failed to create test project: %v", err)
	}

	const sessionID = "wd-test-session"
	if err := sessionStore.SaveSession(ctx, session.SessionInfo{
		ID:           sessionID,
		ProjectID:    created.ID,
		Name:         "Work Dirs Session",
		CreatedAt:    "2024-01-01T00:00:00Z",
		LastActiveAt: "2024-01-01T00:00:00Z",
	}); err != nil {
		_ = db.Close()
		t.Fatalf("failed to create test session: %v", err)
	}

	h := &workDirsHarness{
		ctx:          ctx,
		db:           db,
		projStore:    projStore,
		sessionStore: sessionStore,
		projectID:    created.ID,
		sessionID:    sessionID,
	}
	h.api = &FrontendAPI{
		store:          sessionStore,
		projStore:      projStore,
		projectManager: projectManager,
		agentDir:       agentDir,
		appCtx:         func() context.Context { return ctx },
		emitEvent: func(name string, _ ...any) {
			h.events = append(h.events, name)
		},
	}
	t.Cleanup(func() { _ = db.Close() })
	return h
}

func (h *workDirsHarness) existingDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	return dir
}

func TestAddWorkDirectory_ProjectScope_SuccessAndList(t *testing.T) {
	h := newWorkDirsHarness(t)
	dir := h.existingDir(t)

	if err := h.api.AddWorkDirectory("project", h.projectID, dir, "build artifacts"); err != nil {
		t.Fatalf("AddWorkDirectory project: %v", err)
	}

	recs, err := h.api.ListProjectWorkDirectories(h.projectID)
	if err != nil {
		t.Fatalf("ListProjectWorkDirectories: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("expected 1 record, got %d", len(recs))
	}
	// AddWorkDirectory normalizes the path (absolute, cleaned, symlink-resolved),
	// so the stored path is the resolved form of the input directory.
	wantPath, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("resolve symlinks: %v", err)
	}
	if recs[0].Path != wantPath || recs[0].Description != "build artifacts" {
		t.Fatalf("unexpected record: %#v", recs[0])
	}
	if recs[0].ID == "" || recs[0].CreatedAt == "" {
		t.Fatalf("expected generated id/created_at, got %#v", recs[0])
	}
	if h.emitCount(EventWorkDirsChanged) != 1 {
		t.Fatalf("expected 1 workdirs:changed emission, got %d", h.emitCount(EventWorkDirsChanged))
	}
}

func TestAddWorkDirectory_SessionScope_SuccessAndList(t *testing.T) {
	h := newWorkDirsHarness(t)
	dir := h.existingDir(t)

	if err := h.api.AddWorkDirectory("session", h.sessionID, dir, "scratch space"); err != nil {
		t.Fatalf("AddWorkDirectory session: %v", err)
	}

	recs, err := h.api.ListSessionWorkDirectories(h.sessionID)
	if err != nil {
		t.Fatalf("ListSessionWorkDirectories: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("expected 1 record, got %d", len(recs))
	}
	wantPath, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("resolve symlinks: %v", err)
	}
	if recs[0].Path != wantPath || recs[0].Description != "scratch space" {
		t.Fatalf("unexpected record: %#v", recs[0])
	}
	if h.emitCount(EventWorkDirsChanged) != 1 {
		t.Fatalf("expected 1 workdirs:changed emission, got %d", h.emitCount(EventWorkDirsChanged))
	}
}

func TestAddWorkDirectory_RejectsInvalidInput(t *testing.T) {
	h := newWorkDirsHarness(t)
	dir := h.existingDir(t)
	// A regular file (not a directory) for the "not a directory" case.
	filePath := filepath.Join(t.TempDir(), "not-a-dir.txt")
	if err := os.WriteFile(filePath, []byte("x"), 0o644); err != nil {
		t.Fatalf("setup file: %v", err)
	}

	cases := []struct {
		name        string
		scope       string
		ownerID     string
		path        string
		description string
		wantSubstr  string
	}{
		{"empty path", "project", h.projectID, "   ", "desc", "path is required"},
		{"empty description", "project", h.projectID, dir, "  ", "description is required"},
		{"nonexistent path", "project", h.projectID, "/nonexistent/does/not/exist/xyz", "desc", "work directory does not exist"},
		{"not a directory", "session", h.sessionID, filePath, "desc", "not a directory"},
		{"unknown scope", "galaxy", h.projectID, dir, "desc", "unknown scope"},
		{"project scope with NoProjectID", "project", project.NoProjectID, dir, "desc", "not available for No Project"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := h.api.AddWorkDirectory(tc.scope, tc.ownerID, tc.path, tc.description)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tc.wantSubstr) {
				t.Fatalf("error %q does not contain %q", err.Error(), tc.wantSubstr)
			}
		})
	}

	// None of the rejected calls should have emitted or persisted anything.
	if h.emitCount(EventWorkDirsChanged) != 0 {
		t.Fatalf("expected no emissions on validation failures, got %d", h.emitCount(EventWorkDirsChanged))
	}
	recs, err := h.api.ListProjectWorkDirectories(h.projectID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(recs) != 0 {
		t.Fatalf("expected no persisted records, got %d", len(recs))
	}
}

func TestUpdateWorkDirectoryDescription_ChangesStoredValue(t *testing.T) {
	h := newWorkDirsHarness(t)
	dir := h.existingDir(t)

	if err := h.api.AddWorkDirectory("session", h.sessionID, dir, "initial"); err != nil {
		t.Fatalf("AddWorkDirectory: %v", err)
	}
	recs, err := h.api.ListSessionWorkDirectories(h.sessionID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("expected 1 record, got %d", len(recs))
	}
	id := recs[0].ID

	if err := h.api.UpdateWorkDirectoryDescription("session", h.sessionID, id, "updated description"); err != nil {
		t.Fatalf("UpdateWorkDirectoryDescription: %v", err)
	}
	updated, err := h.api.ListSessionWorkDirectories(h.sessionID)
	if err != nil {
		t.Fatalf("list after update: %v", err)
	}
	if updated[0].Description != "updated description" {
		t.Fatalf("description not updated: got %q", updated[0].Description)
	}
	if h.emitCount(EventWorkDirsChanged) != 2 { // add + update
		t.Fatalf("expected 2 emissions, got %d", h.emitCount(EventWorkDirsChanged))
	}
}

func TestUpdateWorkDirectoryDescription_RejectsEmpty(t *testing.T) {
	h := newWorkDirsHarness(t)
	dir := h.existingDir(t)

	if err := h.api.AddWorkDirectory("project", h.projectID, dir, "initial"); err != nil {
		t.Fatalf("AddWorkDirectory: %v", err)
	}
	recs, err := h.api.ListProjectWorkDirectories(h.projectID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	id := recs[0].ID

	if err := h.api.UpdateWorkDirectoryDescription("project", h.projectID, id, "   "); err == nil {
		t.Fatal("expected error for empty description, got nil")
	}
	// Rejecting empty must not emit.
	if h.emitCount(EventWorkDirsChanged) != 1 {
		t.Fatalf("expected exactly 1 emission (add only), got %d", h.emitCount(EventWorkDirsChanged))
	}
}

func TestDeleteWorkDirectory_RemovesRow(t *testing.T) {
	h := newWorkDirsHarness(t)
	dir := h.existingDir(t)

	if err := h.api.AddWorkDirectory("project", h.projectID, dir, "to delete"); err != nil {
		t.Fatalf("AddWorkDirectory: %v", err)
	}
	recs, err := h.api.ListProjectWorkDirectories(h.projectID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("expected 1 record, got %d", len(recs))
	}
	id := recs[0].ID

	if err := h.api.DeleteWorkDirectory("project", h.projectID, id); err != nil {
		t.Fatalf("DeleteWorkDirectory: %v", err)
	}
	after, err := h.api.ListProjectWorkDirectories(h.projectID)
	if err != nil {
		t.Fatalf("list after delete: %v", err)
	}
	if len(after) != 0 {
		t.Fatalf("expected 0 records after delete, got %d", len(after))
	}
	if h.emitCount(EventWorkDirsChanged) != 2 { // add + delete
		t.Fatalf("expected 2 emissions, got %d", h.emitCount(EventWorkDirsChanged))
	}
}

func TestListWorkDirectories_NilStoreReturnsError(t *testing.T) {
	f := &FrontendAPI{} // no stores wired — mirrors early startup
	if _, err := f.ListProjectWorkDirectories("p1"); err == nil {
		t.Fatal("expected error when project store is nil")
	}
	if _, err := f.ListSessionWorkDirectories("s1"); err == nil {
		t.Fatal("expected error when session store is nil")
	}
}

func TestListWorkDirectories_EmptyByDefault(t *testing.T) {
	h := newWorkDirsHarness(t)

	projs, err := h.api.ListProjectWorkDirectories(h.projectID)
	if err != nil {
		t.Fatalf("list project: %v", err)
	}
	if len(projs) != 0 {
		t.Fatalf("expected empty project list, got %d", len(projs))
	}
	sess, err := h.api.ListSessionWorkDirectories(h.sessionID)
	if err != nil {
		t.Fatalf("list session: %v", err)
	}
	if len(sess) != 0 {
		t.Fatalf("expected empty session list, got %d", len(sess))
	}
}

func TestAddWorkDirectory_NormalizesPath(t *testing.T) {
	h := newWorkDirsHarness(t)
	dir := h.existingDir(t)

	// Pass a non-canonical path (trailing slash). The stored path must be
	// cleaned and symlink-resolved — this is what the security-containment
	// match compares against, so a non-canonical root would silently fail.
	if err := h.api.AddWorkDirectory("project", h.projectID, dir+"/", "desc"); err != nil {
		t.Fatalf("AddWorkDirectory: %v", err)
	}
	wantPath, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("resolve symlinks: %v", err)
	}
	recs, err := h.api.ListProjectWorkDirectories(h.projectID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(recs) != 1 || recs[0].Path != wantPath {
		t.Fatalf("expected normalized path %q, got %#v", wantPath, recs)
	}
}

func TestAddWorkDirectory_RejectsDuplicatePath(t *testing.T) {
	h := newWorkDirsHarness(t)
	dir := h.existingDir(t)

	if err := h.api.AddWorkDirectory("project", h.projectID, dir, "first"); err != nil {
		t.Fatalf("first AddWorkDirectory: %v", err)
	}
	// Same path again, passed non-canonically (trailing slash). Normalization
	// makes both resolve to the same stored path, so the unique constraint fires.
	err := h.api.AddWorkDirectory("project", h.projectID, dir+"/", "second")
	if err == nil {
		t.Fatal("expected duplicate-rejection error, got nil")
	}
	if !strings.Contains(err.Error(), "already added") {
		t.Fatalf("expected 'already added' error, got %q", err.Error())
	}
	recs, err := h.api.ListProjectWorkDirectories(h.projectID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("expected exactly 1 record after duplicate, got %d", len(recs))
	}
	// The rejected insert must not emit (no spurious UI refetch).
	if h.emitCount(EventWorkDirsChanged) != 1 {
		t.Fatalf("expected 1 emission (first add only), got %d", h.emitCount(EventWorkDirsChanged))
	}
}

func TestDeleteWorkDirectory_ScopeGuardIgnoresWrongOwner(t *testing.T) {
	h := newWorkDirsHarness(t)
	dir := h.existingDir(t)

	if err := h.api.AddWorkDirectory("project", h.projectID, dir, "owned"); err != nil {
		t.Fatalf("AddWorkDirectory: %v", err)
	}
	recs, err := h.api.ListProjectWorkDirectories(h.projectID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("expected 1 record, got %d", len(recs))
	}
	id := recs[0].ID

	// Delete with a DIFFERENT project ID. The scope guard (WHERE id AND
	// project_id) must prevent the row owned by h.projectID from being removed.
	if err := h.api.DeleteWorkDirectory("project", "some-other-project-id", id); err != nil {
		t.Fatalf("DeleteWorkDirectory with wrong owner should not error: %v", err)
	}
	after, err := h.api.ListProjectWorkDirectories(h.projectID)
	if err != nil {
		t.Fatalf("list after guarded delete: %v", err)
	}
	if len(after) != 1 || after[0].ID != id {
		t.Fatalf("scope guard failed: record should survive, got %#v", after)
	}

	// The correct owner can still update it.
	if err := h.api.UpdateWorkDirectoryDescription("project", h.projectID, id, "updated"); err != nil {
		t.Fatalf("UpdateWorkDirectoryDescription correct owner: %v", err)
	}
	after, err = h.api.ListProjectWorkDirectories(h.projectID)
	if err != nil {
		t.Fatalf("list after update: %v", err)
	}
	if after[0].Description != "updated" {
		t.Fatalf("description should be updated, got %q", after[0].Description)
	}
}
