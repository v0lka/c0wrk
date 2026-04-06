package workspace

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/go-git/go-billy/v6/osfs"
	git "github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/object"
	"github.com/go-git/go-git/v6/storage/filesystem"
	xworktree "github.com/go-git/go-git/v6/x/plumbing/worktree"
)

const maxDiffBytes = 32768

// worktreeNameRE matches the constraint imposed by the x/plumbing/worktree package.
var worktreeNameRE = regexp.MustCompile(`[^a-zA-Z0-9\-]`)

// WorktreeManager manages a git worktree lifecycle for an isolated session workspace.
type WorktreeManager struct {
	rootDir      string // main workspace path
	worktreeDir  string // .c0wrk-worktrees/<session-id>
	baseCommit   string // commit the worktree was created from
	branchName   string // work/<session-id>
	worktreeName string // sanitised session ID (alphanumeric + hyphens only)
	repo         *git.Repository
	logger       *slog.Logger
}

// NewWorktreeManager creates a WorktreeManager for the given root directory and session.
func NewWorktreeManager(rootDir, sessionID string, logger *slog.Logger) *WorktreeManager {
	return &WorktreeManager{
		rootDir:      rootDir,
		worktreeDir:  filepath.Join(rootDir, ".c0wrk-worktrees", sessionID),
		branchName:   "work/" + sessionID,
		worktreeName: sanitiseWorktreeName(sessionID),
		logger:       logger,
	}
}

// sanitiseWorktreeName replaces characters not matching [a-zA-Z0-9\-] with hyphens.
func sanitiseWorktreeName(name string) string {
	return worktreeNameRE.ReplaceAllString(name, "-")
}

// defaultSignature returns the placeholder commit author signature.
func defaultSignature() *object.Signature {
	return &object.Signature{
		Name:  "c0wrk",
		Email: "agent@c0wrk.local",
		When:  time.Now(),
	}
}

// EnsureGitInit initialises a git repository in rootDir with an initial commit.
func (wm *WorktreeManager) EnsureGitInit() error {
	repo, err := git.PlainInit(wm.rootDir, false)
	if err != nil {
		return fmt.Errorf("git init failed: %w", err)
	}

	wt, err := repo.Worktree()
	if err != nil {
		return fmt.Errorf("get worktree failed: %w", err)
	}

	if err := wt.AddGlob("."); err != nil {
		return fmt.Errorf("git add failed: %w", err)
	}

	_, err = wt.Commit("initial", &git.CommitOptions{
		AllowEmptyCommits: true,
		Author:            defaultSignature(),
	})
	if err != nil {
		return fmt.Errorf("git commit failed: %w", err)
	}

	return nil
}

// Init opens the root repo (initialising if needed) and creates a linked worktree.
func (wm *WorktreeManager) Init() error {
	repo, err := git.PlainOpen(wm.rootDir)
	if err != nil {
		// Workspace is not a git repo — try to auto-initialise one.
		if initErr := wm.EnsureGitInit(); initErr != nil {
			return fmt.Errorf("failed to init git repo: %w", initErr)
		}
		wm.logInfo("auto-initialised git repository in workspace")

		repo, err = git.PlainOpen(wm.rootDir)
		if err != nil {
			return fmt.Errorf("failed to open repo after init: %w", err)
		}
	}
	wm.repo = repo

	// Capture current HEAD commit.
	head, err := repo.Head()
	if err != nil {
		return fmt.Errorf("failed to get HEAD: %w", err)
	}
	wm.baseCommit = head.Hash().String()

	// Ensure parent directory exists.
	if err := os.MkdirAll(filepath.Dir(wm.worktreeDir), 0o755); err != nil {
		return fmt.Errorf("failed to create worktree parent dir: %w", err)
	}

	// Create the linked worktree using x/plumbing/worktree.
	dotgitFs := osfs.New(filepath.Join(wm.rootDir, ".git"), osfs.WithBoundOS())
	stor := filesystem.NewStorageWithOptions(dotgitFs, nil, filesystem.Options{})

	xwt, err := xworktree.New(stor)
	if err != nil {
		return fmt.Errorf("failed to create worktree manager: %w", err)
	}

	wtFs := osfs.New(wm.worktreeDir, osfs.WithBoundOS())
	if err := xwt.Add(wtFs, wm.worktreeName, xworktree.WithCommit(head.Hash())); err != nil {
		return fmt.Errorf("failed to create worktree: %w", err)
	}

	wm.logInfo("worktree created", "path", wm.worktreeDir, "branch", wm.branchName)
	return nil
}

// WorktreePath returns the isolated worktree directory.
func (wm *WorktreeManager) WorktreePath() string {
	return wm.worktreeDir
}

// openWorktreeRepo opens the git repository rooted at the linked worktree directory.
func (wm *WorktreeManager) openWorktreeRepo() (*git.Repository, error) {
	return git.PlainOpen(wm.worktreeDir)
}

