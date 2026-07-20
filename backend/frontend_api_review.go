package backend

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/v0lka/c0wrk/backend/review"
	"github.com/v0lka/c0wrk/backend/session"
	"github.com/v0lka/c0wrk/core/workspace"
)

// reviewPromptContent is the persisted body of the review_prompt chat message.
// It is the single source of truth for the wording: the frontend derives its
// copy from the Content returned by SaveReviewPrompt rather than duplicating
// the string.
const reviewPromptContent = "Uncommitted changes detected in this repository."

// GetReview loads the persisted review buffer (status + general comment + hunk
// comments) for a session. A session with no persisted review returns an empty,
// active review rather than an error.
func (f *FrontendAPI) GetReview(sessionID string) (*review.Review, error) {
	if f.reviewStore == nil {
		return nil, errors.New("review store not initialized")
	}
	return f.reviewStore.GetReview(context.Background(), sessionID)
}

// SaveReviewGeneralComment upserts the session-wide general review comment.
// An empty body clears the general comment.
func (f *FrontendAPI) SaveReviewGeneralComment(sessionID, body string) error {
	if f.reviewStore == nil {
		return errors.New("review store not initialized")
	}
	if err := f.reviewStore.UpsertGeneralComment(context.Background(), sessionID, body); err != nil {
		return fmt.Errorf("failed to save general review comment: %w", err)
	}
	return nil
}

// SaveReviewHunkComment upserts a hunk-scoped review comment for the given
// (filePath, hunkID) pair and returns the resulting comment id. An empty body
// removes the comment (returning an empty id).
func (f *FrontendAPI) SaveReviewHunkComment(sessionID, filePath, hunkID, body string) (string, error) {
	if f.reviewStore == nil {
		return "", errors.New("review store not initialized")
	}
	id, err := f.reviewStore.UpsertHunkComment(context.Background(), sessionID, filePath, hunkID, body)
	if err != nil {
		return "", fmt.Errorf("failed to save hunk review comment: %w", err)
	}
	return id, nil
}

// DeleteReviewComment removes a single review comment by id.
func (f *FrontendAPI) DeleteReviewComment(id string) error {
	if f.reviewStore == nil {
		return errors.New("review store not initialized")
	}
	if err := f.reviewStore.DeleteComment(context.Background(), id); err != nil {
		return fmt.Errorf("failed to delete review comment: %w", err)
	}
	return nil
}

// SetReviewStatus upserts the review status for a session. status must be one
// of "active", "submitted", or "approved".
func (f *FrontendAPI) SetReviewStatus(sessionID, status string) error {
	if f.reviewStore == nil {
		return errors.New("review store not initialized")
	}
	if err := f.reviewStore.SetReviewStatus(context.Background(), sessionID, review.ReviewStatus(status)); err != nil {
		return fmt.Errorf("failed to set review status: %w", err)
	}
	return nil
}

// ClearReviewComments removes all review comments (general + hunk) for a
// session while preserving the review status.
func (f *FrontendAPI) ClearReviewComments(sessionID string) error {
	if f.reviewStore == nil {
		return errors.New("review store not initialized")
	}
	if err := f.reviewStore.ClearComments(context.Background(), sessionID); err != nil {
		return fmt.Errorf("failed to clear review comments: %w", err)
	}
	return nil
}

// ClearReview resets the whole review for a session (all comments + status).
func (f *FrontendAPI) ClearReview(sessionID string) error {
	if f.reviewStore == nil {
		return errors.New("review store not initialized")
	}
	if err := f.reviewStore.ClearReview(context.Background(), sessionID); err != nil {
		return fmt.Errorf("failed to clear review: %w", err)
	}
	return nil
}

