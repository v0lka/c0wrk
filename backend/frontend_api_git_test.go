package backend

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/v0lka/c0wrk/core/workspace"
)

// --- helpers for git tests ---

// commitFile creates a file, stages it, and commits it.
func commitFile(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	runGit(t, dir, "add", name)
	runGit(t, dir, "commit", "-m", "add "+name)
}

// withGitRepo creates a temporary directory with an initialised git
// repository and a committed file (committed.txt), then runs fn with a
// FrontendAPI pointing at that directory.
func withGitRepo(t *testing.T, fn func(*FrontendAPI, string)) {
	t.Helper()
	tmpDir := t.TempDir()
	gitInit(t, tmpDir)
	commitFile(t, tmpDir, "committed.txt", "v1\n")

	f := &FrontendAPI{activeProjectPath: tmpDir}
	fn(f, tmpDir)
}

// --- StageFile tests ---

func TestStageFile_NoProject(t *testing.T) {
	f := &FrontendAPI{}
	err := f.StageFile("/some/file.txt")
	if err == nil {
		t.Fatal("expected error when no active project")
	}
}

func TestStageFile_NoProjectMode(t *testing.T) {
	f := &FrontendAPI{activeProjectID: "NO_PROJECT", activeProjectPath: t.TempDir()}
	err := f.StageFile(filepath.Join(f.activeProjectPath, "file.txt"))
	if err == nil {
		t.Fatal("expected error for No Project mode")
	}
}

func TestStageFile_Success(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, dir string) {
		// Create an unstaged file.
		path := filepath.Join(dir, "newfile.txt")
		if err := os.WriteFile(path, []byte("hello\n"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}

		if err := f.StageFile(path); err != nil {
			t.Fatalf("StageFile: %v", err)
		}

		// Verify it is now staged.
		status, err := workspace.GitStatus(context.Background(), dir)
		if err != nil {
			t.Fatalf("GitStatus: %v", err)
		}
		e, ok := status[path]
		if !ok {
			t.Fatal("newfile.txt missing from status")
		}
		if !e.Staged {
			t.Error("expected staged=true after StageFile")
		}
	})
}

func TestStageFile_FileNotFound(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, dir string) {
		err := f.StageFile(filepath.Join(dir, "nonexistent.txt"))
		if err == nil {
			t.Fatal("expected error for nonexistent file")
		}
	})
}

func TestStageFile_PathOutsideWorkspace(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, dir string) {
		err := f.StageFile("/__not__in__workspace__/file.txt")
		if err == nil {
			t.Fatal("expected error for path outside workspace")
		}
	})
}

func TestStageFile_AlreadyStaged(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, dir string) {
		path := filepath.Join(dir, "already.txt")
		if err := os.WriteFile(path, []byte("content\n"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		// Stage once.
		if err := f.StageFile(path); err != nil {
			t.Fatalf("StageFile: %v", err)
		}
		// Stage again — should be idempotent.
		if err := f.StageFile(path); err != nil {
			t.Fatalf("StageFile (idempotent): %v", err)
		}
	})
}

// --- UnstageFile tests ---

func TestUnstageFile_NoProject(t *testing.T) {
	f := &FrontendAPI{}
	err := f.UnstageFile("/some/file.txt")
	if err == nil {
		t.Fatal("expected error when no active project")
	}
}

func TestUnstageFile_NoProjectMode(t *testing.T) {
	f := &FrontendAPI{activeProjectID: "NO_PROJECT", activeProjectPath: t.TempDir()}
	err := f.UnstageFile(filepath.Join(f.activeProjectPath, "file.txt"))
	if err == nil {
		t.Fatal("expected error for No Project mode")
	}
}

func TestUnstageFile_Success(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, dir string) {
		// Create and stage a file.
		path := filepath.Join(dir, "tounstage.txt")
		if err := os.WriteFile(path, []byte("content\n"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		runGit(t, dir, "add", "tounstage.txt")

		if err := f.UnstageFile(path); err != nil {
			t.Fatalf("UnstageFile: %v", err)
		}

		// Verify it is now unstaged but still trackable as untracked.
		status, err := workspace.GitStatus(context.Background(), dir)
		if err != nil {
			t.Fatalf("GitStatus: %v", err)
		}
		e, ok := status[path]
		if !ok {
			t.Fatal("tounstage.txt missing from status")
		}
		if e.Staged {
			t.Error("expected staged=false after UnstageFile")
		}
	})
}

func TestUnstageFile_NotStaged(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, dir string) {
		// File exists but is not staged.
		path := filepath.Join(dir, "notstaged.txt")
		if err := os.WriteFile(path, []byte("content\n"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		// Unstaging an untracked file via git reset HEAD is harmless.
		if err := f.UnstageFile(path); err != nil {
			t.Fatalf("UnstageFile (not staged): %v", err)
		}
	})
}

// --- StageAll / UnstageAll tests ---

func TestStageAll_NoProject(t *testing.T) {
	f := &FrontendAPI{}
	err := f.StageAll()
	if err == nil {
		t.Fatal("expected error when no active project")
	}
}

func TestStageAll_NoProjectMode(t *testing.T) {
	f := &FrontendAPI{activeProjectID: "NO_PROJECT", activeProjectPath: t.TempDir()}
	err := f.StageAll()
	if err == nil {
		t.Fatal("expected error for No Project mode")
	}
}

func TestStageAll_Success(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, dir string) {
		// Create an untracked file.
		path := filepath.Join(dir, "stageall.txt")
		if err := os.WriteFile(path, []byte("hi\n"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}

		if err := f.StageAll(); err != nil {
			t.Fatalf("StageAll: %v", err)
		}

		// Verify it is now staged.
		status, err := workspace.GitStatus(context.Background(), dir)
		if err != nil {
			t.Fatalf("GitStatus: %v", err)
		}
		e, ok := status[path]
		if !ok {
			t.Fatal("stageall.txt missing from status")
		}
		if !e.Staged {
			t.Error("expected staged=true after StageAll")
		}
	})
}

func TestUnstageAll_NoProject(t *testing.T) {
	f := &FrontendAPI{}
	err := f.UnstageAll()
	if err == nil {
		t.Fatal("expected error when no active project")
	}
}

func TestUnstageAll_NoProjectMode(t *testing.T) {
	f := &FrontendAPI{activeProjectID: "NO_PROJECT", activeProjectPath: t.TempDir()}
	err := f.UnstageAll()
	if err == nil {
		t.Fatal("expected error for No Project mode")
	}
}

func TestUnstageAll_Success(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, dir string) {
		// Create and stage a file.
		path := filepath.Join(dir, "unstageall.txt")
		if err := os.WriteFile(path, []byte("hi\n"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		runGit(t, dir, "add", "unstageall.txt")

		if err := f.UnstageAll(); err != nil {
			t.Fatalf("UnstageAll: %v", err)
		}

		// Verify it is no longer staged.
		status, err := workspace.GitStatus(context.Background(), dir)
		if err != nil {
			t.Fatalf("GitStatus: %v", err)
		}
		e, ok := status[path]
		if !ok {
			t.Fatal("unstageall.txt missing from status")
		}
		if e.Staged {
			t.Error("expected staged=false after UnstageAll")
		}
	})
}

// --- GetDiffStat tests ---

func TestGetDiffStat_NoProject(t *testing.T) {
	f := &FrontendAPI{}
	_, err := f.GetDiffStat("/some/file.txt")
	if err == nil {
		t.Fatal("expected error when no active project")
	}
}

func TestGetDiffStat_NoProjectMode(t *testing.T) {
	f := &FrontendAPI{activeProjectID: "NO_PROJECT", activeProjectPath: t.TempDir()}
	_, err := f.GetDiffStat(filepath.Join(f.activeProjectPath, "file.txt"))
	if err == nil {
		t.Fatal("expected error for No Project mode")
	}
}

func TestGetDiffStat_NoChanges(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, dir string) {
		path := filepath.Join(dir, "committed.txt")
		stat, err := f.GetDiffStat(path)
		if err != nil {
			t.Fatalf("GetDiffStat: %v", err)
		}
		if stat.Added != 0 || stat.Deleted != 0 {
			t.Errorf("expected 0/0 for unchanged file, got %d/%d", stat.Added, stat.Deleted)
		}
	})
}

