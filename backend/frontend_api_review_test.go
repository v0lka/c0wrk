package backend

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/v0lka/c0wrk/backend/project"
	"github.com/v0lka/c0wrk/backend/session"
)

// genReviewLines returns n numbered lines ("l1\nl2\n…\n").
func genReviewLines(n int) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "l%d\n", i)
	}
	return b.String()
}

// replaceLine swaps the 1-based lineNo of s (newline-delimited) with repl.
func replaceLine(t *testing.T, s string, lineNo int, repl string) string {
	t.Helper()
	parts := strings.Split(s, "\n")
	if lineNo < 1 || lineNo > len(parts) {
		t.Fatalf("replaceLine: lineNo %d out of range (%d lines)", lineNo, len(parts))
	}
	parts[lineNo-1] = repl
	return strings.Join(parts, "\n")
}

// hunkContextLines counts the context (leading-space) body lines in a raw
// hunk block, skipping the "@@" header and "\ No newline" markers.
func hunkContextLines(raw string) int {
	body := strings.Split(raw, "\n")
	count := 0
	for i, l := range body {
		if i == 0 { // hunk header "@@ …"
			continue
		}
		if l != "" && l[0] == ' ' {
			count++
		}
	}
	return count
}

// TestGetReviewDiff_TwoFiles asserts the RPC groups uncommitted changes per
// file with the expected hunk counts, includes staged and unstaged changes
// together (vs HEAD), and yields at least 5 context lines per hunk (-U5).
func TestGetReviewDiff_TwoFiles(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, dir string) {
		base := genReviewLines(30)
		commitFile(t, dir, "file1.txt", base)
		commitFile(t, dir, "file2.txt", base)

		// file1: two change regions 18 lines apart => 2 distinct hunks
		// (gap > 2*5 context lines, so git does not merge them).
		f1 := replaceLine(t, base, 6, "CHG6")
		f1 = replaceLine(t, f1, 24, "CHG24")
		if err := os.WriteFile(filepath.Join(dir, "file1.txt"), []byte(f1), 0o644); err != nil {
			t.Fatalf("write file1: %v", err)
		}

		// file2: one change region => 1 hunk. Stage it to prove the diff
		// covers staged + unstaged changes combined against HEAD.
		f2 := replaceLine(t, base, 15, "CHG15")
		if err := os.WriteFile(filepath.Join(dir, "file2.txt"), []byte(f2), 0o644); err != nil {
			t.Fatalf("write file2: %v", err)
		}
		runGit(t, dir, "add", "file2.txt")

		files, err := f.GetReviewDiff()
		if err != nil {
			t.Fatalf("GetReviewDiff: %v", err)
		}
		if len(files) != 2 {
			t.Fatalf("expected 2 changed files, got %d", len(files))
		}

		// Assert per-file hunk counts via a path-keyed map.
		counts := map[string]int{}
		for _, file := range files {
			counts[file.Path] = len(file.Hunks)
		}
		if counts["file1.txt"] != 2 {
			t.Errorf("file1.txt: expected 2 hunks, got %d", counts["file1.txt"])
		}
		if counts["file2.txt"] != 1 {
			t.Errorf("file2.txt: expected 1 hunk, got %d", counts["file2.txt"])
		}

		// Every hunk must carry at least 5 context lines (the -U5 effect).
		for _, file := range files {
			for _, h := range file.Hunks {
				if c := hunkContextLines(h.Raw); c < 5 {
					t.Errorf("%s hunk @ +%d: %d context lines, want >=5", file.Path, h.NewStart, c)
				}
			}
		}
	})
}

// TestGetReviewDiff_NoProject verifies the read-only RPC returns an empty
// slice (not an error) for No Project mode.
func TestGetReviewDiff_NoProject(t *testing.T) {
	f := &FrontendAPI{activeProjectID: project.NoProjectID, activeProjectPath: t.TempDir()}
	files, err := f.GetReviewDiff()
	if err != nil {
		t.Fatalf("unexpected error for No Project: %v", err)
	}
	if len(files) != 0 {
		t.Errorf("expected empty slice for No Project, got %d files", len(files))
	}
}

// TestGetReviewDiff_NoProjectEmpty verifies that an unconfigured
// FrontendAPI (no active project) also returns an empty slice.
func TestGetReviewDiff_NoActiveProject(t *testing.T) {
	f := &FrontendAPI{}
	files, err := f.GetReviewDiff()
	if err != nil {
		t.Fatalf("unexpected error for no active project: %v", err)
	}
	if len(files) != 0 {
		t.Errorf("expected empty slice with no active project, got %d files", len(files))
	}
}