// GetDiff returns the diff of uncommitted changes in the worktree.
// Output is truncated to 32 KB. Returns "" when no changes exist.
func (wm *WorktreeManager) GetDiff() (string, error) {
	wtRepo, err := wm.openWorktreeRepo()
	if err != nil {
		return "", fmt.Errorf("open worktree repo: %w", err)
	}

	wt, err := wtRepo.Worktree()
	if err != nil {
		return "", fmt.Errorf("get worktree: %w", err)
	}

	st, err := wt.Status()
	if err != nil {
		return "", fmt.Errorf("get status: %w", err)
	}

	if st.IsClean() {
		return "", nil
	}

	// Get HEAD commit tree.
	headRef, err := wtRepo.Head()
	if err != nil {
		return "", fmt.Errorf("get HEAD: %w", err)
	}

	headCommit, err := wtRepo.CommitObject(headRef.Hash())
	if err != nil {
		return "", fmt.Errorf("get HEAD commit: %w", err)
	}

	headTree, err := headCommit.Tree()
	if err != nil {
		return "", fmt.Errorf("get HEAD tree: %w", err)
	}

	// Stage all changes, commit temporarily, get patch, then mixed-reset.
	if err := wt.AddWithOptions(&git.AddOptions{All: true}); err != nil {
		return "", fmt.Errorf("stage changes: %w", err)
	}

	tmpHash, err := wt.Commit("__diff_tmp__", &git.CommitOptions{
		AllowEmptyCommits: false,
		Author:            defaultSignature(),
	})
	if err != nil {
		// If nothing to commit after staging (edge case), reset and return empty.
		if errors.Is(err, git.ErrEmptyCommit) {
			_ = wt.Reset(&git.ResetOptions{Commit: headRef.Hash(), Mode: git.MixedReset})
			return "", nil
		}
		return "", fmt.Errorf("tmp commit: %w", err)
	}

	tmpCommit, err := wtRepo.CommitObject(tmpHash)
	if err != nil {
		_ = wt.Reset(&git.ResetOptions{Commit: headRef.Hash(), Mode: git.MixedReset})
		return "", fmt.Errorf("get tmp commit: %w", err)
	}

	tmpTree, err := tmpCommit.Tree()
	if err != nil {
		_ = wt.Reset(&git.ResetOptions{Commit: headRef.Hash(), Mode: git.MixedReset})
		return "", fmt.Errorf("get tmp tree: %w", err)
	}

	patch, err := headTree.PatchContext(context.Background(), tmpTree)

	// Reset back to HEAD (undo the temporary commit, preserve working directory).
	_ = wt.Reset(&git.ResetOptions{Commit: headRef.Hash(), Mode: git.MixedReset})

	if err != nil {
		return "", fmt.Errorf("compute diff: %w", err)
	}

	out := patch.String()
	if len(out) > maxDiffBytes {
		out = out[:maxDiffBytes] + "\n... (truncated, showing first 32KB of diff)"
	}
	return out, nil
}

// GetDiffStat returns the --stat summary of uncommitted changes.
// Returns "" when no changes exist.
func (wm *WorktreeManager) GetDiffStat() (string, error) {
	wtRepo, err := wm.openWorktreeRepo()
	if err != nil {
		return "", fmt.Errorf("open worktree repo: %w", err)
	}

	wt, err := wtRepo.Worktree()
	if err != nil {
		return "", fmt.Errorf("get worktree: %w", err)
	}

	st, err := wt.Status()
	if err != nil {
		return "", fmt.Errorf("get status: %w", err)
	}

	if st.IsClean() {
		return "", nil
	}

	// Get HEAD commit tree.
	headRef, err := wtRepo.Head()
	if err != nil {
		return "", fmt.Errorf("get HEAD: %w", err)
	}

	headCommit, err := wtRepo.CommitObject(headRef.Hash())
	if err != nil {
		return "", fmt.Errorf("get HEAD commit: %w", err)
	}

	headTree, err := headCommit.Tree()
	if err != nil {
		return "", fmt.Errorf("get HEAD tree: %w", err)
	}

	// Stage all changes, commit temporarily, get stats, then mixed-reset.
	if err := wt.AddWithOptions(&git.AddOptions{All: true}); err != nil {
		return "", fmt.Errorf("stage changes: %w", err)
	}

	tmpHash, err := wt.Commit("__diff_tmp__", &git.CommitOptions{
		AllowEmptyCommits: false,
		Author:            defaultSignature(),
	})
	if err != nil {
		if errors.Is(err, git.ErrEmptyCommit) {
			_ = wt.Reset(&git.ResetOptions{Commit: headRef.Hash(), Mode: git.MixedReset})
			return "", nil
		}
		return "", fmt.Errorf("tmp commit: %w", err)
	}

	tmpCommit, err := wtRepo.CommitObject(tmpHash)
	if err != nil {
		_ = wt.Reset(&git.ResetOptions{Commit: headRef.Hash(), Mode: git.MixedReset})
		return "", fmt.Errorf("get tmp commit: %w", err)
	}

	tmpTree, err := tmpCommit.Tree()
	if err != nil {
		_ = wt.Reset(&git.ResetOptions{Commit: headRef.Hash(), Mode: git.MixedReset})
		return "", fmt.Errorf("get tmp tree: %w", err)
	}

	patch, err := headTree.PatchContext(context.Background(), tmpTree)

	// Reset back to HEAD (undo the temporary commit, preserve working directory).
	_ = wt.Reset(&git.ResetOptions{Commit: headRef.Hash(), Mode: git.MixedReset})

	if err != nil {
		return "", fmt.Errorf("compute diff: %w", err)
	}

	return patch.Stats().String(), nil
}

