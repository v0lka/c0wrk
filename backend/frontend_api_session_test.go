package backend

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/v0lka/c0wrk/backend/project"
	"github.com/v0lka/c0wrk/backend/review"
	"github.com/v0lka/c0wrk/backend/session"
)

// newForkTestAPI builds a minimal FrontendAPI with real session/review stores
// and a stub manager (orchestrator factory is never invoked by ForkSession).
// The shared db owns a project + session row so FK constraints hold.
func newForkTestAPI(t *testing.T) (api *FrontendAPI, sessionStore *session.SQLiteSessionStore, reviewStore *review.SQLiteReviewStore, db interface{ Close() error }) {
	t.Helper()
	ctx := context.Background()

	dbConn := openProjectSwitchTestDB(t)

	projectStore, err := project.NewSQLiteProjectStore(dbConn)
	if err != nil {
		_ = dbConn.Close()
		t.Fatalf("project store: %v", err)
	}
	sessionStore, err = session.NewSQLiteSessionStore(dbConn)
	if err != nil {
		_ = dbConn.Close()
		t.Fatalf("session store: %v", err)
	}
	reviewStore, err = review.NewSQLiteReviewStore(dbConn)
	if err != nil {
		_ = dbConn.Close()
		t.Fatalf("review store: %v", err)
	}

	agentDir := t.TempDir()
	projectManager := project.NewManager(projectStore, agentDir, nil)
	createdProject, err := projectManager.CreateProject("Fork Project", "")
	if err != nil {
		_ = dbConn.Close()
		t.Fatalf("create project: %v", err)
	}

	manager := session.NewManager(nil, func(session.Event) {}, agentDir)
	manager.SetSessionStore(sessionStore)
	manager.SetProjectResolver(func(projectID string) (string, error) {
		return createdProject.WorkspacePath, nil
	})

	api = &FrontendAPI{
		app:         &Application{manager: manager},
		store:       sessionStore,
		reviewStore: reviewStore,
	}

	// Seed a source session.
	if err := sessionStore.SaveSession(ctx, session.SessionInfo{
		ID: "fork-src", ProjectID: createdProject.ID, Name: "Source Session",
		CreatedAt: time.Now().Format(time.RFC3339),
	}); err != nil {
		_ = dbConn.Close()
		t.Fatalf("save session: %v", err)
	}

	return api, sessionStore, reviewStore, dbConn
}

// mustListAPITaskIDs returns the task ids for a session, failing the test on error.
func mustListAPITaskIDs(ctx context.Context, t *testing.T, store *session.SQLiteSessionStore, sessionID string) []string {
	t.Helper()
	// Use GetLatestTaskID to confirm a task exists in the fork (no direct db
	// access across packages).
	latest, err := store.GetLatestTaskID(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetLatestTaskID: %v", err)
	}
	if latest == "" {
		return nil
	}
	return []string{latest}
}

func TestForkSessionRPC_GuardBlocksUnfinishedTask(t *testing.T) {
	api, sessionStore, _, db := newForkTestAPI(t)
	defer func() { _ = db.Close() }()
	ctx := context.Background()

	// Seed an in-progress task → fork must be rejected.
	if err := sessionStore.SaveTask(ctx, session.TaskRecord{
		ID: "task-run", SessionID: "fork-src", OriginalRequest: "running",
		RoutingDecision: json.RawMessage(`{}`), Plan: json.RawMessage(`{}`),
		Reflections: json.RawMessage(`[]`), Status: "in_progress", CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("SaveTask: %v", err)
	}

	fork, err := api.ForkSession("fork-src")
	if err == nil {
		t.Fatal("expected error when forking session with unfinished task")
	}
	if !strings.Contains(err.Error(), "unfinished task") {
		t.Errorf("unexpected error message: %v", err)
	}
	if fork != nil {
		t.Error("expected nil fork on guard failure")
	}
}

func TestForkSessionRPC_SuccessClonesSessionAndReview(t *testing.T) {
	api, sessionStore, reviewStore, db := newForkTestAPI(t)
	defer func() { _ = db.Close() }()
	ctx := context.Background()

	// Completed task + a message + a review comment.
	if err := sessionStore.SaveTask(ctx, session.TaskRecord{
		ID: "task-done", SessionID: "fork-src", OriginalRequest: "done",
		RoutingDecision: json.RawMessage(`{}`), Plan: json.RawMessage(`{}`),
		Reflections: json.RawMessage(`[]`), Status: "completed", CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("SaveTask: %v", err)
	}
	if err := sessionStore.SaveMessage(ctx, session.ChatMessage{
		SessionID: "fork-src", Role: "user", Content: "hi", Metadata: json.RawMessage(`{}`),
		CreatedAt: time.Now().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("SaveMessage: %v", err)
	}
	if err := reviewStore.UpsertGeneralComment(ctx, "fork-src", "review note"); err != nil {
		t.Fatalf("UpsertGeneralComment: %v", err)
	}

	fork, err := api.ForkSession("fork-src")
	if err != nil {
		t.Fatalf("ForkSession: %v", err)
	}
	if fork == nil || fork.ID == "fork-src" {
		t.Fatalf("invalid fork: %+v", fork)
	}
	if fork.Name != "Source Session (fork 1)" {
		t.Errorf("fork name=%q", fork.Name)
	}

	// Review was cloned (best-effort).
	rev, err := reviewStore.GetReview(ctx, fork.ID)
	if err != nil {
		t.Fatalf("GetReview fork: %v", err)
	}
	if rev.GeneralComment != "review note" {
		t.Errorf("review not cloned: general=%q", rev.GeneralComment)
	}

	// Message + task copied.
	msgs, _ := sessionStore.LoadMessages(ctx, fork.ID)
	if len(msgs) != 1 || msgs[0].Content != "hi" {
		t.Errorf("messages not copied: %+v", msgs)
	}
	tasks := mustListAPITaskIDs(ctx, t, sessionStore, fork.ID)
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task in fork, got %d", len(tasks))
	}
}