func TestGetDiffStat_AddedLines(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, dir string) {
		path := filepath.Join(dir, "committed.txt")
		// Append lines to an existing file.
		fh, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		if _, err := fh.WriteString("line 2\nline 3\n"); err != nil {
			_ = fh.Close()
			t.Fatalf("write: %v", err)
		}
		if err := fh.Close(); err != nil {
			t.Fatalf("close: %v", err)
		}

		stat, err := f.GetDiffStat(path)
		if err != nil {
			t.Fatalf("GetDiffStat: %v", err)
		}
		if stat.Added != 2 || stat.Deleted != 0 {
			t.Errorf("expected 2/0 for added lines, got %d/%d", stat.Added, stat.Deleted)
		}
	})
}

func TestGetDiffStat_StagedChanges(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, dir string) {
		path := filepath.Join(dir, "committed.txt")
		// Modify and stage.
		if err := os.WriteFile(path, []byte("staged content\n"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		runGit(t, dir, "add", "committed.txt")

		// By default git diff --numstat shows unstaged changes → 0.
		stat, err := f.GetDiffStat(path)
		if err != nil {
			t.Fatalf("GetDiffStat: %v", err)
		}
		if stat.Added != 0 || stat.Deleted != 0 {
			t.Errorf("expected 0/0 for staged-only changes (unstaged diff), got %d/%d", stat.Added, stat.Deleted)
		}
	})
}

func TestGetDiffStat_FileNotFound(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, dir string) {
		// git diff --numstat for a nonexistent file exits 0 with
		// empty output — this is not an error in git's model.
		// Our wrapper reflects that by returning a zero DiffStat.
		stat, err := f.GetDiffStat(filepath.Join(dir, "nonexistent.txt"))
		if err != nil {
			t.Fatalf("GetDiffStat: %v", err)
		}
		if stat.Added != 0 || stat.Deleted != 0 {
			t.Errorf("expected 0/0 for nonexistent file, got %d/%d", stat.Added, stat.Deleted)
		}
	})
}

func TestGetDiffStat_DeletedLines(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, dir string) {
		path := filepath.Join(dir, "committed.txt")
		// Delete a line.
		if err := os.WriteFile(path, []byte(""), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}

		stat, err := f.GetDiffStat(path)
		if err != nil {
			t.Fatalf("GetDiffStat: %v", err)
		}
		if stat.Added != 0 || stat.Deleted != 1 {
			t.Errorf("expected 0/1 for deleted line, got %d/%d", stat.Added, stat.Deleted)
		}
	})
}

// --- GetDiffStats (batch) tests ---

func TestGetDiffStats_NoProject(t *testing.T) {
	f := &FrontendAPI{}
	_, err := f.GetDiffStats()
	if err == nil {
		t.Fatal("expected error when no active project")
	}
}

func TestGetDiffStats_Clean(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, dir string) {
		stats, err := f.GetDiffStats()
		if err != nil {
			t.Fatalf("GetDiffStats: %v", err)
		}
		if stats == nil {
			t.Fatal("expected non-nil map when clean")
		}
		if len(stats) != 0 {
			t.Errorf("expected empty map when clean, got %d entries", len(stats))
		}
	})
}

func TestGetDiffStats_MultiFile(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, dir string) {
		// Modify a tracked file (unstaged): +2/-0.
		path := filepath.Join(dir, "committed.txt")
		fh, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		if _, err := fh.WriteString("line 2\nline 3\n"); err != nil {
			_ = fh.Close()
			t.Fatalf("write: %v", err)
		}
		_ = fh.Close()

		// Stage a brand-new file: +1/-0 vs HEAD.
		newPath := filepath.Join(dir, "staged.txt")
		if err := os.WriteFile(newPath, []byte("new\n"), 0o644); err != nil {
			t.Fatalf("write staged: %v", err)
		}
		runGit(t, dir, "add", "staged.txt")

		// An untracked file is NOT reported by git diff --numstat HEAD.
		if err := os.WriteFile(filepath.Join(dir, "untracked.txt"), []byte("u\n"), 0o644); err != nil {
			t.Fatalf("write untracked: %v", err)
		}

		stats, err := f.GetDiffStats()
		if err != nil {
			t.Fatalf("GetDiffStats: %v", err)
		}
		if len(stats) != 2 {
			t.Fatalf("expected 2 entries, got %d (%v)", len(stats), stats)
		}

		// Keys are absolute paths matching the GitStatus key convention.
		committed := stats[filepath.Join(dir, "committed.txt")]
		if committed.Added != 2 || committed.Deleted != 0 {
			t.Errorf("committed.txt: expected 2/0, got %d/%d", committed.Added, committed.Deleted)
		}
		staged := stats[filepath.Join(dir, "staged.txt")]
		if staged.Added != 1 || staged.Deleted != 0 {
			t.Errorf("staged.txt: expected 1/0, got %d/%d", staged.Added, staged.Deleted)
		}
		if _, ok := stats[filepath.Join(dir, "untracked.txt")]; ok {
			t.Error("untracked.txt should not be present in diff stats")
		}
	})
}

// --- parseDiffStat unit tests ---

func TestParseDiffStat_Empty(t *testing.T) {
	stat, err := parseDiffStat("")
	if err != nil {
		t.Fatalf("parseDiffStat: %v", err)
	}
	if stat.Added != 0 || stat.Deleted != 0 {
		t.Errorf("expected 0/0, got %d/%d", stat.Added, stat.Deleted)
	}
}

func TestParseDiffStat_WhitespaceOnly(t *testing.T) {
	stat, err := parseDiffStat("  \n  ")
	if err != nil {
		t.Fatalf("parseDiffStat: %v", err)
	}
	if stat.Added != 0 || stat.Deleted != 0 {
		t.Errorf("expected 0/0, got %d/%d", stat.Added, stat.Deleted)
	}
}

func TestParseDiffStat_Normal(t *testing.T) {
	stat, err := parseDiffStat("5\t3\tfile.txt")
	if err != nil {
		t.Fatalf("parseDiffStat: %v", err)
	}
	if stat.Added != 5 || stat.Deleted != 3 {
		t.Errorf("expected 5/3, got %d/%d", stat.Added, stat.Deleted)
	}
}

func TestParseDiffStat_Binary(t *testing.T) {
	stat, err := parseDiffStat("-\t-\timage.png")
	if err != nil {
		t.Fatalf("parseDiffStat: %v", err)
	}
	if stat.Added != 0 || stat.Deleted != 0 {
		t.Errorf("expected 0/0 for binary, got %d/%d", stat.Added, stat.Deleted)
	}
}

// --- GitStatus with both staged and unstaged ---

func TestGitStatus_BothStagedAndUnstaged(t *testing.T) {
	tmpDir := t.TempDir()
	gitInit(t, tmpDir)
	commitFile(t, tmpDir, "base.txt", "base\n")

	path := filepath.Join(tmpDir, "base.txt")

	// Modify and stage (index change).
	if err := os.WriteFile(path, []byte("staged\n"), 0o644); err != nil {
		t.Fatalf("write v2: %v", err)
	}
	runGit(t, tmpDir, "add", "base.txt")

	// Modify again without staging (work-tree change).
	if err := os.WriteFile(path, []byte("staged+unstaged\n"), 0o644); err != nil {
		t.Fatalf("write v3: %v", err)
	}

	status, err := workspace.GitStatus(context.Background(), tmpDir)
	if err != nil {
		t.Fatalf("GitStatus: %v", err)
	}

	e, ok := status[path]
	if !ok {
		t.Fatal("base.txt missing from status")
	}

	// Legacy fields: Status should be "M" (index), Staged=true.
	if e.Status != "M" {
		t.Errorf("legacy Status: got %q, want M", e.Status)
	}
	if !e.Staged {
		t.Error("legacy Staged: expected true (index change)")
	}

	// New fields: both index and work-tree should be "M".
	if e.IndexStatus != "M" {
		t.Errorf("IndexStatus: got %q, want M", e.IndexStatus)
	}
	if e.WorkTreeStatus != "M" {
		t.Errorf("WorkTreeStatus: got %q, want M", e.WorkTreeStatus)
	}
}