// TestGetReviewDiff_NonGit verifies the RPC returns an empty slice for a
// workspace that is not a git repository.
func TestGetReviewDiff_NonGit(t *testing.T) {
	f := &FrontendAPI{activeProjectPath: t.TempDir()}
	files, err := f.GetReviewDiff()
	if err != nil {
		t.Fatalf("unexpected error for non-git workspace: %v", err)
	}
	if len(files) != 0 {
		t.Errorf("expected empty slice for non-git workspace, got %d files", len(files))
	}
}

// TestGetReviewDiff_CleanTree verifies the RPC returns an empty slice when
// the working tree has no uncommitted changes relative to HEAD.
func TestGetReviewDiff_CleanTree(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, _ string) {
		files, err := f.GetReviewDiff()
		if err != nil {
			t.Fatalf("unexpected error for clean tree: %v", err)
		}
		if len(files) != 0 {
			t.Errorf("expected empty slice for clean tree, got %d files", len(files))
		}
	})
}

// TestGetReviewDiff_IncludesUntracked verifies the RPC surfaces untracked
// files (never added to the index) alongside tracked changes: `git diff
// HEAD` omits them, so BuildReviewDiff emits each as a new-file diff against
// /dev/null. Git-ignored files must still be excluded.
func TestGetReviewDiff_IncludesUntracked(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, dir string) {
		// An untracked file that must appear as a full-file addition.
		if err := os.WriteFile(filepath.Join(dir, "newfile.txt"), []byte("hello\nworld\n"), 0o644); err != nil {
			t.Fatalf("write newfile: %v", err)
		}
		// An untracked file in a subdirectory (rel path must round-trip).
		if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
			t.Fatalf("mkdir sub: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "sub", "nested.txt"), []byte("nested\n"), 0o644); err != nil {
			t.Fatalf("write nested: %v", err)
		}
		// A git-ignored untracked file that must NOT appear.
		if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("ignored.txt\n"), 0o644); err != nil {
			t.Fatalf("write gitignore: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "ignored.txt"), []byte("nope\n"), 0o644); err != nil {
			t.Fatalf("write ignored: %v", err)
		}

		files, err := f.GetReviewDiff()
		if err != nil {
			t.Fatalf("GetReviewDiff: %v", err)
		}

		// Note: .gitignore is itself untracked and therefore appears too.
		paths := map[string]bool{}
		for _, file := range files {
			paths[file.Path] = true
		}
		if !paths["newfile.txt"] {
			t.Errorf("expected untracked newfile.txt in review diff, got paths: %v", paths)
		}
		if !paths["sub/nested.txt"] {
			t.Errorf("expected untracked sub/nested.txt in review diff, got paths: %v", paths)
		}
		if paths["ignored.txt"] {
			t.Errorf("git-ignored ignored.txt must not appear in review diff, got paths: %v", paths)
		}

		// An untracked file is a pure addition: a single hunk at new line 1
		// with the whole file added, marked as new (old side empty).
		var newFile *ReviewFileDiff
		for i := range files {
			if files[i].Path == "newfile.txt" {
				newFile = &files[i]
				break
			}
		}
		if newFile == nil {
			t.Fatalf("newfile.txt missing from review diff")
		}
		if len(newFile.Hunks) != 1 {
			t.Fatalf("newfile.txt: expected 1 hunk, got %d", len(newFile.Hunks))
		}
		h := newFile.Hunks[0]
		if h.OldCount != 0 || h.NewStart != 1 {
			t.Errorf("newfile.txt hunk = old %+d new @%d count %d, want pure addition at new line 1",
				h.OldCount, h.NewStart, h.NewCount)
		}
	})
}

// --- GetCommitDiff ---

