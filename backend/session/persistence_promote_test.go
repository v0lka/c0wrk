package session

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/v0lka/c0wrk/backend/project"
)

// promoteTestPathsFromBase derives the promotion path pairs from a fixed base
// directory (one runtimeTempDir call per test — the helper creates a fresh
// temp dir per invocation, so callers must reuse a single base): the
// per-session directory pair and the more specific workspace pair, mirroring
// what Manager.MoveSessionStorage moves on disk.
func promoteTestPathsFromBase(base, srcSessionID, dstProjectID string) (rewrites [][2]string, oldSessionDir, newSessionDir, oldWorkspace, newWorkspace string) {
	projectsDir := filepath.Join(base, "projects")
	oldSessionDir = filepath.Join(projectsDir, project.NoProjectID, srcSessionID)
	newSessionDir = filepath.Join(projectsDir, dstProjectID, srcSessionID)
	oldWorkspace = filepath.Join(oldSessionDir, "workspace")
	newWorkspace = filepath.Join(projectsDir, dstProjectID, "Workspace")
	return [][2]string{
		{oldSessionDir, newSessionDir},
		{oldWorkspace, newWorkspace},
	}, oldSessionDir, newSessionDir, oldWorkspace, newWorkspace
}

// promoteTestPaths builds the promotion path pairs from a fresh temp base.
func promoteTestPaths(t *testing.T, srcSessionID, dstProjectID string) [][2]string {
	t.Helper()
	rewrites, _, _, _, _ := promoteTestPathsFromBase(runtimeTempDir(t), srcSessionID, dstProjectID)
	return rewrites
}

// seedPromoteSession creates a CHAT session under the No Project pseudo-project
// (FK row included) with the given messages.
func seedPromoteSession(t *testing.T, store *SQLiteSessionStore, db *sql.DB, sessionID string, messages ...ChatMessage) {
	t.Helper()
	ctx := context.Background()
	// The sessions FK requires the No Project row to exist. Idempotent: tests
	// may seed several CHAT sessions against the same DB.
	if _, err := db.ExecContext(ctx, `
		INSERT INTO projects (id, name, workspace_path, created_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(id) DO NOTHING`,
		project.NoProjectID, "No Project", runtimeTempDir(t), time.Now().Format(time.RFC3339),
	); err != nil {
		t.Fatalf("failed to insert No Project row: %v", err)
	}
	if err := store.SaveSession(ctx, SessionInfo{
		ID:        sessionID,
		ProjectID: project.NoProjectID,
		Name:      "Chat session",
		CreatedAt: "2026-01-01T00:00:00Z",
		Archived:  true,
	}); err != nil {
		t.Fatalf("failed to seed session: %v", err)
	}
	for i := range messages {
		if messages[i].CreatedAt == "" {
			messages[i].CreatedAt = "2026-01-01T00:01:00Z"
		}
		messages[i].SessionID = sessionID
		if err := store.SaveMessage(ctx, messages[i]); err != nil {
			t.Fatalf("failed to seed message %d: %v", i, err)
		}
	}
}

// metadataJSON marshals a metadata blob the way the manager does, keeping the
// marshaling faithful (escaping included).
func metadataJSON(t *testing.T, blob map[string]any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(blob)
	if err != nil {
		t.Fatalf("failed to marshal metadata: %v", err)
	}
	return data
}