func TestGitStatus_IndexOnly(t *testing.T) {
	tmpDir := t.TempDir()
	gitInit(t, tmpDir)
	commitFile(t, tmpDir, "idx.txt", "v1\n")

	path := filepath.Join(tmpDir, "idx.txt")
	if err := os.WriteFile(path, []byte("v2\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	runGit(t, tmpDir, "add", "idx.txt")

	status, err := workspace.GitStatus(context.Background(), tmpDir)
	if err != nil {
		t.Fatalf("GitStatus: %v", err)
	}

	e, ok := status[path]
	if !ok {
		t.Fatal("idx.txt missing from status")
	}
	if e.IndexStatus != "M" {
		t.Errorf("IndexStatus: got %q, want M", e.IndexStatus)
	}
	if e.WorkTreeStatus != "" {
		t.Errorf("WorkTreeStatus: got %q, want empty", e.WorkTreeStatus)
	}
}

func TestGitStatus_WorkTreeOnly(t *testing.T) {
	tmpDir := t.TempDir()
	gitInit(t, tmpDir)
	commitFile(t, tmpDir, "wt.txt", "v1\n")

	path := filepath.Join(tmpDir, "wt.txt")
	if err := os.WriteFile(path, []byte("v2\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	// Do NOT stage.

	status, err := workspace.GitStatus(context.Background(), tmpDir)
	if err != nil {
		t.Fatalf("GitStatus: %v", err)
	}

	e, ok := status[path]
	if !ok {
		t.Fatal("wt.txt missing from status")
	}
	if e.IndexStatus != "" {
		t.Errorf("IndexStatus: got %q, want empty", e.IndexStatus)
	}
	if e.WorkTreeStatus != "M" {
		t.Errorf("WorkTreeStatus: got %q, want M", e.WorkTreeStatus)
	}
}

// --- Commit tests ---

func TestCommit_NoProject(t *testing.T) {
	f := &FrontendAPI{}
	_, err := f.Commit("test")
	if err == nil {
		t.Fatal("expected error when no active project")
	}
}

func TestCommit_NoProjectMode(t *testing.T) {
	f := &FrontendAPI{activeProjectID: "NO_PROJECT", activeProjectPath: t.TempDir()}
	_, err := f.Commit("test")
	if err == nil {
		t.Fatal("expected error for No Project mode")
	}
}

func TestCommit_EmptyMessage(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, dir string) {
		_, err := f.Commit("")
		if err == nil {
			t.Fatal("expected error for empty commit message")
		}
	})
}

func TestCommit_WhitespaceMessage(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, dir string) {
		_, err := f.Commit("\n  \t")
		if err == nil {
			t.Fatal("expected error for whitespace-only commit message")
		}
	})
}

func TestCommit_Success(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, dir string) {
		// Create and stage a file.
		path := filepath.Join(dir, "commitme.txt")
		if err := os.WriteFile(path, []byte("content\n"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		if err := f.StageFile(path); err != nil {
			t.Fatalf("StageFile: %v", err)
		}

		// Commit and capture the returned SHA.
		sha, err := f.Commit("add commitme.txt")
		if err != nil {
			t.Fatalf("Commit: %v", err)
		}
		if sha == "" {
			t.Fatal("Commit: expected non-empty commit SHA")
		}

		// The returned SHA must match git rev-parse HEAD.
		revCmd := exec.CommandContext(context.Background(), "git", "rev-parse", "HEAD")
		revCmd.Dir = dir
		revOut, err := revCmd.Output()
		if err != nil {
			t.Fatalf("git rev-parse: %v", err)
		}
		wantSHA := strings.TrimSpace(string(revOut))
		if sha != wantSHA {
			t.Errorf("Commit SHA: got %q, want %q", sha, wantSHA)
		}

		// Verify the commit subject via git log.
		cmd := exec.CommandContext(context.Background(), "git", "log", "-1", "--format=%s")
		cmd.Dir = dir
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("git log: %v", err)
		}
		got := strings.TrimSpace(string(out))
		if got != "add commitme.txt" {
			t.Errorf("git log: got %q, want %q", got, "add commitme.txt")
		}
	})
}

func TestCommit_NothingToCommit(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, dir string) {
		// No changes staged — commit should fail.
		_, err := f.Commit("empty commit")
		if err == nil {
			t.Fatal("expected error when nothing to commit")
		}
	})
}

// --- GetBranches tests ---

func TestGetBranches_NoProject(t *testing.T) {
	f := &FrontendAPI{}
	_, err := f.GetBranches()
	if err == nil {
		t.Fatal("expected error when no active project")
	}
}

func TestGetBranches_NoProjectMode(t *testing.T) {
	f := &FrontendAPI{activeProjectID: "NO_PROJECT", activeProjectPath: t.TempDir()}
	_, err := f.GetBranches()
	if err == nil {
		t.Fatal("expected error for No Project mode")
	}
}

func TestGetBranches_ReturnsCurrent(t *testing.T) {
	tmpDir := t.TempDir()
	gitInit(t, tmpDir)
	commitFile(t, tmpDir, "f.txt", "x\n")

	// Get default branch name.
	defBranch := gitDefaultBranch(t, tmpDir)

	f := &FrontendAPI{activeProjectPath: tmpDir}
	branches, err := f.GetBranches()
	if err != nil {
		t.Fatalf("GetBranches: %v", err)
	}
	if len(branches) == 0 {
		t.Fatal("GetBranches: expected at least one branch")
	}

	foundCurrent := false
	for _, b := range branches {
		if b.Name == defBranch {
			foundCurrent = true
			if !b.IsCurrent {
				t.Errorf("branch %q: expected IsCurrent=true", b.Name)
			}
		}
	}
	if !foundCurrent {
		t.Fatalf("default branch %q not found in GetBranches output", defBranch)
	}
}

func TestGetBranches_WithExtraBranch(t *testing.T) {
	tmpDir := t.TempDir()
	gitInit(t, tmpDir)
	commitFile(t, tmpDir, "f.txt", "x\n")

	// Create an additional branch.
	runGit(t, tmpDir, "branch", "feature-x")

	f := &FrontendAPI{activeProjectPath: tmpDir}
	branches, err := f.GetBranches()
	if err != nil {
		t.Fatalf("GetBranches: %v", err)
	}

	foundFeatureX := false
	for _, b := range branches {
		if b.Name == "feature-x" {
			foundFeatureX = true
			if b.IsCurrent {
				t.Error("feature-x: expected IsCurrent=false")
			}
		}
	}
	if !foundFeatureX {
		t.Fatal(`branch "feature-x" not found in GetBranches output`)
	}
}

func TestGetBranches_EmptyRepo(t *testing.T) {
	tmpDir := t.TempDir()
	// Init git without any commits — for-each-ref may return empty.
	cmd := exec.CommandContext(context.Background(), "git", "init")
	cmd.Dir = tmpDir
	if err := cmd.Run(); err != nil {
		t.Skipf("git init failed: %v", err)
	}
	// Configure user for commits.
	cmd = exec.CommandContext(context.Background(), "git", "config", "user.email", "test@test.com")
	cmd.Dir = tmpDir
	_ = cmd.Run()
	cmd = exec.CommandContext(context.Background(), "git", "config", "user.name", "Test")
	cmd.Dir = tmpDir
	_ = cmd.Run()

	f := &FrontendAPI{activeProjectPath: tmpDir}
	branches, err := f.GetBranches()
	if err != nil {
		t.Fatalf("GetBranches (empty repo): %v", err)
	}
	if branches == nil {
		t.Fatal("GetBranches (empty repo): returned nil, want empty slice")
	}
	// In a freshly initialized repo without commits, there are no branches.
	if len(branches) != 0 {
		t.Logf("GetBranches (empty repo): %d branches (expected 0)", len(branches))
	}
}

// --- GetCurrentBranch tests ---

func TestGetCurrentBranch_NoProject(t *testing.T) {
	f := &FrontendAPI{}
	_, err := f.GetCurrentBranch()
	if err == nil {
		t.Fatal("expected error when no active project")
	}
}

func TestGetCurrentBranch_NoProjectMode(t *testing.T) {
	f := &FrontendAPI{activeProjectID: "NO_PROJECT", activeProjectPath: t.TempDir()}
	_, err := f.GetCurrentBranch()
	if err == nil {
		t.Fatal("expected error for No Project mode")
	}
}