// SaveReviewPrompt persists a "review_prompt" chat message for a session and
// returns its descriptor (the generated prompt_id plus the message content).
//
// The review_prompt is injected client-side after a successful task_complete
// when the repository has uncommitted changes. Persisting it here (rather than
// keeping it as a frontend-only message) means it survives a session switch
// and an app restart: getSessionHistory returns it like any other message, and
// its resolution (enter/decline) is later recorded via ResolvePendingMessage
// keyed on the returned prompt_id, so the resolved state round-trips too.
//
// The message content is the single source of truth (see reviewPromptContent):
// the frontend renders the live card from the Content returned here rather
// than duplicating the string, so the wording stays consistent across the
// in-memory and persisted forms.
//
// Best-effort persistence on the store is not appropriate here: the frontend
// only renders the prompt after this RPC succeeds, so a persist failure is
// surfaced as an error instead of producing a prompt that would silently
// vanish on the next session switch.
func (f *FrontendAPI) SaveReviewPrompt(sessionID string) (*ReviewPromptMessage, error) {
	if f.store == nil {
		return nil, errors.New("session store not available")
	}
	promptID := uuid.NewString()
	metadata, err := json.Marshal(map[string]any{"prompt_id": promptID})
	if err != nil {
		return nil, fmt.Errorf("failed to marshal review prompt metadata: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := f.store.SaveMessage(ctx, session.ChatMessage{
		SessionID: sessionID,
		Role:      "review_prompt",
		Content:   reviewPromptContent,
		Metadata:  metadata,
		CreatedAt: time.Now().Format(time.RFC3339),
	}); err != nil {
		f.log().Error("failed to save review_prompt message", "session", sessionID, "error", err)
		return nil, fmt.Errorf("failed to save review prompt: %w", err)
	}
	return &ReviewPromptMessage{PromptID: promptID, Content: reviewPromptContent}, nil
}

// GetReviewDiff returns ALL uncommitted changes (staged and unstaged tracked
// changes plus untracked files) for the active project's repository, grouped
// per file with 5 lines of context per hunk. It builds the combined diff via
// workspace.BuildReviewDiff and parses the result into per-file hunk
// snapshots for the review page.
//
// This is a read-only RPC: it emits no events. It returns an empty slice
// (never an error) when no project is active, the project is No Project,
// the workspace is not a git repository, or the working tree is clean.
func (f *FrontendAPI) GetReviewDiff() ([]ReviewFileDiff, error) {
	f.activeProjectMu.RLock()
	projectPath := f.activeProjectPath
	f.activeProjectMu.RUnlock()

	// Read-only and non-fatal: no active project, No Project mode, or a
	// non-git workspace all yield an empty result rather than an error.
	if projectPath == "" || f.isNoProject() || !f.isGitRepo(projectPath) {
		return []ReviewFileDiff{}, nil
	}

	// BuildReviewDiff combines `git diff -U5 HEAD` (staged + unstaged
	// tracked changes) with a per-file `git diff --no-index` for every
	// untracked file. `git diff HEAD` alone omits untracked files because
	// they have no index entry, so they are emitted separately here.
	ctx, cancel := context.WithTimeout(f.ctx(), gitCmdTimeout)
	defer cancel()
	out, err := workspace.BuildReviewDiff(ctx, projectPath, 5)
	if err != nil {
		return nil, err
	}

	return workspace.ParseReviewDiff(out), nil
}

// GetCommitDiff returns the per-file hunk diff introduced by a single commit
// (the patch relative to its first parent), grouped per file with 5 lines of
// context per hunk. It runs `git diff-tree --no-commit-id -p --root -U5 -M
// <sha>` and parses the result into the same per-file hunk snapshot shape as
// GetReviewDiff, so the review page can render a commit's changes in
// read-only mode.
//
// Root commits (no parent) are diffed against an empty tree (--root) so every
// file appears as added. Merge commits yield an empty slice (git diff-tree
// does not emit a patch for merges by default), matching GetCommitFiles.
//
// This is a read-only RPC: it emits no events. Returns an error when no
// project is active, the project is No Project, the sha is empty/invalid, or
// the git command fails.
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