// TestGetCommitDiff_TwoFiles asserts the RPC returns the per-file hunk diff
// introduced by a single commit (relative to its parent), grouped per file
// with at least 5 context lines per hunk (-U5).
func TestGetCommitDiff_TwoFiles(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, dir string) {
		base := genReviewLines(30)
		commitFile(t, dir, "file1.txt", base)
		commitFile(t, dir, "file2.txt", base)

		// file1: two change regions 18 lines apart => 2 distinct hunks.
		f1 := replaceLine(t, base, 6, "CHG6")
		f1 = replaceLine(t, f1, 24, "CHG24")
		if err := os.WriteFile(filepath.Join(dir, "file1.txt"), []byte(f1), 0o644); err != nil {
			t.Fatalf("write file1: %v", err)
		}

		// file2: one change region => 1 hunk.
		f2 := replaceLine(t, base, 15, "CHG15")
		if err := os.WriteFile(filepath.Join(dir, "file2.txt"), []byte(f2), 0o644); err != nil {
			t.Fatalf("write file2: %v", err)
		}
		runGit(t, dir, "add", "-A")
		runGit(t, dir, "commit", "-m", "two-file changes")

		sha := gitOut(t, dir, "rev-parse", "HEAD")

		files, err := f.GetCommitDiff(sha)
		if err != nil {
			t.Fatalf("GetCommitDiff: %v", err)
		}
		if len(files) != 2 {
			t.Fatalf("expected 2 changed files, got %d", len(files))
		}

		counts := map[string]int{}
		for _, file := range files {
			counts[file.Path] = len(file.Hunks)
		}
		if counts["file1.txt"] != 2 {
			t.Errorf("file1.txt: expected 2 hunks, got %d", counts["file1.txt"])
		}
		if counts["file2.txt"] != 1 {
			t.Errorf("file2.txt: expected 1 hunk, got %d", counts["file2.txt"])
		}

		// Every hunk must carry at least 5 context lines (the -U5 effect).
		for _, file := range files {
			for _, h := range file.Hunks {
				if c := hunkContextLines(h.Raw); c < 5 {
					t.Errorf("%s hunk @ +%d: %d context lines, want >=5", file.Path, h.NewStart, c)
				}
			}
		}
	})
}