func TestGetCurrentBranch_ReturnsBranchName(t *testing.T) {
	tmpDir := t.TempDir()
	gitInit(t, tmpDir)
	commitFile(t, tmpDir, "f.txt", "x\n")

	defBranch := gitDefaultBranch(t, tmpDir)

	f := &FrontendAPI{activeProjectPath: tmpDir}
	info, err := f.GetCurrentBranch()
	if err != nil {
		t.Fatalf("GetCurrentBranch: %v", err)
	}
	if info.Name != defBranch {
		t.Errorf("GetCurrentBranch: got %q, want %q", info.Name, defBranch)
	}
	if info.Upstream != "" {
		t.Errorf("GetCurrentBranch: upstream: got %q, want empty (no upstream configured)", info.Upstream)
	}
	if info.Ahead != 0 || info.Behind != 0 {
		t.Errorf("GetCurrentBranch: ahead/behind: got %d/%d, want 0/0", info.Ahead, info.Behind)
	}
}

func TestGetCurrentBranch_DetachedHead(t *testing.T) {
	tmpDir := t.TempDir()
	gitInit(t, tmpDir)
	commitFile(t, tmpDir, "f.txt", "x\n")

	// Detach HEAD.
	runGit(t, tmpDir, "checkout", "--detach", "HEAD")

	f := &FrontendAPI{activeProjectPath: tmpDir}
	info, err := f.GetCurrentBranch()
	if err != nil {
		t.Fatalf("GetCurrentBranch (detached): %v", err)
	}
	if info.Name != "HEAD" {
		t.Errorf("GetCurrentBranch (detached): got %q, want HEAD", info.Name)
	}
	if info.Upstream != "" {
		t.Errorf("GetCurrentBranch (detached): upstream: got %q, want empty", info.Upstream)
	}
}

// --- Event emission tests ---

func TestEventEmitted_StageFile(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, dir string) {
		events := make([]struct {
			name    string
			payload string
		}, 0)
		f.emitEvent = func(name string, args ...any) {
			if len(args) >= 1 {
				if path, ok := args[0].(string); ok {
					events = append(events, struct {
						name    string
						payload string
					}{name, path})
				}
			}
		}

		path := filepath.Join(dir, "eventtest.txt")
		if err := os.WriteFile(path, []byte("hello\n"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		if err := f.StageFile(path); err != nil {
			t.Fatalf("StageFile: %v", err)
		}

		if len(events) != 1 {
			t.Fatalf("expected 1 event, got %d", len(events))
		}
		if events[0].name != EventGitStatusChanged {
			t.Errorf("event name: got %q, want %q", events[0].name, EventGitStatusChanged)
		}
		if events[0].payload != dir {
			t.Errorf("event payload: got %q, want %q", events[0].payload, dir)
		}
	})
}

func TestEventEmitted_UnstageFile(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, dir string) {
		path := filepath.Join(dir, "eventtest.txt")
		if err := os.WriteFile(path, []byte("hello\n"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		runGit(t, dir, "add", "eventtest.txt")

		emitted := false
		var payload string
		f.emitEvent = func(name string, args ...any) {
			if name == EventGitStatusChanged && len(args) >= 1 {
				emitted = true
				payload, _ = args[0].(string)
			}
		}

		if err := f.UnstageFile(path); err != nil {
			t.Fatalf("UnstageFile: %v", err)
		}

		if !emitted {
			t.Fatal("expected git:status_changed event after UnstageFile")
		}
		if payload != dir {
			t.Errorf("event payload: got %q, want %q", payload, dir)
		}
	})
}

func TestEventEmitted_StageAll(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, dir string) {
		emitted := false
		var payload string
		f.emitEvent = func(name string, args ...any) {
			if name == EventGitStatusChanged && len(args) >= 1 {
				emitted = true
				payload, _ = args[0].(string)
			}
		}

		if err := f.StageAll(); err != nil {
			t.Fatalf("StageAll: %v", err)
		}

		if !emitted {
			t.Fatal("expected git:status_changed event after StageAll")
		}
		if payload != dir {
			t.Errorf("event payload: got %q, want %q", payload, dir)
		}
	})
}

func TestEventEmitted_UnstageAll(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, dir string) {
		emitted := false
		var payload string
		f.emitEvent = func(name string, args ...any) {
			if name == EventGitStatusChanged && len(args) >= 1 {
				emitted = true
				payload, _ = args[0].(string)
			}
		}

		if err := f.UnstageAll(); err != nil {
			t.Fatalf("UnstageAll: %v", err)
		}

		if !emitted {
			t.Fatal("expected git:status_changed event after UnstageAll")
		}
		if payload != dir {
			t.Errorf("event payload: got %q, want %q", payload, dir)
		}
	})
}

func TestEventEmitted_Commit(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, dir string) {
		var emitted []string
		f.emitEvent = func(name string, args ...any) {
			if name == EventGitStatusChanged && len(args) >= 1 {
				if p, ok := args[0].(string); ok {
					emitted = append(emitted, p)
				}
			}
		}

		path := filepath.Join(dir, "eventcommit.txt")
		if err := os.WriteFile(path, []byte("hello\n"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		if err := f.StageFile(path); err != nil {
			t.Fatalf("StageFile: %v", err)
		}

		if _, err := f.Commit("event commit"); err != nil {
			t.Fatalf("Commit: %v", err)
		}

		if len(emitted) < 2 {
			t.Fatalf("expected at least 2 events (stage + commit), got %d", len(emitted))
		}
		if emitted[len(emitted)-1] != dir {
			t.Errorf("event payload: got %q, want %q", emitted[len(emitted)-1], dir)
		}
	})
}

func TestEventNotEmitted_OnError(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, dir string) {
		f.emitEvent = func(name string, args ...any) {
			t.Errorf("unexpected event %q emitted on error path", name)
		}
		// Commit with nothing staged should fail without emitting.
		_, _ = f.Commit("nothing to commit")
	})
}

// --- CheckoutBranch tests ---

func TestCheckoutBranch_NoProject(t *testing.T) {
	f := &FrontendAPI{}
	if err := f.CheckoutBranch("main"); err == nil {
		t.Fatal("expected error when no active project")
	}
}

func TestCheckoutBranch_EmptyName(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, dir string) {
		if err := f.CheckoutBranch("   "); err == nil {
			t.Fatal("expected error for empty branch name")
		}
	})
}

func TestCheckoutBranch_Success(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, dir string) {
		// Create a second branch to switch to.
		runGit(t, dir, "branch", "feature-x")

		var emitted string
		f.emitEvent = func(name string, args ...any) {
			if name == EventGitStatusChanged && len(args) >= 1 {
				if p, ok := args[0].(string); ok {
					emitted = p
				}
			}
		}

		if err := f.CheckoutBranch("feature-x"); err != nil {
			t.Fatalf("CheckoutBranch: %v", err)
		}

		current, err := f.GetCurrentBranch()
		if err != nil {
			t.Fatalf("GetCurrentBranch: %v", err)
		}
		if current.Name != "feature-x" {
			t.Errorf("current branch: got %q, want %q", current.Name, "feature-x")
		}
		if emitted != dir {
			t.Errorf("event payload: got %q, want %q", emitted, dir)
		}
	})
}

func TestCheckoutBranch_LocalChangesOverwritten(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, dir string) {
		// Create a feature branch with a divergent commit that modifies
		// the tracked file committed.txt. Using a tracked-file conflict
		// (rather than an untracked one) exercises git's "local changes
		// would be overwritten by checkout" path.
		mustGit := func(args ...string) {
			t.Helper()
			cmd := exec.CommandContext(context.Background(), "git", args...)
			cmd.Dir = dir
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
			}
		}
		defaultBranch := gitDefaultBranch(t, dir)

		mustGit("branch", "feature-y")
		mustGit("checkout", "feature-y")
		// Modify the tracked file on feature-y and commit.
		if err := os.WriteFile(filepath.Join(dir, "committed.txt"), []byte("v2\n"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		mustGit("add", "committed.txt")
		mustGit("commit", "-m", "modify on feature-y")
		mustGit("checkout", defaultBranch)

		// Modify the same tracked file on the current branch without
		// committing — this conflicts with feature-y's version.
		if err := os.WriteFile(filepath.Join(dir, "committed.txt"), []byte("local-change\n"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}

		err := f.CheckoutBranch("feature-y")
		if err == nil {
			t.Fatal("expected error when local changes would be overwritten")
		}
		if !strings.Contains(err.Error(), "local changes") {
			t.Errorf("expected friendly message, got: %v", err)
		}
	})
}

