package backend

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/v0lka/c0wrk/backend/review"
	"github.com/v0lka/c0wrk/core/workspace"
)

// GetReview loads the persisted review buffer (status + general comment + hunk
// comments) for a session. A session with no persisted review returns an empty,
// active review rather than an error.
func (f *FrontendAPI) GetReview(sessionID string) (*review.Review, error) {
	f.seedAcquire()
	if f.reviewStore == nil {
		return nil, errors.New("review store not initialized")
	}
	ctx, cancel := f.storeOpCtx()
	defer cancel()
	return f.reviewStore.GetReview(ctx, sessionID)
}

// SaveReviewGeneralComment upserts the session-wide general review comment.
// An empty body clears the general comment.
func (f *FrontendAPI) SaveReviewGeneralComment(sessionID, body string) error {
	f.seedAcquire()
	if f.reviewStore == nil {
		return errors.New("review store not initialized")
	}
	ctx, cancel := f.storeOpCtx()
	defer cancel()
	if err := f.reviewStore.UpsertGeneralComment(ctx, sessionID, body); err != nil {
		return fmt.Errorf("failed to save general review comment: %w", err)
	}
	return nil
}

// SaveReviewHunkComment upserts a hunk-scoped review comment for the given
// (filePath, hunkID) pair and returns the resulting comment id. An empty body
// removes the comment (returning an empty id).
func (f *FrontendAPI) SaveReviewHunkComment(sessionID, filePath, hunkID, body string) (string, error) {
	f.seedAcquire()
	if f.reviewStore == nil {
		return "", errors.New("review store not initialized")
	}
	ctx, cancel := f.storeOpCtx()
	defer cancel()
	id, err := f.reviewStore.UpsertHunkComment(ctx, sessionID, filePath, hunkID, body)
	if err != nil {
		return "", fmt.Errorf("failed to save hunk review comment: %w", err)
	}
	return id, nil
}

// SaveReviewFileComment upserts a file-scoped review comment for the given
// filePath and returns the resulting comment id. An empty body removes the
// comment (returning an empty id).
func (f *FrontendAPI) SaveReviewFileComment(sessionID, filePath, body string) (string, error) {
	f.seedAcquire()
	if f.reviewStore == nil {
		return "", errors.New("review store not initialized")
	}
	ctx, cancel := f.storeOpCtx()
	defer cancel()
	id, err := f.reviewStore.UpsertFileComment(ctx, sessionID, filePath, body)
	if err != nil {
		return "", fmt.Errorf("failed to save file review comment: %w", err)
	}
	return id, nil
}

func (f *FrontendAPI) DeleteReviewComment(id string) error {
	f.seedAcquire()
	if f.reviewStore == nil {
		return errors.New("review store not initialized")
	}
	ctx, cancel := f.storeOpCtx()
	defer cancel()
	if err := f.reviewStore.DeleteComment(ctx, id); err != nil {
		return fmt.Errorf("failed to delete review comment: %w", err)
	}
	return nil
}

// SetReviewStatus upserts the review status for a session. status must be one
// of "active", "submitted", or "approved".
func (f *FrontendAPI) SetReviewStatus(sessionID, status string) error {
	f.seedAcquire()
	if f.reviewStore == nil {
		return errors.New("review store not initialized")
	}
	ctx, cancel := f.storeOpCtx()
	defer cancel()
	if err := f.reviewStore.SetReviewStatus(ctx, sessionID, review.ReviewStatus(status)); err != nil {
		return fmt.Errorf("failed to set review status: %w", err)
	}
	return nil
}

// ClearReviewComments removes all review comments (general + hunk) for a
// session while preserving the review status.
func (f *FrontendAPI) ClearReviewComments(sessionID string) error {
	f.seedAcquire()
	if f.reviewStore == nil {
		return errors.New("review store not initialized")
	}
	ctx, cancel := f.storeOpCtx()
	defer cancel()
	if err := f.reviewStore.ClearComments(ctx, sessionID); err != nil {
		return fmt.Errorf("failed to clear review comments: %w", err)
	}
	return nil
}

// ClearReview resets the whole review for a session (all comments + status).
func (f *FrontendAPI) ClearReview(sessionID string) error {
	f.seedAcquire()
	if f.reviewStore == nil {
		return errors.New("review store not initialized")
	}
	ctx, cancel := f.storeOpCtx()
	defer cancel()
	if err := f.reviewStore.ClearReview(ctx, sessionID); err != nil {
		return fmt.Errorf("failed to clear review: %w", err)
	}
	return nil
}

// GetReviewDiff returns ALL uncommitted changes (staged + unstaged tracked
// changes plus untracked files) for the active project's repository, grouped
// per file with 5 context lines per hunk. Read-only (no events); an empty
// slice — never an error — for no project, non-git, or a clean tree.
// See specs/domains/review.md.
func (f *FrontendAPI) GetReviewDiff() ([]ReviewFileDiff, error) {
	f.activeProjectMu.RLock()
	projectPath := f.activeProjectPath
	f.activeProjectMu.RUnlock()

	if projectPath == "" || f.isNoProject() || !f.isGitRepo(projectPath) {
		return []ReviewFileDiff{}, nil
	}

	ctx, cancel := context.WithTimeout(f.ctx(), gitCmdTimeout)
	defer cancel()
	out, err := workspace.BuildReviewDiff(ctx, projectPath, 5)
	if err != nil {
		return nil, err
	}

	return workspace.ParseReviewDiff(out), nil
}

// GetCommitDiff returns the per-file hunk diff a single commit introduces
// (patch vs first parent) in the GetReviewDiff shape, so the review page can
// render a commit read-only. Root commits diff against an empty tree (--root);
// merge commits yield an empty slice (diff-tree emits no merge patch),
// matching GetCommitFiles. Read-only; errors for no project, No Project, an
// empty/invalid sha, or a git failure.
func (f *FrontendAPI) GetCommitDiff(sha string) ([]ReviewFileDiff, error) {
	commit := strings.TrimSpace(sha)
	if commit == "" {
		return nil, errors.New("commit sha must not be empty")
	}
	if err := validateCommitSha(commit); err != nil {
		return nil, err
	}

	repoPath, err := f.resolveGitRepoRoot()
	if err != nil {
		return nil, err
	}

	out, err := f.runGitCmd(repoPath, "diff-tree", "--no-commit-id", "-p", "--root", "-U5", "-M", commit)
	if err != nil {
		return nil, err
	}

	return workspace.ParseReviewDiff(out), nil
}
