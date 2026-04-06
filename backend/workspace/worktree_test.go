package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	git "github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing/object"
	"github.com/stretchr/testify/require"
)

// testSignature returns a deterministic commit author for tests.
func testSignature() *object.Signature {
	return &object.Signature{
		Name:  "Test",
		Email: "test@test.com",
		When:  time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
	}
}

// setupTestRepo creates a temp dir with a git repo containing an initial empty commit.
func setupTestRepo(t *testing.T) (string, *git.Repository) {
	t.Helper()
	dir := t.TempDir()
	repo, err := git.PlainInit(dir, false)
	require.NoError(t, err)

	wt, err := repo.Worktree()
	require.NoError(t, err)

	_, err = wt.Commit("init", &git.CommitOptions{
		AllowEmptyCommits: true,
		Author:            testSignature(),
	})
	require.NoError(t, err)

	return dir, repo
}

// setupTestRepoWithFile creates a repo and commits a single file into it.
func setupTestRepoWithFile(t *testing.T, name, content string) (string, *git.Repository) {
	t.Helper()
	dir, repo := setupTestRepo(t)

	require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644))

	wt, err := repo.Worktree()
	require.NoError(t, err)
	_, err = wt.Add(name)
	require.NoError(t, err)
	_, err = wt.Commit("add "+name, &git.CommitOptions{Author: testSignature()})
	require.NoError(t, err)

	return dir, repo
}

func TestNewWorktreeManager(t *testing.T) {
	rootDir := filepath.Join(t.TempDir(), "root")
	wm := NewWorktreeManager(rootDir, "sess-1", nil)
	require.Equal(t, rootDir, wm.rootDir)
	require.Equal(t, filepath.Join(rootDir, ".c0wrk-worktrees", "sess-1"), wm.worktreeDir)
	require.Equal(t, "work/sess-1", wm.branchName)
}

func TestInitAndLifecycle(t *testing.T) {
	root, repo := setupTestRepo(t)

	// Add a real file and commit it.
	require.NoError(t, os.WriteFile(filepath.Join(root, "hello.txt"), []byte("hello"), 0o644))
	wt, err := repo.Worktree()
	require.NoError(t, err)
	_, err = wt.Add("hello.txt")
	require.NoError(t, err)
	_, err = wt.Commit("add hello", &git.CommitOptions{Author: testSignature()})
	require.NoError(t, err)

	wm := NewWorktreeManager(root, "test-session", nil)
	require.NoError(t, wm.Init())
	require.Equal(t, wm.worktreeDir, wm.WorktreePath())

	// Verify worktree directory exists and contains hello.txt.
	_, err = os.Stat(filepath.Join(wm.worktreeDir, "hello.txt"))
	require.NoError(t, err, "hello.txt not found in worktree")

	t.Cleanup(func() { _ = wm.Cleanup() })
}

func TestGetDiff(t *testing.T) {
	root, _ := setupTestRepoWithFile(t, "tracked.txt", "original\n")

	wm := NewWorktreeManager(root, "diff-session", nil)
	require.NoError(t, wm.Init())
	t.Cleanup(func() { _ = wm.Cleanup() })

	// Modify the tracked file in the worktree.
	require.NoError(t, os.WriteFile(filepath.Join(wm.worktreeDir, "tracked.txt"), []byte("modified\n"), 0o644))

	diff, err := wm.GetDiff()
	require.NoError(t, err)
	require.Contains(t, diff, "tracked.txt")
}

func TestGetDiffStat(t *testing.T) {
	root, _ := setupTestRepoWithFile(t, "stat.txt", "original\n")

	wm := NewWorktreeManager(root, "stat-session", nil)
	require.NoError(t, wm.Init())
	t.Cleanup(func() { _ = wm.Cleanup() })

	require.NoError(t, os.WriteFile(filepath.Join(wm.worktreeDir, "stat.txt"), []byte("modified\n"), 0o644))

	stat, err := wm.GetDiffStat()
	require.NoError(t, err)
	require.Contains(t, stat, "stat.txt")
}