func TestCheckoutBranch_NonexistentBranch(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, dir string) {
		if err := f.CheckoutBranch("does-not-exist"); err == nil {
			t.Fatal("expected error for nonexistent branch")
		}
	})
}

// --- CreateBranch tests ---

func TestCreateBranch_NoProject(t *testing.T) {
	f := &FrontendAPI{}
	if err := f.CreateBranch("main", ""); err == nil {
		t.Fatal("expected error when no active project")
	}
}

func TestCreateBranch_EmptyName(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, dir string) {
		if err := f.CreateBranch("", ""); err == nil {
			t.Fatal("expected error for empty branch name")
		}
	})
}

func TestCreateBranch_Success(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, dir string) {
		var emitted string
		f.emitEvent = func(name string, args ...any) {
			if name == EventGitStatusChanged && len(args) >= 1 {
				if p, ok := args[0].(string); ok {
					emitted = p
				}
			}
		}

		if err := f.CreateBranch("new-feature", ""); err != nil {
			t.Fatalf("CreateBranch: %v", err)
		}

		current, err := f.GetCurrentBranch()
		if err != nil {
			t.Fatalf("GetCurrentBranch: %v", err)
		}
		if current.Name != "new-feature" {
			t.Errorf("current branch: got %q, want %q", current.Name, "new-feature")
		}
		if emitted != dir {
			t.Errorf("event payload: got %q, want %q", emitted, dir)
		}
	})
}

func TestCreateBranch_AlreadyExists(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, dir string) {
		// The default branch already exists.
		err := f.CreateBranch(gitDefaultBranch(t, dir), "")
		if err == nil {
			t.Fatal("expected error for existing branch")
		}
		if !strings.Contains(err.Error(), "already exists") {
			t.Errorf("expected 'already exists' message, got: %v", err)
		}
	})
}

func TestCreateBranch_FromLocalBranch(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, dir string) {
		// Create a second local branch to use as base.
		gitOut(t, dir, "branch", "develop")

		if err := f.CreateBranch("feature", "develop"); err != nil {
			t.Fatalf("CreateBranch from local: %v", err)
		}

		current, err := f.GetCurrentBranch()
		if err != nil {
			t.Fatalf("GetCurrentBranch: %v", err)
		}
		if current.Name != "feature" {
			t.Errorf("current branch: got %q, want %q", current.Name, "feature")
		}
	})
}

func TestCreateBranch_FromRemoteBranch_Track(t *testing.T) {
	remoteDir := t.TempDir()
	gitOut(t, remoteDir, "init", "--bare")

	localDir := t.TempDir()
	gitInit(t, localDir)
	commitFile(t, localDir, "a.txt", "a\n")
	gitOut(t, localDir, "remote", "add", "origin", remoteDir)
	branch := gitDefaultBranch(t, localDir)
	gitOut(t, localDir, "push", "-u", "origin", branch)

	f := &FrontendAPI{activeProjectPath: localDir}

	if err := f.CreateBranch("feature", "origin/"+branch); err != nil {
		t.Fatalf("CreateBranch from remote: %v", err)
	}

	current, err := f.GetCurrentBranch()
	if err != nil {
		t.Fatalf("GetCurrentBranch: %v", err)
	}
	if current.Name != "feature" {
		t.Errorf("current branch: got %q, want %q", current.Name, "feature")
	}
	// --track should have set up upstream tracking automatically.
	if current.Upstream != "origin/"+branch {
		t.Errorf("upstream: got %q, want %q", current.Upstream, "origin/"+branch)
	}
}

func TestCreateBranch_FromTag(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, dir string) {
		// Use update-ref to create a lightweight tag, bypassing the
		// global tag.gpgsign=true config that would require an editor.
		gitOut(t, dir, "update-ref", "refs/tags/v1.0", "HEAD")

		if err := f.CreateBranch("release", "v1.0"); err != nil {
			t.Fatalf("CreateBranch from tag: %v", err)
		}

		current, err := f.GetCurrentBranch()
		if err != nil {
			t.Fatalf("GetCurrentBranch: %v", err)
		}
		if current.Name != "release" {
			t.Errorf("current branch: got %q, want %q", current.Name, "release")
		}
	})
}

func TestCreateBranch_FromCommit(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, dir string) {
		sha := gitOut(t, dir, "rev-parse", "--short", "HEAD")

		if err := f.CreateBranch("from-commit", sha); err != nil {
			t.Fatalf("CreateBranch from commit: %v", err)
		}

		current, err := f.GetCurrentBranch()
		if err != nil {
			t.Fatalf("GetCurrentBranch: %v", err)
		}
		if current.Name != "from-commit" {
			t.Errorf("current branch: got %q, want %q", current.Name, "from-commit")
		}
	})
}

func TestCreateBranch_InvalidBase(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, dir string) {
		err := f.CreateBranch("feature", "nonexistent-ref")
		if err == nil {
			t.Fatal("expected error for invalid base ref")
		}
		if !strings.Contains(err.Error(), "not a valid ref") {
			t.Errorf("expected 'not a valid ref' message, got: %v", err)
		}
		if !errors.Is(err, ErrInvalidBaseRef) {
			t.Errorf("expected error to wrap ErrInvalidBaseRef, got: %v", err)
		}
	})
}

// --- GetBranchBases tests ---

func TestGetBranchBases_NoProject(t *testing.T) {
	f := &FrontendAPI{}
	if _, err := f.GetBranchBases(); err == nil {
		t.Fatal("expected error when no active project")
	}
}

func TestGetBranchBases_Types(t *testing.T) {
	// Set up a repo with local branches, a remote, tags, and commits.
	remoteDir := t.TempDir()
	gitOut(t, remoteDir, "init", "--bare")

	localDir := t.TempDir()
	gitInit(t, localDir)
	commitFile(t, localDir, "a.txt", "a\n")
	gitOut(t, localDir, "remote", "add", "origin", remoteDir)
	branch := gitDefaultBranch(t, localDir)
	gitOut(t, localDir, "push", "-u", "origin", branch)

	// Create a second local branch and a tag.
	gitOut(t, localDir, "branch", "develop")
	// Use update-ref to create a lightweight tag, bypassing the
	// global tag.gpgsign=true config that would require an editor.
	gitOut(t, localDir, "update-ref", "refs/tags/v1.0", "HEAD")

	// Extra commit so we have at least 2 in the log.
	commitFile(t, localDir, "b.txt", "b\n")

	f := &FrontendAPI{activeProjectPath: localDir}
	bases, err := f.GetBranchBases()
	if err != nil {
		t.Fatalf("GetBranchBases: %v", err)
	}

	if len(bases) == 0 {
		t.Fatal("expected at least one base")
	}

	// Verify type ordering: local → remote → tag → commit.
	typeOrder := map[string]int{"local": 0, "remote": 1, "tag": 2, "commit": 3}
	var lastType string
	for _, b := range bases {
		ord, ok := typeOrder[b.Type]
		if !ok {
			t.Errorf("unexpected type %q for base %q", b.Type, b.Ref)
			continue
		}
		if lastType != "" && typeOrder[lastType] > ord {
			t.Errorf("type ordering: %q came after %q", b.Type, lastType)
		}
		lastType = b.Type
	}

	// Verify specific refs are present.
	found := make(map[string]bool)
	for _, b := range bases {
		found[b.Ref] = true
	}
	if !found["develop"] {
		t.Error("expected local branch 'develop' in bases")
	}
	if !found["origin/"+branch] {
		t.Errorf("expected remote branch %q in bases", "origin/"+branch)
	}
	if !found["v1.0"] {
		t.Error("expected tag 'v1.0' in bases")
	}

	// Commits should have non-empty Detail (subject).
	for _, b := range bases {
		if b.Type == "commit" && b.Detail == "" {
			t.Errorf("commit %q has empty detail", b.Ref)
		}
	}
}

func TestGetBranchBases_ExcludesCurrent(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, dir string) {
		branch := gitDefaultBranch(t, dir)

		bases, err := f.GetBranchBases()
		if err != nil {
			t.Fatalf("GetBranchBases: %v", err)
		}

		for _, b := range bases {
			if b.Type == "local" && b.Ref == branch {
				t.Errorf("current branch %q should be excluded from local bases", branch)
			}
		}
	})
}