// Reset discards all uncommitted changes in the worktree.
func (wm *WorktreeManager) Reset() error {
	wtRepo, err := wm.openWorktreeRepo()
	if err != nil {
		return fmt.Errorf("open worktree repo: %w", err)
	}

	wt, err := wtRepo.Worktree()
	if err != nil {
		return fmt.Errorf("get worktree: %w", err)
	}

	headRef, err := wtRepo.Head()
	if err != nil {
		return fmt.Errorf("get HEAD: %w", err)
	}

	if err := wt.Reset(&git.ResetOptions{Commit: headRef.Hash(), Mode: git.HardReset}); err != nil {
		return fmt.Errorf("reset failed: %w", err)
	}

	if err := wt.Clean(&git.CleanOptions{Dir: true}); err != nil {
		return fmt.Errorf("clean failed: %w", err)
	}

	return nil
}

// Merge stages and commits worktree changes, then fast-forwards the root branch.
// Returns nil if there are no changes to commit.
func (wm *WorktreeManager) Merge(commitMsg string) error {
	wtRepo, err := wm.openWorktreeRepo()
	if err != nil {
		return fmt.Errorf("open worktree repo: %w", err)
	}

	wt, err := wtRepo.Worktree()
	if err != nil {
		return fmt.Errorf("get worktree: %w", err)
	}

	// Check worktree status — if nothing changed, return nil.
	st, err := wt.Status()
	if err != nil {
		return fmt.Errorf("get status: %w", err)
	}
	if st.IsClean() {
		return nil
	}

	// Stage all changes.
	if err := wt.AddWithOptions(&git.AddOptions{All: true}); err != nil {
		return fmt.Errorf("git add failed: %w", err)
	}

	// Commit in the worktree.
	commitHash, err := wt.Commit(commitMsg, &git.CommitOptions{
		Author: defaultSignature(),
	})
	if err != nil {
		if errors.Is(err, git.ErrEmptyCommit) {
			return nil
		}
		return fmt.Errorf("git commit failed: %w", err)
	}

	// Fast-forward the root repo's main branch to the worktree's new commit.
	if wm.repo == nil {
		return errors.New("root repo not initialised")
	}

	// Determine the main branch ref name (typically refs/heads/master or refs/heads/main).
	rootHead, err := wm.repo.Head()
	if err != nil {
		return fmt.Errorf("get root HEAD: %w", err)
	}
	mainBranchRef := rootHead.Name()

	// Ensure root branch hasn't moved since Init — enforce fast-forward semantics.
	if rootHead.Hash().String() != wm.baseCommit {
		return fmt.Errorf("cannot fast-forward: root branch has advanced from %s to %s since worktree was created",
			wm.baseCommit, rootHead.Hash())
	}

	// Now safe to fast-forward the root repo's branch ref to the worktree commit hash.
	err = wm.repo.Storer.SetReference(plumbing.NewHashReference(mainBranchRef, commitHash))
	if err != nil {
		return fmt.Errorf("update root branch ref: %w", err)
	}

	// Reset the root worktree to match the new HEAD.
	rootWt, err := wm.repo.Worktree()
	if err != nil {
		return fmt.Errorf("get root worktree: %w", err)
	}

	if err := rootWt.Reset(&git.ResetOptions{Commit: commitHash, Mode: git.HardReset}); err != nil {
		return fmt.Errorf("reset root worktree: %w", err)
	}

	return nil
}

// Cleanup removes the worktree and its branch. Safe to call multiple times.
// Errors are ignored (best-effort cleanup).
func (wm *WorktreeManager) Cleanup() error {
	// Remove the linked worktree metadata from the root repo.
	if wm.repo != nil {
		dotgitFs := osfs.New(filepath.Join(wm.rootDir, ".git"), osfs.WithBoundOS())
		stor := filesystem.NewStorageWithOptions(dotgitFs, nil, filesystem.Options{})

		xwt, err := xworktree.New(stor)
		if err == nil {
			_ = xwt.Remove(wm.worktreeName)
		}

		// Delete the worktree branch reference.
		branchRef := plumbing.NewBranchReferenceName(wm.worktreeName)
		_ = wm.repo.Storer.RemoveReference(branchRef)
	}

	// Best-effort removal of the worktree directory.
	_ = os.RemoveAll(wm.worktreeDir)

	return nil
}

func (wm *WorktreeManager) logInfo(msg string, args ...any) {
	if wm.logger != nil {
		wm.logger.Info(msg, args...)
	}
}