// TestGetCommitDiff_RootCommit verifies the RPC returns all files as added
// for a root commit (no parent), via the --root flag.
func TestGetCommitDiff_RootCommit(t *testing.T) {
	tmpDir := t.TempDir()
	gitInit(t, tmpDir)
	if err := os.WriteFile(filepath.Join(tmpDir, "init.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("write init: %v", err)
	}
	runGit(t, tmpDir, "add", "init.txt")
	runGit(t, tmpDir, "commit", "-m", "initial")

	f := &FrontendAPI{activeProjectPath: tmpDir}
	sha := gitOut(t, tmpDir, "rev-parse", "HEAD")

	files, err := f.GetCommitDiff(sha)
	if err != nil {
		t.Fatalf("GetCommitDiff: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("expected 1 file for root commit, got %d", len(files))
	}
	if files[0].Path != "init.txt" {
		t.Errorf("expected init.txt, got %s", files[0].Path)
	}
	if len(files[0].Hunks) != 1 {
		t.Errorf("expected 1 hunk, got %d", len(files[0].Hunks))
	}
}

// TestGetCommitDiff_EmptySHA verifies the RPC rejects an empty/whitespace SHA.
func TestGetCommitDiff_EmptySHA(t *testing.T) {
	f := &FrontendAPI{activeProjectPath: t.TempDir()}
	if _, err := f.GetCommitDiff(""); err == nil {
		t.Fatal("expected error for empty sha")
	}
	if _, err := f.GetCommitDiff("   "); err == nil {
		t.Fatal("expected error for whitespace-only sha")
	}
}

// TestGetCommitDiff_InvalidSha verifies the RPC rejects a non-hex SHA.
func TestGetCommitDiff_InvalidSha(t *testing.T) {
	f := &FrontendAPI{activeProjectPath: t.TempDir()}
	if _, err := f.GetCommitDiff("not-a-sha"); err == nil {
		t.Fatal("expected error for non-hex SHA")
	}
}

// TestGetCommitDiff_NoProject verifies the RPC returns an error for No
// Project mode (a commit diff requires a git repository).
func TestGetCommitDiff_NoProject(t *testing.T) {
	f := &FrontendAPI{activeProjectID: project.NoProjectID, activeProjectPath: t.TempDir()}
	if _, err := f.GetCommitDiff("abcdef1234"); err == nil {
		t.Fatal("expected error for No Project")
	}
}

// TestSaveReviewPrompt_PersistsAndResolves verifies the review_prompt message
// is persisted with a prompt_id and that ResolvePendingMessage keyed on that
// prompt_id records the user's decision — so the prompt (and its resolved
// state) survives a session switch / restart instead of being a transient
// frontend-only message.
func TestSaveReviewPrompt_PersistsAndResolves(t *testing.T) {
	ctx := context.Background()
	// File-based temp db: with :memory: each pooled connection is a separate
	// database, so the message saved on one connection is invisible to a
	// query routed to another. A temp file guarantees a single shared store.
	dbPath := filepath.Join(t.TempDir(), "review_prompt.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer func() { _ = db.Close() }()
	// Mirror production's connection-pool and pragma config (see OpenDatabase):
	// a single pooled connection (MaxOpenConns(1)) is the regime where a
	// read cursor held open during a follow-up UPDATE would deadlock. WAL +
	// busy_timeout keep the single connection usable, and foreign_keys=ON
	// matches production so a constraint violation from SaveReviewPrompt
	// would surface here too.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	for _, pragma := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA busy_timeout=5000",
		"PRAGMA foreign_keys=ON",
	} {
		if _, err := db.ExecContext(ctx, pragma); err != nil {
			t.Fatalf("exec %s: %v", pragma, err)
		}
	}

	// Prerequisite rows for the foreign-key chain session_messages → sessions →
	// projects, now enforced because the test mirrors production's
	// foreign_keys=ON. The session store creates sessions/session_messages; the
	// projects table is owned by the project store, so create it here.
	if _, err := db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS projects (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			workspace_path TEXT NOT NULL,
			is_external BOOLEAN NOT NULL DEFAULT 0,
			created_at TIMESTAMP NOT NULL,
			last_active_at TIMESTAMP
		)`); err != nil {
		t.Fatalf("create projects table: %v", err)
	}
	store, err := session.NewSQLiteSessionStore(db)
	if err != nil {
		t.Fatalf("NewSQLiteSessionStore: %v", err)
	}
	// Seed the project + session the prompt belongs to.
	if _, err := db.ExecContext(ctx, `
		INSERT INTO projects (id, name, workspace_path, created_at) VALUES (?, ?, ?, ?)`,
		"proj-review", "Review Project", "/tmp/review", "2024-01-15T10:00:00Z",
	); err != nil {
		t.Fatalf("insert project: %v", err)
	}
	if err := store.SaveSession(ctx, session.SessionInfo{
		ID:        "sess-review",
		ProjectID: "proj-review",
		Name:      "Review Session",
		CreatedAt: "2024-01-15T10:00:00Z",
	}); err != nil {
		t.Fatalf("save session: %v", err)
	}
	f := &FrontendAPI{store: store}

	prompt, err := f.SaveReviewPrompt("sess-review")
	if err != nil {
		t.Fatalf("SaveReviewPrompt: %v", err)
	}
	if prompt.PromptID == "" {
		t.Fatal("expected non-empty prompt_id")
	}
	if prompt.Content == "" {
		t.Fatal("expected non-empty content returned to the frontend")
	}
	promptID := prompt.PromptID

	// The message is persisted and reloads as a review_prompt with prompt_id.
	msgs, err := store.LoadMessages(ctx, "sess-review")
	if err != nil {
		t.Fatalf("LoadMessages: %v", err)
	}
	if len(msgs) != 1 || msgs[0].Role != "review_prompt" {
		t.Fatalf("expected 1 review_prompt message, got %+v", msgs)
	}
	// The content returned to the frontend must equal what is persisted, so
	// the live card and the reloaded message are identical (single source of
	// truth for the wording).
	if msgs[0].Content != prompt.Content {
		t.Errorf("persisted content = %q, want %q (returned by SaveReviewPrompt)", msgs[0].Content, prompt.Content)
	}
	var meta map[string]any
	if err := json.Unmarshal(msgs[0].Metadata, &meta); err != nil {
		t.Fatalf("unmarshal metadata: %v", err)
	}
	if meta["prompt_id"] != promptID {
		t.Errorf("metadata prompt_id = %v, want %s", meta["prompt_id"], promptID)
	}

	// Resolve via ResolvePendingMessage keyed on prompt_id (mirrors the
	// frontend resolveReviewPrompt wrapper).
	if err := store.ResolvePendingMessage(ctx, "sess-review", "review_prompt", "prompt_id", promptID,
		map[string]any{"resolved": true, "decision": "decline"}); err != nil {
		t.Fatalf("ResolvePendingMessage: %v", err)
	}

	msgs2, err := store.LoadMessages(ctx, "sess-review")
	if err != nil {
		t.Fatalf("LoadMessages after resolve: %v", err)
	}
	if len(msgs2) != 1 {
		t.Fatalf("expected 1 message after resolve, got %d", len(msgs2))
	}
	var meta2 map[string]any
	if err := json.Unmarshal(msgs2[0].Metadata, &meta2); err != nil {
		t.Fatalf("unmarshal metadata after resolve: %v", err)
	}
	if meta2["resolved"] != true || meta2["decision"] != "decline" {
		t.Errorf("after resolve metadata = %+v, want resolved+decline", meta2)
	}
	if meta2["prompt_id"] != promptID {
		t.Errorf("prompt_id dropped after resolve: %v", meta2["prompt_id"])
	}
}