func TestGetBranchBases_EmptyRepo(t *testing.T) {
	// A repo with no commits — git log fails, for-each-ref returns
	// nothing (unborn HEAD is not a ref yet).
	tmpDir := t.TempDir()
	gitInit(t, tmpDir)

	f := &FrontendAPI{activeProjectPath: tmpDir}
	bases, err := f.GetBranchBases()
	if err != nil {
		t.Fatalf("GetBranchBases on empty repo: %v", err)
	}
	if len(bases) != 0 {
		t.Errorf("expected 0 bases in empty repo, got %d", len(bases))
	}
}

// --- GenerateCommitMessage tests ---

func TestGenerateCommitMessage_NoBuilder(t *testing.T) {
	f := &FrontendAPI{}
	if _, err := f.GenerateCommitMessage(); err == nil {
		t.Fatal("expected error when application not initialized")
	}
}

func TestGenerateCommitMessage_NoProject(t *testing.T) {
	f := &FrontendAPI{builderOverride: &mockBuilder{}}
	if _, err := f.GenerateCommitMessage(); err == nil {
		t.Fatal("expected error when no active project")
	}
}

func TestGenerateCommitMessage_NoStagedChanges(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, dir string) {
		f.builderOverride = &mockBuilder{}
		_, err := f.GenerateCommitMessage()
		if err == nil {
			t.Fatal("expected error when there are no staged changes")
		}
		if !strings.Contains(err.Error(), "no staged changes") {
			t.Errorf("expected 'no staged changes' error, got: %v", err)
		}
	})
}

func TestGenerateCommitMessage_Delegates(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, dir string) {
		// Stage a change so `git diff --staged` produces a non-empty diff.
		if err := os.WriteFile(filepath.Join(dir, "newfile.txt"), []byte("hello\n"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		runGit(t, dir, "add", "newfile.txt")

		mock := &mockBuilder{
			generateCommitMsgRes: "feat: add new thing",
		}
		f.builderOverride = mock

		out, err := f.GenerateCommitMessage()
		if err != nil {
			t.Fatalf("GenerateCommitMessage: %v", err)
		}
		if out != "feat: add new thing" {
			t.Errorf("got %q, want %q", out, "feat: add new thing")
		}
		if mock.generateCommitMsgCalls != 1 {
			t.Errorf("expected 1 builder call, got %d", mock.generateCommitMsgCalls)
		}
		// The diff passed to the builder must be the staged diff and
		// contain the new file's content.
		if !strings.Contains(mock.generateCommitMsgDiff, "newfile.txt") {
			t.Errorf("expected staged diff to mention newfile.txt, got: %q", mock.generateCommitMsgDiff)
		}
		if !strings.Contains(mock.generateCommitMsgDiff, "+hello") {
			t.Errorf("expected staged diff to contain the added line, got: %q", mock.generateCommitMsgDiff)
		}
	})
}

func TestGenerateCommitMessage_PropagatesError(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, dir string) {
		// Stage a change so generation reaches the builder.
		if err := os.WriteFile(filepath.Join(dir, "newfile.txt"), []byte("hello\n"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		runGit(t, dir, "add", "newfile.txt")

		mock := &mockBuilder{
			generateCommitMsgErr: errors.New("llm router not available"),
		}
		f.builderOverride = mock

		if _, err := f.GenerateCommitMessage(); err == nil {
			t.Fatal("expected error to propagate")
		}
	})
}

// ---------------------------------------------------------------------------
// Phase 5 tests: remote operations, commit history, stash
// ---------------------------------------------------------------------------

// gitOut runs a git command in dir and returns trimmed stdout. It fails
// the test (not skip) on error so integration assertions are reliable.
// Safe to call after gitInit: by then git is known to be present.
func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), "git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(out))
}

// --- parser tests (deterministic, no git needed) ---