func TestReset(t *testing.T) {
	root, _ := setupTestRepoWithFile(t, "keep.txt", "keep")

	wm := NewWorktreeManager(root, "reset-session", nil)
	require.NoError(t, wm.Init())
	t.Cleanup(func() { _ = wm.Cleanup() })

	// Modify existing file and add a new untracked file.
	require.NoError(t, os.WriteFile(filepath.Join(wm.worktreeDir, "keep.txt"), []byte("modified"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(wm.worktreeDir, "extra.txt"), []byte("extra"), 0o644))

	require.NoError(t, wm.Reset())

	// Verify keep.txt is restored.
	data, err := os.ReadFile(filepath.Join(wm.worktreeDir, "keep.txt"))
	require.NoError(t, err)
	require.Equal(t, "keep", string(data))

	// Verify extra.txt was removed.
	_, err = os.Stat(filepath.Join(wm.worktreeDir, "extra.txt"))
	require.True(t, os.IsNotExist(err), "extra.txt should have been removed by Reset()")
}

func TestMerge(t *testing.T) {
	root, _ := setupTestRepoWithFile(t, "base.txt", "base")

	wm := NewWorktreeManager(root, "merge-session", nil)
	require.NoError(t, wm.Init())
	t.Cleanup(func() { _ = wm.Cleanup() })

	// Create a new file in worktree and merge.
	require.NoError(t, os.WriteFile(filepath.Join(wm.worktreeDir, "merged.txt"), []byte("merged"), 0o644))
	require.NoError(t, wm.Merge("merge test"))

	// Verify the merged file exists in rootDir.
	_, err := os.Stat(filepath.Join(root, "merged.txt"))
	require.NoError(t, err, "merged.txt should exist in rootDir after merge")
}

func TestMergeNoChanges(t *testing.T) {
	root, _ := setupTestRepoWithFile(t, "base.txt", "base")

	wm := NewWorktreeManager(root, "merge-noop-session", nil)
	require.NoError(t, wm.Init())
	t.Cleanup(func() { _ = wm.Cleanup() })

	// Do NOT modify any files — Merge should succeed as a no-op.
	require.NoError(t, wm.Merge("no changes"))

	// Verify the original file still exists in rootDir unchanged.
	data, err := os.ReadFile(filepath.Join(root, "base.txt"))
	require.NoError(t, err, "base.txt should still exist in rootDir")
	require.Equal(t, "base", string(data))
}

func TestCleanup(t *testing.T) {
	root, _ := setupTestRepo(t)

	wm := NewWorktreeManager(root, "cleanup-session", nil)
	require.NoError(t, wm.Init())

	wtDir := wm.worktreeDir
	require.NoError(t, wm.Cleanup())

	_, err := os.Stat(wtDir)
	require.True(t, os.IsNotExist(err), "worktree dir should be removed after Cleanup()")
}

func TestAutoInitNonGitDir(t *testing.T) {
	root := t.TempDir() // no git init — Init() should auto-initialise

	wm := NewWorktreeManager(root, "nogit-session", nil)
	require.NoError(t, wm.Init(), "Init() should auto-init a non-git dir")
	require.NotEqual(t, root, wm.WorktreePath(), "WorktreePath() should be the worktree dir, not rootDir")

	t.Cleanup(func() { _ = wm.Cleanup() })
}

func TestGetDiffTruncation(t *testing.T) {
	root, _ := setupTestRepoWithFile(t, "big.txt", "small\n")

	wm := NewWorktreeManager(root, "trunc-session", nil)
	require.NoError(t, wm.Init())
	t.Cleanup(func() { _ = wm.Cleanup() })

	// Replace tracked file with large content to produce a big diff.
	big := strings.Repeat("x", 50000)
	require.NoError(t, os.WriteFile(filepath.Join(wm.worktreeDir, "big.txt"), []byte(big), 0o644))

	diff, err := wm.GetDiff()
	require.NoError(t, err)
	require.Contains(t, diff, "truncated")
	require.LessOrEqual(t, len(diff), maxDiffBytes+100, "diff too long after truncation")
}

func TestCleanupIdempotent(t *testing.T) {
	root, _ := setupTestRepo(t)

	wm := NewWorktreeManager(root, "idem-session", nil)
	require.NoError(t, wm.Init())

	require.NoError(t, wm.Cleanup(), "first Cleanup()")
	require.NoError(t, wm.Cleanup(), "second Cleanup()")
}

func TestEnsureGitInit(t *testing.T) {
	root := t.TempDir()

	wm := NewWorktreeManager(root, "init-session", nil)
	require.NoError(t, wm.EnsureGitInit())

	// Verify .git directory exists.
	info, err := os.Stat(filepath.Join(root, ".git"))
	require.NoError(t, err, ".git should exist after EnsureGitInit")
	require.True(t, info.IsDir(), ".git should be a directory")
}

func TestResetEmptyTrackedFiles(t *testing.T) {
	root, _ := setupTestRepo(t) // empty commit — no tracked files

	wm := NewWorktreeManager(root, "empty-reset-session", nil)
	require.NoError(t, wm.Init())
	t.Cleanup(func() { _ = wm.Cleanup() })

	// Create an untracked file in the worktree.
	untrackedPath := filepath.Join(wm.worktreeDir, "untracked.txt")
	require.NoError(t, os.WriteFile(untrackedPath, []byte("should be cleaned"), 0o644))

	// Reset should succeed even though HEAD has no tracked files.
	require.NoError(t, wm.Reset())

	// Verify the untracked file was removed by git clean.
	_, err := os.Stat(untrackedPath)
	require.True(t, os.IsNotExist(err), "untracked.txt should have been removed by Reset()")
}

func TestExternalWorkspaceFullLifecycle(t *testing.T) {
	// 1. Create a temporary directory simulating a user's existing project.
	root, repo := setupTestRepo(t)

	// 2. Add real project files.
	require.NoError(t, os.WriteFile(filepath.Join(root, "README.md"), []byte("# My Project\n"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "src"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "src", "main.go"), []byte("package main\n"), 0o644))

	wt, err := repo.Worktree()
	require.NoError(t, err)
	_, err = wt.Add("README.md")
	require.NoError(t, err)
	_, err = wt.Add(filepath.Join("src", "main.go"))
	require.NoError(t, err)
	_, err = wt.Commit("initial project files", &git.CommitOptions{Author: testSignature()})
	require.NoError(t, err)

	// Record HEAD before Init to verify repo reuse.
	headBefore, err := repo.Head()
	require.NoError(t, err)
	headHashBefore := headBefore.Hash()

	// 3. Create a WorktreeManager pointing at the existing repo.
	wm := NewWorktreeManager(root, "external-session", nil)

	// 4. Init() must reuse the existing repo (not re-init).
	require.NoError(t, wm.Init())
	t.Cleanup(func() { _ = wm.Cleanup() })

	require.NotEqual(t, root, wm.WorktreePath(), "WorktreePath() should differ from rootDir")

	// HEAD should be unchanged — Init reused the repo, didn't re-init.
	headAfter, err := repo.Head()
	require.NoError(t, err)
	require.Equal(t, headHashBefore, headAfter.Hash(), "HEAD changed after Init() — repo was re-initialised")

	// Verify the commit log still has our original commit message.
	logIter, err := repo.Log(&git.LogOptions{})
	require.NoError(t, err)
	foundOriginalCommit := false
	err = logIter.ForEach(func(c *object.Commit) error {
		if strings.Contains(c.Message, "initial project files") {
			foundOriginalCommit = true
		}
		return nil
	})
	require.NoError(t, err)
	require.True(t, foundOriginalCommit, "original commit message lost from log")

	// 5. Verify the worktree contains the files from the original repo.
	for _, rel := range []string{"README.md", filepath.Join("src", "main.go")} {
		wtFile := filepath.Join(wm.WorktreePath(), rel)
		_, err := os.Stat(wtFile)
		require.NoError(t, err, "expected %s in worktree", rel)
	}
	data, err := os.ReadFile(filepath.Join(wm.WorktreePath(), "README.md"))
	require.NoError(t, err)
	require.Equal(t, "# My Project\n", string(data))

	// 6. Modify an existing file and add a new file in the worktree.
	require.NoError(t, os.WriteFile(filepath.Join(wm.WorktreePath(), "README.md"), []byte("# My Project\nUpdated.\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(wm.WorktreePath(), "src", "util.go"), []byte("package main\n// util\n"), 0o644))

	// 7. Merge changes back to the original repo's main branch.
	require.NoError(t, wm.Merge("add util and update readme"))

	// Verify merged changes are present in rootDir.
	readmeData, err := os.ReadFile(filepath.Join(root, "README.md"))
	require.NoError(t, err, "README.md should exist in rootDir after merge")
	require.Contains(t, string(readmeData), "Updated.")

	_, err = os.Stat(filepath.Join(root, "src", "util.go"))
	require.NoError(t, err, "src/util.go should exist in rootDir after merge")

	// 8. Cleanup and verify the worktree is removed but original repo is intact.
	wtDir := wm.WorktreePath()
	require.NoError(t, wm.Cleanup())

	_, err = os.Stat(wtDir)
	require.True(t, os.IsNotExist(err), "worktree dir should be removed after Cleanup()")

	// Original repo must still be intact with all files.
	for _, rel := range []string{".git", "README.md", filepath.Join("src", "main.go"), filepath.Join("src", "util.go")} {
		_, err := os.Stat(filepath.Join(root, rel))
		require.NoError(t, err, "original repo file %s should still exist after Cleanup", rel)
	}

	// Verify the git log in the original repo contains the merge commit.
	mergeLogIter, err := repo.Log(&git.LogOptions{})
	require.NoError(t, err)
	foundMergeCommit := false
	err = mergeLogIter.ForEach(func(c *object.Commit) error {
		if strings.Contains(c.Message, "add util and update readme") {
			foundMergeCommit = true
		}
		return nil
	})
	require.NoError(t, err)
	require.True(t, foundMergeCommit, "merge commit message not found in original repo log")
}

func TestGetDiffPreservesChanges(t *testing.T) {
	root, _ := setupTestRepoWithFile(t, "preserve.txt", "original\n")

	wm := NewWorktreeManager(root, "preserve-session", nil)
	require.NoError(t, wm.Init())
	t.Cleanup(func() { _ = wm.Cleanup() })

	modifiedContent := "modified content\n"
	require.NoError(t, os.WriteFile(filepath.Join(wm.worktreeDir, "preserve.txt"), []byte(modifiedContent), 0o644))

	// Call GetDiff — should NOT destroy working directory changes.
	diff, err := wm.GetDiff()
	require.NoError(t, err)
	require.Contains(t, diff, "preserve.txt")

	// Verify the file is still modified after GetDiff.
	data, err := os.ReadFile(filepath.Join(wm.worktreeDir, "preserve.txt"))
	require.NoError(t, err)
	require.Equal(t, modifiedContent, string(data), "GetDiff destroyed working directory changes")

	// Call GetDiffStat — should also preserve working directory changes.
	stat, err := wm.GetDiffStat()
	require.NoError(t, err)
	require.Contains(t, stat, "preserve.txt")

	// Verify the file is still modified after GetDiffStat.
	data, err = os.ReadFile(filepath.Join(wm.worktreeDir, "preserve.txt"))
	require.NoError(t, err)
	require.Equal(t, modifiedContent, string(data), "GetDiffStat destroyed working directory changes")
}

func TestGetDiffThenMerge(t *testing.T) {
	root, _ := setupTestRepoWithFile(t, "flow.txt", "original\n")

	wm := NewWorktreeManager(root, "flow-session", nil)
	require.NoError(t, wm.Init())
	t.Cleanup(func() { _ = wm.Cleanup() })

	// Modify existing file and add a new file in the worktree.
	require.NoError(t, os.WriteFile(filepath.Join(wm.worktreeDir, "flow.txt"), []byte("updated\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(wm.worktreeDir, "new_file.txt"), []byte("brand new\n"), 0o644))

	// GetDiff should return changes without destroying them.
	diff, err := wm.GetDiff()
	require.NoError(t, err)
	require.Contains(t, diff, "flow.txt")
	require.Contains(t, diff, "new_file.txt")

	// Merge should succeed — changes must still be present.
	require.NoError(t, wm.Merge("flow merge"))

	// Verify changes are present in root working directory.
	data, err := os.ReadFile(filepath.Join(root, "flow.txt"))
	require.NoError(t, err)
	require.Equal(t, "updated\n", string(data), "flow.txt not updated in root after merge")

	data, err = os.ReadFile(filepath.Join(root, "new_file.txt"))
	require.NoError(t, err)
	require.Equal(t, "brand new\n", string(data), "new_file.txt not present in root after merge")
}

func TestMergeFailsOnAdvancedRoot(t *testing.T) {
	root, repo := setupTestRepoWithFile(t, "base.txt", "base\n")

	wm := NewWorktreeManager(root, "advanced-session", nil)
	require.NoError(t, wm.Init())
	t.Cleanup(func() { _ = wm.Cleanup() })

	// Simulate someone else committing to the root repo after worktree was created.
	require.NoError(t, os.WriteFile(filepath.Join(root, "other.txt"), []byte("other change\n"), 0o644))
	rootWt, err := repo.Worktree()
	require.NoError(t, err)
	_, err = rootWt.Add("other.txt")
	require.NoError(t, err)
	_, err = rootWt.Commit("external commit", &git.CommitOptions{Author: testSignature()})
	require.NoError(t, err)

	// Make a change in the worktree.
	require.NoError(t, os.WriteFile(filepath.Join(wm.worktreeDir, "base.txt"), []byte("modified\n"), 0o644))

	// Merge should fail because root branch has advanced.
	err = wm.Merge("should fail")
	require.Error(t, err)
	require.Contains(t, err.Error(), "cannot fast-forward")
	require.Contains(t, err.Error(), "root branch has advanced")
}