func TestPromoteSessionToProject_ReParentsRewritesMetadataAndClearsArchived(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx := context.Background()
	dstID := "promoted-project"
	db := store.db
	base := runtimeTempDir(t)
	rewrites, oldSessionDir, newSessionDir, _, newWorkspace := promoteTestPathsFromBase(base, "s-promote", dstID)

	// Destination project row for the FK.
	if _, err := db.ExecContext(ctx, `
		INSERT INTO projects (id, name, workspace_path, created_at)
		VALUES (?, ?, ?, ?)`,
		dstID, "Promoted", runtimeTempDir(t), time.Now().Format(time.RFC3339),
	); err != nil {
		t.Fatalf("failed to insert destination project: %v", err)
	}

	// Metadata blobs exactly as the manager persists them: an image attachment
	// under the old session dir (session-directory pair applies) and a doc
	// reference under the old workspace (the more specific workspace pair must
	// win over the generic pair — longest match first).
	oldWorkspace := filepath.Join(oldSessionDir, "workspace")
	seedPromoteSession(t, store, db, "s-promote",
		ChatMessage{
			Role:    "user",
			Content: "Look at " + oldWorkspace + "/notes.txt — historical prose keeps its old path",
			Metadata: metadataJSON(t, map[string]any{
				"images": []any{map[string]any{
					"path":       filepath.Join(oldSessionDir, "images", "img_1.jpg"),
					"media_type": "image/png",
				}},
			}),
		},
		ChatMessage{
			Role:     "assistant",
			Content:  "done",
			Metadata: metadataJSON(t, map[string]any{"doc": map[string]any{"path": filepath.Join(oldWorkspace, "out.md")}}),
		},
	)

	if err := store.PromoteSessionToProject(ctx, "s-promote", dstID, rewrites); err != nil {
		t.Fatalf("PromoteSessionToProject failed: %v", err)
	}

	loaded, err := store.LoadSession(ctx, "s-promote")
	if err != nil {
		t.Fatalf("failed to load promoted session: %v", err)
	}
	if loaded.ProjectID != dstID {
		t.Fatalf("project_id = %q, want %q", loaded.ProjectID, dstID)
	}
	if loaded.Archived {
		t.Fatal("promoted session must not be archived")
	}

	msgs, err := store.LoadMessages(ctx, "s-promote")
	if err != nil {
		t.Fatalf("failed to load messages: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("message count = %d, want 2", len(msgs))
	}

	// Message 1: the machine-resolved image path moved with the session dir;
	// the historical prose kept its original absolute path.
	var userMeta struct {
		Images []struct {
			Path string `json:"path"`
		} `json:"images"`
	}
	if err := json.Unmarshal(msgs[0].Metadata, &userMeta); err != nil {
		t.Fatalf("failed to unmarshal user metadata: %v", err)
	}
	wantImage := filepath.Join(newSessionDir, "images", "img_1.jpg")
	if userMeta.Images[0].Path != wantImage {
		t.Fatalf("image path = %q, want %q", userMeta.Images[0].Path, wantImage)
	}
	if !strings.Contains(msgs[0].Content, oldWorkspace) {
		t.Fatalf("free-text content must keep the historical path %q, got %q", oldWorkspace, msgs[0].Content)
	}

	// Message 2: the workspace pair (longer prefix) must win, landing the doc
	// reference at the project Workspace, not at <sid>/workspace.
	var docMeta struct {
		Doc struct {
			Path string `json:"path"`
		} `json:"doc"`
	}
	if err := json.Unmarshal(msgs[1].Metadata, &docMeta); err != nil {
		t.Fatalf("failed to unmarshal doc metadata: %v", err)
	}
	wantDoc := filepath.Join(newWorkspace, "out.md")
	if docMeta.Doc.Path != wantDoc {
		t.Fatalf("doc path = %q, want %q (workspace pair must win)", docMeta.Doc.Path, wantDoc)
	}
}

func TestPromoteSessionToProject_RewritesJSONEscapedPrefix(t *testing.T) {
	// The stored metadata is JSON text: on Windows the path separators inside
	// it are doubled. Feed an escaped Windows-style prefix and verify both the
	// escaped form is rewritten (store level is OS-agnostic — pure text) and
	// the raw form leaves nothing behind.
	oldRaw := `C:\Users\u\.c0wrk\projects\__no_project__\sid`
	newRaw := `C:\Users\u\.c0wrk\projects\dst\sid`
	got := rewritePathPrefixes(
		`{"images":[{"path":"`+jsonEscapeString(filepath.Join(oldRaw, "images", "a.jpg"))+`"}]}`,
		[][2]string{{oldRaw, newRaw}},
	)
	want := `{"images":[{"path":"` + jsonEscapeString(filepath.Join(newRaw, "images", "a.jpg")) + `"}]}`
	if got != want {
		t.Fatalf("escaped rewrite mismatch:\n got %s\nwant %s", got, want)
	}
}

func TestPromoteSessionToProject_RefusesNonChatSession(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx := context.Background()
	if err := store.SaveSession(ctx, SessionInfo{
		ID:        "code-session",
		ProjectID: testProjectID,
		Name:      "CODE session",
		CreatedAt: "2026-01-01T00:00:00Z",
	}); err != nil {
		t.Fatalf("failed to seed session: %v", err)
	}

	err := store.PromoteSessionToProject(ctx, "code-session", "other-dest", nil)
	if err == nil {
		t.Fatal("promoting a CODE session must be refused")
	}
	loaded, loadErr := store.LoadSession(ctx, "code-session")
	if loadErr != nil {
		t.Fatalf("failed to reload session: %v", loadErr)
	}
	if loaded.ProjectID != testProjectID {
		t.Fatalf("refused promotion must leave the session untouched, project_id = %q", loaded.ProjectID)
	}
}

func TestPromoteSessionToProject_RefusesNoProjectDestinationAndMissingSession(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx := context.Background()
	db := store.db
	rewrites := promoteTestPaths(t, "s-x", "dst-never-used")
	seedPromoteSession(t, store, db, "s-x")

	if err := store.PromoteSessionToProject(ctx, "s-x", project.NoProjectID, rewrites); err == nil {
		t.Fatal("destination = No Project must be refused")
	}
	if err := store.PromoteSessionToProject(ctx, "missing-session", "dst-never-used", rewrites); err == nil {
		t.Fatal("promoting an unknown session must fail")
	}
}

func TestPromoteSessionToProject_MissingDestinationProjectRollsBack(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx := context.Background()
	db := store.db
	base := runtimeTempDir(t)
	rewrites, oldSessionDir, _, _, _ := promoteTestPathsFromBase(base, "s-rollback", "ghost-project")
	seedPromoteSession(t, store, db, "s-x") // unrelated session proving no collateral rewrites
	seedPromoteSession(t, store, db, "s-rollback",
		ChatMessage{
			Role:     "user",
			Content:  "with image",
			Metadata: metadataJSON(t, map[string]any{"images": []any{map[string]any{"path": filepath.Join(oldSessionDir, "images", "a.png")}}}),
		},
	)

	// ghost-project has no projects row — the FK must reject the UPDATE and
	// the whole transaction (including any metadata rewrite) must roll back.
	if err := store.PromoteSessionToProject(ctx, "s-rollback", "ghost-project", rewrites); err == nil {
		t.Fatal("promoting to a non-existent project must fail on the FK")
	}

	loaded, err := store.LoadSession(ctx, "s-rollback")
	if err != nil {
		t.Fatalf("failed to reload session: %v", err)
	}
	if loaded.ProjectID != project.NoProjectID {
		t.Fatalf("failed promotion must roll back, project_id = %q", loaded.ProjectID)
	}
	if !loaded.Archived {
		t.Fatal("failed promotion must roll back the archived reset")
	}
	msgs, err := store.LoadMessages(ctx, "s-rollback")
	if err != nil {
		t.Fatalf("failed to reload messages: %v", err)
	}
	if !strings.Contains(string(msgs[0].Metadata), oldSessionDir) {
		t.Fatalf("failed promotion must roll back metadata rewrites, got %s", msgs[0].Metadata)
	}
}

func TestPromoteSessionToProject_OtherSessionsUntouched(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx := context.Background()
	dstID := "promoted-project"
	db := store.db
	base := runtimeTempDir(t)
	rewrites, oldSessionDir, newSessionDir, _, _ := promoteTestPathsFromBase(base, "s-mine", dstID)
	if _, err := db.ExecContext(ctx, `
		INSERT INTO projects (id, name, workspace_path, created_at)
		VALUES (?, ?, ?, ?)`,
		dstID, "Promoted", runtimeTempDir(t), time.Now().Format(time.RFC3339),
	); err != nil {
		t.Fatalf("failed to insert destination project: %v", err)
	}

	seedPromoteSession(t, store, db, "s-mine",
		ChatMessage{
			Role:     "user",
			Content:  "mine",
			Metadata: metadataJSON(t, map[string]any{"images": []any{map[string]any{"path": filepath.Join(oldSessionDir, "images", "a.png")}}}),
		},
	)
	// A second CHAT session whose metadata embeds ITS OWN session dir — must
	// stay untouched (the rewrite is scoped to the promoted session's rows and
	// its own prefix pair).
	otherDir := filepath.Join(runtimeTempDir(t), "projects", project.NoProjectID, "s-other")
	seedPromoteSession(t, store, db, "s-other",
		ChatMessage{
			Role:     "user",
			Content:  "other",
			Metadata: metadataJSON(t, map[string]any{"images": []any{map[string]any{"path": filepath.Join(otherDir, "images", "b.png")}}}),
		},
	)

	if err := store.PromoteSessionToProject(ctx, "s-mine", dstID, rewrites); err != nil {
		t.Fatalf("PromoteSessionToProject failed: %v", err)
	}

	msgs, err := store.LoadMessages(ctx, "s-other")
	if err != nil {
		t.Fatalf("failed to load other session messages: %v", err)
	}
	if !strings.Contains(string(msgs[0].Metadata), otherDir) {
		t.Fatalf("another session's metadata must stay untouched, got %s", msgs[0].Metadata)
	}

	mine, err := store.LoadMessages(ctx, "s-mine")
	if err != nil {
		t.Fatalf("failed to load promoted messages: %v", err)
	}
	if !strings.Contains(string(mine[0].Metadata), newSessionDir) {
		t.Fatalf("promoted metadata must carry the new session dir, got %s", mine[0].Metadata)
	}
}