func TestParseCommitFiles(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []CommitFile
	}{
		{name: "empty", input: "", want: []CommitFile{}},
		{name: "added", input: "A\ta.txt", want: []CommitFile{{Status: "A", Path: "a.txt"}}},
		{name: "modified", input: "M\tb.txt", want: []CommitFile{{Status: "M", Path: "b.txt"}}},
		{name: "deleted", input: "D\tc.txt", want: []CommitFile{{Status: "D", Path: "c.txt"}}},
		{name: "rename with score normalized", input: "R100\told.txt\tnew.txt", want: []CommitFile{{Status: "R", Path: "new.txt"}}},
		{name: "multiple", input: "A\ta.txt\nD\tb.txt\nM\tc.txt", want: []CommitFile{
			{Status: "A", Path: "a.txt"},
			{Status: "D", Path: "b.txt"},
			{Status: "M", Path: "c.txt"},
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parseCommitFiles(tc.input)
			if len(got) != len(tc.want) {
				t.Fatalf("len: got %d, want %d (%+v)", len(got), len(tc.want), got)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("[%d]: got %+v, want %+v", i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestParseStashList(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []StashEntry
	}{
		{name: "empty", input: "", want: []StashEntry{}},
		{name: "wip", input: "stash@{0}: WIP on main: abc1234 msg", want: []StashEntry{{Index: 0, Message: "WIP on main: abc1234 msg"}}},
		{name: "on branch", input: "stash@{1}: On main: my message", want: []StashEntry{{Index: 1, Message: "On main: my message"}}},
		{name: "multiple", input: "stash@{0}: WIP on main: m0\nstash@{1}: On main: m1", want: []StashEntry{
			{Index: 0, Message: "WIP on main: m0"},
			{Index: 1, Message: "On main: m1"},
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parseStashList(tc.input)
			if len(got) != len(tc.want) {
				t.Fatalf("len: got %d, want %d (%+v)", len(got), len(tc.want), got)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("[%d]: got %+v, want %+v", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// --- GetCommitFiles ---

func TestGetCommitFiles(t *testing.T) {
	tmpDir := t.TempDir()
	gitInit(t, tmpDir)
	commitFile(t, tmpDir, "keep.txt", "k\n")
	commitFile(t, tmpDir, "del.txt", "d\n")
	commitFile(t, tmpDir, "mod.txt", "v1\n")

	f := &FrontendAPI{activeProjectPath: tmpDir}

	// A mixed commit: add, modify, delete, rename.
	if err := os.WriteFile(filepath.Join(tmpDir, "new.txt"), []byte("n\n"), 0o644); err != nil {
		t.Fatalf("write new: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "mod.txt"), []byte("v2\n"), 0o644); err != nil {
		t.Fatalf("write mod: %v", err)
	}
	runGit(t, tmpDir, "rm", "del.txt")
	runGit(t, tmpDir, "mv", "keep.txt", "renamed.txt")
	runGit(t, tmpDir, "add", "-A")
	runGit(t, tmpDir, "commit", "-m", "mixed changes")

	sha := gitOut(t, tmpDir, "rev-parse", "HEAD")

	files, err := f.GetCommitFiles(sha)
	if err != nil {
		t.Fatalf("GetCommitFiles: %v", err)
	}

	byPath := map[string]string{}
	for _, cf := range files {
		byPath[cf.Path] = cf.Status
	}
	if byPath["new.txt"] != "A" {
		t.Errorf("new.txt: got %q, want A", byPath["new.txt"])
	}
	if byPath["mod.txt"] != "M" {
		t.Errorf("mod.txt: got %q, want M", byPath["mod.txt"])
	}
	if byPath["del.txt"] != "D" {
		t.Errorf("del.txt: got %q, want D", byPath["del.txt"])
	}
	if byPath["renamed.txt"] != "R" {
		t.Errorf("renamed.txt: got %q, want R", byPath["renamed.txt"])
	}
}

func TestGetCommitFiles_EmptySHA(t *testing.T) {
	f := &FrontendAPI{activeProjectPath: t.TempDir()}
	if _, err := f.GetCommitFiles(""); err == nil {
		t.Fatal("expected error for empty sha")
	}
	if _, err := f.GetCommitFiles("   "); err == nil {
		t.Fatal("expected error for whitespace-only sha")
	}
}

// --- Stash ---

func TestStashCreateListPop(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, dir string) {
		// No stashes initially.
		list, err := f.StashList()
		if err != nil {
			t.Fatalf("StashList (initial): %v", err)
		}
		if len(list) != 0 {
			t.Errorf("initial stash list: got %d, want 0", len(list))
		}

		// Modify a tracked file and stash it.
		if err := os.WriteFile(filepath.Join(dir, "committed.txt"), []byte("modified\n"), 0o644); err != nil {
			t.Fatalf("write committed: %v", err)
		}
		if err := f.StashCreate("my stash"); err != nil {
			t.Fatalf("StashCreate: %v", err)
		}

		// Stash list now has one entry.
		list, err = f.StashList()
		if err != nil {
			t.Fatalf("StashList (after create): %v", err)
		}
		if len(list) != 1 {
			t.Fatalf("stash list: got %d, want 1", len(list))
		}
		if list[0].Index != 0 {
			t.Errorf("stash index: got %d, want 0", list[0].Index)
		}
		if !strings.Contains(list[0].Message, "my stash") {
			t.Errorf("stash message: got %q, want to contain %q", list[0].Message, "my stash")
		}

		// After stash the tracked file reverts to its committed content.
		got, _ := os.ReadFile(filepath.Join(dir, "committed.txt"))
		if string(got) != "v1\n" {
			t.Errorf("after stash: committed.txt = %q, want %q", string(got), "v1\n")
		}

		// Pop restores the modification.
		if err := f.StashPop(0); err != nil {
			t.Fatalf("StashPop: %v", err)
		}
		got, _ = os.ReadFile(filepath.Join(dir, "committed.txt"))
		if string(got) != "modified\n" {
			t.Errorf("after pop: committed.txt = %q, want %q", string(got), "modified\n")
		}

		// Stash list is empty again after pop.
		list, err = f.StashList()
		if err != nil {
			t.Fatalf("StashList (after pop): %v", err)
		}
		if len(list) != 0 {
			t.Errorf("after pop stash list: got %d, want 0", len(list))
		}
	})
}

func TestStashPop_NegativeIndex(t *testing.T) {
	f := &FrontendAPI{}
	if err := f.StashPop(-1); err == nil {
		t.Fatal("expected error for negative stash index")
	}
}

func TestStashDrop_NegativeIndex(t *testing.T) {
	f := &FrontendAPI{}
	if err := f.StashDrop(-1); err == nil {
		t.Fatal("expected error for negative stash index")
	}
}

func TestStashDrop_RemovesStash(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, dir string) {
		// Stash a modification.
		if err := os.WriteFile(filepath.Join(dir, "committed.txt"), []byte("modified\n"), 0o644); err != nil {
			t.Fatalf("write committed: %v", err)
		}
		if err := f.StashCreate("drop me"); err != nil {
			t.Fatalf("StashCreate: %v", err)
		}

		list, err := f.StashList()
		if err != nil {
			t.Fatalf("StashList: %v", err)
		}
		if len(list) != 1 {
			t.Fatalf("stash list: got %d, want 1", len(list))
		}

		// Drop removes the stash without applying it.
		if err := f.StashDrop(0); err != nil {
			t.Fatalf("StashDrop: %v", err)
		}
		list, err = f.StashList()
		if err != nil {
			t.Fatalf("StashList (after drop): %v", err)
		}
		if len(list) != 0 {
			t.Errorf("after drop stash list: got %d, want 0", len(list))
		}

		// Dropping does not restore the working-tree change.
		got, _ := os.ReadFile(filepath.Join(dir, "committed.txt"))
		if string(got) != "v1\n" {
			t.Errorf("after drop: committed.txt = %q, want %q", string(got), "v1\n")
		}
	})
}

func TestEventEmitted_StashDrop(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, dir string) {
		var emitted string
		f.emitEvent = func(name string, args ...any) {
			if name == EventGitStatusChanged && len(args) >= 1 {
				emitted, _ = args[0].(string)
			}
		}
		if err := os.WriteFile(filepath.Join(dir, "committed.txt"), []byte("changed\n"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		if err := f.StashCreate("to drop"); err != nil {
			t.Fatalf("StashCreate: %v", err)
		}
		// Reset the captured payload from StashCreate before dropping.
		emitted = ""
		if err := f.StashDrop(0); err != nil {
			t.Fatalf("StashDrop: %v", err)
		}
		if emitted != dir {
			t.Errorf("event payload: got %q, want %q", emitted, dir)
		}
	})
}

func TestEventEmitted_StashCreate(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, dir string) {
		var emitted string
		f.emitEvent = func(name string, args ...any) {
			if name == EventGitStatusChanged && len(args) >= 1 {
				emitted, _ = args[0].(string)
			}
		}
		if err := os.WriteFile(filepath.Join(dir, "committed.txt"), []byte("changed\n"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		if err := f.StashCreate("event test"); err != nil {
			t.Fatalf("StashCreate: %v", err)
		}
		if emitted != dir {
			t.Errorf("event payload: got %q, want %q", emitted, dir)
		}
	})
}

// --- GetCurrentBranch ahead/behind ---

func TestGetCurrentBranch_AheadBehind(t *testing.T) {
	localDir := t.TempDir()
	gitInit(t, localDir)
	commitFile(t, localDir, "a.txt", "a\n")

	// Bare remote + upstream tracking via push -u.
	remoteDir := t.TempDir()
	gitOut(t, remoteDir, "init", "--bare")
	gitOut(t, localDir, "remote", "add", "origin", remoteDir)
	branch := gitDefaultBranch(t, localDir)
	gitOut(t, localDir, "push", "-u", "origin", branch)

	f := &FrontendAPI{activeProjectPath: localDir}

	// In sync.
	info, err := f.GetCurrentBranch()
	if err != nil {
		t.Fatalf("GetCurrentBranch: %v", err)
	}
	if info.Upstream != "origin/"+branch {
		t.Errorf("upstream: got %q, want %q", info.Upstream, "origin/"+branch)
	}
	if info.Ahead != 0 || info.Behind != 0 {
		t.Errorf("in sync: ahead/behind = %d/%d, want 0/0", info.Ahead, info.Behind)
	}

	// One local commit ahead.
	commitFile(t, localDir, "b.txt", "b\n")
	info, err = f.GetCurrentBranch()
	if err != nil {
		t.Fatalf("GetCurrentBranch (ahead): %v", err)
	}
	if info.Ahead != 1 || info.Behind != 0 {
		t.Errorf("ahead: ahead/behind = %d/%d, want 1/0", info.Ahead, info.Behind)
	}
}

// --- remote operations via a local bare remote ---

func TestPush_LocalRemote(t *testing.T) {
	remoteDir := t.TempDir()
	gitOut(t, remoteDir, "init", "--bare")

	localDir := t.TempDir()
	gitInit(t, localDir)
	commitFile(t, localDir, "a.txt", "a\n")
	gitOut(t, localDir, "remote", "add", "origin", remoteDir)
	branch := gitDefaultBranch(t, localDir)
	// Set upstream (and seed the remote) outside the RPC under test.
	gitOut(t, localDir, "push", "-u", "origin", branch)

	f := &FrontendAPI{activeProjectPath: localDir}
	var emitted string
	f.emitEvent = func(name string, args ...any) {
		if name == EventGitStatusChanged && len(args) >= 1 {
			emitted, _ = args[0].(string)
		}
	}

	// New local commit to push via the RPC.
	commitFile(t, localDir, "b.txt", "b\n")
	if _, err := f.Push("origin", nil); err != nil {
		t.Fatalf("Push: %v", err)
	}
	if emitted != localDir {
		t.Errorf("event payload: got %q, want %q", emitted, localDir)
	}

	// The bare remote should now point at the new local commit.
	remoteHead := gitOut(t, remoteDir, "rev-parse", "refs/heads/"+branch)
	localHead := gitOut(t, localDir, "rev-parse", "HEAD")
	if remoteHead != localHead {
		t.Errorf("after push: remote head %q != local head %q", remoteHead, localHead)
	}
}

func TestFetchAndPull_LocalRemote(t *testing.T) {
	remoteDir := t.TempDir()
	gitOut(t, remoteDir, "init", "--bare")

	localDir := t.TempDir()
	gitInit(t, localDir)
	commitFile(t, localDir, "a.txt", "a\n")
	gitOut(t, localDir, "remote", "add", "origin", remoteDir)
	branch := gitDefaultBranch(t, localDir)
	gitOut(t, localDir, "push", "-u", "origin", branch)

	f := &FrontendAPI{activeProjectPath: localDir}

	// Advance the remote from a clone so local falls behind.
	cloneParent := t.TempDir()
	cloneDir := filepath.Join(cloneParent, "clone")
	gitOut(t, cloneParent, "clone", remoteDir, cloneDir)
	runGit(t, cloneDir, "config", "user.email", "test@test.com")
	runGit(t, cloneDir, "config", "user.name", "Test")
	commitFile(t, cloneDir, "b.txt", "b\n")
	runGit(t, cloneDir, "push", "origin", branch)

	// Fetch updates origin/<branch>; local is now behind by 1.
	if _, err := f.Fetch("origin", nil); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	info, err := f.GetCurrentBranch()
	if err != nil {
		t.Fatalf("GetCurrentBranch after fetch: %v", err)
	}
	if info.Behind != 1 || info.Ahead != 0 {
		t.Errorf("after fetch: behind/ahead = %d/%d, want 1/0", info.Behind, info.Ahead)
	}
	// Fetch must not merge: b.txt still absent locally.
	if _, err := os.Stat(filepath.Join(localDir, "b.txt")); !os.IsNotExist(err) {
		t.Errorf("after fetch: b.txt should not exist yet, stat err: %v", err)
	}

	// Pull fast-forwards local to include b.txt.
	if _, err := f.Pull("origin", nil); err != nil {
		t.Fatalf("Pull: %v", err)
	}
	if _, err := os.Stat(filepath.Join(localDir, "b.txt")); err != nil {
		t.Errorf("after pull: b.txt should exist, stat err: %v", err)
	}
}

// --- error paths ---

func TestPhase5Git_NoProject(t *testing.T) {
	f := &FrontendAPI{}
	if _, err := f.Pull("origin", nil); err == nil {
		t.Error("Pull: expected error")
	}
	if _, err := f.Push("origin", nil); err == nil {
		t.Error("Push: expected error")
	}
	if _, err := f.Fetch("origin", nil); err == nil {
		t.Error("Fetch: expected error")
	}
	if _, err := f.GetCommitFiles("abc"); err == nil {
		t.Error("GetCommitFiles: expected error")
	}
	if err := f.StashCreate("msg"); err == nil {
		t.Error("StashCreate: expected error")
	}
	if err := f.StashPop(0); err == nil {
		t.Error("StashPop: expected error")
	}
	if _, err := f.StashList(); err == nil {
		t.Error("StashList: expected error")
	}
}

func TestPhase5Git_NoProjectMode(t *testing.T) {
	f := &FrontendAPI{activeProjectID: "NO_PROJECT", activeProjectPath: t.TempDir()}
	if _, err := f.Pull("origin", nil); err == nil {
		t.Error("Pull: expected error")
	}
	if _, err := f.Push("origin", nil); err == nil {
		t.Error("Push: expected error")
	}
	if _, err := f.Fetch("origin", nil); err == nil {
		t.Error("Fetch: expected error")
	}
	if _, err := f.GetCommitFiles("abc"); err == nil {
		t.Error("GetCommitFiles: expected error")
	}
	if err := f.StashCreate("msg"); err == nil {
		t.Error("StashCreate: expected error")
	}
	if err := f.StashPop(0); err == nil {
		t.Error("StashPop: expected error")
	}
	if _, err := f.StashList(); err == nil {
		t.Error("StashList: expected error")
	}
}

// --- remote operation flag validation ---

func TestValidateRemoteFlags(t *testing.T) {
	tests := []struct {
		name    string
		op      string
		flags   []string
		wantErr bool
	}{
		{"pull no flags", "pull", nil, false},
		{"pull ff-only", "pull", []string{"--ff-only"}, false},
		{"pull rebase", "pull", []string{"--rebase"}, false},
		{"pull rebase autostash", "pull", []string{"--rebase", "--autostash"}, false},
		{"push force", "push", []string{"--force"}, false},
		{"push force-with-lease", "push", []string{"--force-with-lease"}, false},
		{"push no-verify", "push", []string{"--no-verify"}, false},
		{"fetch tags", "fetch", []string{"--tags"}, false},
		{"fetch prune", "fetch", []string{"--prune"}, false},
		{"pull push-only flag", "pull", []string{"--force"}, true},
		{"push pull-only flag", "push", []string{"--rebase"}, true},
		{"fetch push-only flag", "fetch", []string{"--force"}, true},
		{"pull unknown flag", "pull", []string{"--evil"}, true},
		{"push unknown flag", "push", []string{"--evil"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateRemoteFlags(tt.op, tt.flags)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateRemoteFlags(%q, %v) error = %v, wantErr %v",
					tt.op, tt.flags, err, tt.wantErr)
			}
		})
	}
}

func TestRemoteOp_RejectsInvalidFlags(t *testing.T) {
	// No active project, but flag validation runs first — an invalid flag
	// is rejected before the project check.
	f := &FrontendAPI{}
	if _, err := f.Pull("", []string{"--evil"}); err == nil {
		t.Error("Pull with invalid flag: expected error")
	}
	if _, err := f.Push("", []string{"--evil"}); err == nil {
		t.Error("Push with invalid flag: expected error")
	}
	if _, err := f.Fetch("", []string{"--evil"}); err == nil {
		t.Error("Fetch with invalid flag: expected error")
	}
	// Cross-operation flags are rejected (--force is push-only).
	if _, err := f.Pull("", []string{"--force"}); err == nil {
		t.Error("Pull with push-only flag --force: expected error")
	}
}

// TestPull_FFOnlyFlag_FastForward verifies that the --ff-only flag is
// actually passed to git: when local is behind and can fast-forward,
// pull --ff-only succeeds.
func TestPull_FFOnlyFlag_FastForward(t *testing.T) {
	remoteDir := t.TempDir()
	gitOut(t, remoteDir, "init", "--bare")

	localDir := t.TempDir()
	gitInit(t, localDir)
	commitFile(t, localDir, "a.txt", "a\n")
	gitOut(t, localDir, "remote", "add", "origin", remoteDir)
	branch := gitDefaultBranch(t, localDir)
	gitOut(t, localDir, "push", "-u", "origin", branch)

	f := &FrontendAPI{activeProjectPath: localDir}

	// Advance the remote from a clone so local falls behind.
	cloneParent := t.TempDir()
	cloneDir := filepath.Join(cloneParent, "clone")
	gitOut(t, cloneParent, "clone", remoteDir, cloneDir)
	runGit(t, cloneDir, "config", "user.email", "test@test.com")
	runGit(t, cloneDir, "config", "user.name", "Test")
	commitFile(t, cloneDir, "b.txt", "b\n")
	runGit(t, cloneDir, "push", "origin", branch)

	if _, err := f.Fetch("origin", nil); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if _, err := f.Pull("origin", []string{"--ff-only"}); err != nil {
		t.Fatalf("Pull --ff-only (fast-forward): %v", err)
	}
	if _, err := os.Stat(filepath.Join(localDir, "b.txt")); err != nil {
		t.Errorf("after pull --ff-only: b.txt should exist, stat err: %v", err)
	}
}

// TestPull_FFOnlyFlag_Diverged verifies that the --ff-only flag is
// actually passed to git: when local and remote have diverged, pull
// --ff-only fails (not a fast-forward) instead of creating a merge.
func TestPull_FFOnlyFlag_Diverged(t *testing.T) {
	remoteDir := t.TempDir()
	gitOut(t, remoteDir, "init", "--bare")

	localDir := t.TempDir()
	gitInit(t, localDir)
	commitFile(t, localDir, "a.txt", "a\n")
	gitOut(t, localDir, "remote", "add", "origin", remoteDir)
	branch := gitDefaultBranch(t, localDir)
	gitOut(t, localDir, "push", "-u", "origin", branch)

	f := &FrontendAPI{activeProjectPath: localDir}

	// Advance the remote from a clone.
	cloneParent := t.TempDir()
	cloneDir := filepath.Join(cloneParent, "clone")
	gitOut(t, cloneParent, "clone", remoteDir, cloneDir)
	runGit(t, cloneDir, "config", "user.email", "test@test.com")
	runGit(t, cloneDir, "config", "user.name", "Test")
	commitFile(t, cloneDir, "b.txt", "b\n")
	runGit(t, cloneDir, "push", "origin", branch)

	// Diverge locally with a different commit.
	commitFile(t, localDir, "c.txt", "c\n")

	if _, err := f.Fetch("origin", nil); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if _, err := f.Pull("origin", []string{"--ff-only"}); err == nil {
		t.Fatal("Pull --ff-only (diverged): expected error, got nil")
	}
}
