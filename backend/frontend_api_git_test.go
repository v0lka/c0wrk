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
	err := f.Commit("test")
	if err == nil {
		t.Fatal("expected error when no active project")
	}
}

func TestCommit_NoProjectMode(t *testing.T) {
	f := &FrontendAPI{activeProjectID: "NO_PROJECT", activeProjectPath: t.TempDir()}
	err := f.Commit("test")
	if err == nil {
		t.Fatal("expected error for No Project mode")
	}
}

func TestCommit_EmptyMessage(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, dir string) {
		err := f.Commit("")
		if err == nil {
			t.Fatal("expected error for empty commit message")
		}
	})
}

func TestCommit_WhitespaceMessage(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, dir string) {
		err := f.Commit("\n  \t")
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

		// Commit.
		if err := f.Commit("add commitme.txt"); err != nil {
			t.Fatalf("Commit: %v", err)
		}

		// Verify via git log.
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
		err := f.Commit("empty commit")
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
	name, err := f.GetCurrentBranch()
	if err != nil {
		t.Fatalf("GetCurrentBranch: %v", err)
	}
	if name != defBranch {
		t.Errorf("GetCurrentBranch: got %q, want %q", name, defBranch)
	}
}

func TestGetCurrentBranch_DetachedHead(t *testing.T) {
	tmpDir := t.TempDir()
	gitInit(t, tmpDir)
	commitFile(t, tmpDir, "f.txt", "x\n")

	// Detach HEAD.
	runGit(t, tmpDir, "checkout", "--detach", "HEAD")

	f := &FrontendAPI{activeProjectPath: tmpDir}
	name, err := f.GetCurrentBranch()
	if err != nil {
		t.Fatalf("GetCurrentBranch (detached): %v", err)
	}
	if name != "HEAD" {
		t.Errorf("GetCurrentBranch (detached): got %q, want HEAD", name)
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

		if err := f.Commit("event commit"); err != nil {
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
		_ = f.Commit("nothing to commit")
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
		if current != "feature-x" {
			t.Errorf("current branch: got %q, want %q", current, "feature-x")
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
	if err := f.CreateBranch("main"); err == nil {
		t.Fatal("expected error when no active project")
	}
}

func TestCreateBranch_EmptyName(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, dir string) {
		if err := f.CreateBranch(""); err == nil {
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

		if err := f.CreateBranch("new-feature"); err != nil {
			t.Fatalf("CreateBranch: %v", err)
		}

		current, err := f.GetCurrentBranch()
		if err != nil {
			t.Fatalf("GetCurrentBranch: %v", err)
		}
		if current != "new-feature" {
			t.Errorf("current branch: got %q, want %q", current, "new-feature")
		}
		if emitted != dir {
			t.Errorf("event payload: got %q, want %q", emitted, dir)
		}
	})
}

func TestCreateBranch_AlreadyExists(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, dir string) {
		// The default branch already exists.
		err := f.CreateBranch(gitDefaultBranch(t, dir))
		if err == nil {
			t.Fatal("expected error for existing branch")
		}
		if !strings.Contains(err.Error(), "already exists") {
			t.Errorf("expected 'already exists' message, got: %v", err)
		}
	})
}

// --- GenerateCommitMessage tests ---

func TestGenerateCommitMessage_NoBuilder(t *testing.T) {
	f := &FrontendAPI{}
	if _, err := f.GenerateCommitMessage("diff"); err == nil {
		t.Fatal("expected error when application not initialized")
	}
}

func TestGenerateCommitMessage_EmptyDiff(t *testing.T) {
	f := &FrontendAPI{builderOverride: &mockBuilder{}}
	if _, err := f.GenerateCommitMessage("   "); err == nil {
		t.Fatal("expected error for empty diff")
	}
}

func TestGenerateCommitMessage_Delegates(t *testing.T) {
	mock := &mockBuilder{
		generateCommitMsgRes: "feat: add new thing",
	}
	f := &FrontendAPI{builderOverride: mock}

	out, err := f.GenerateCommitMessage("diff --staged ...")
	if err != nil {
		t.Fatalf("GenerateCommitMessage: %v", err)
	}
	if out != "feat: add new thing" {
		t.Errorf("got %q, want %q", out, "feat: add new thing")
	}
	if mock.generateCommitMsgCalls != 1 {
		t.Errorf("expected 1 builder call, got %d", mock.generateCommitMsgCalls)
	}
}

func TestGenerateCommitMessage_PropagatesError(t *testing.T) {
	mock := &mockBuilder{
		generateCommitMsgErr: errors.New("llm router not available"),
	}
	f := &FrontendAPI{builderOverride: mock}

	if _, err := f.GenerateCommitMessage("diff"); err == nil {
		t.Fatal("expected error to propagate")
	}
}
