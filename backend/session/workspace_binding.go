package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/google/uuid"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/backend/project"
)

// The fresh window (no transcript, no task rows) has closed — the
// workspace can no longer be chosen; the send-time binding flow surfaces
// this as an actionable message instead of a raw store failure.
var ErrSessionNotFresh = errors.New("session is no longer fresh — its workspace can only be chosen before the first message")

// The Git owner's lazy-restore seam: guarantee the managed workspace on its
// pinned branch before the orchestrator is built. recreated=true = tree
// recreated (data-loss warning); error = restore aborts, never falls back
// to the checkout. See specs/domains/session-worktrees.md (§ Restore).
type WorkspaceEnsurer func(ctx context.Context, repoRoot string, binding *WorkspaceBinding) (recreated bool, err error)

// Reserves identity without orchestrator, event, or row — the manager's
// INTERNAL identity seam; the managed fork provisions before the row
// exists (binding at FIRST send, ADR-091). See specs/domains/session-lifecycle.md.
type SessionDraft struct {
	ID               string            `json:"id"`
	ProjectID        string            `json:"project_id"`
	WorkspaceBinding *WorkspaceBinding `json:"workspace_binding,omitempty"`
}

// NewSessionDraft allocates identity for pre-creation provisioning.
func NewSessionDraft(projectID string, binding *WorkspaceBinding) SessionDraft {
	return SessionDraft{ID: uuid.NewString(), ProjectID: projectID, WorkspaceBinding: cloneWorkspaceBinding(binding)}
}

func cloneWorkspaceBinding(binding *WorkspaceBinding) *WorkspaceBinding {
	if binding == nil {
		return nil
	}
	b := *binding
	return &b
}

// WorkspaceBinding returns a copy of the immutable runtime binding.
func (s *Session) WorkspaceBinding() *WorkspaceBinding {
	return cloneWorkspaceBinding(s.workspaceBinding)
}

// WorkspaceKind distinguishes the project checkout from a session-owned tree.
type WorkspaceKind string

const (
	WorkspaceLocal           WorkspaceKind = "local"            // the project checkout
	WorkspaceManagedWorktree WorkspaceKind = "managed_worktree" // session-owned git worktree
)

// Immutable after session creation; Branch is the selected identity, not a
// live HEAD reading; CHAT sessions have none (specs/domains/session-worktrees.md).
type WorkspaceBinding struct {
	Kind          WorkspaceKind `json:"kind"`
	WorkspacePath string        `json:"workspace_path"`
	WorktreeName  string        `json:"worktree_name,omitempty"`
	Branch        string        `json:"branch,omitempty"`
}

// ProjectContext identifies the owning project independently of execution.
type ProjectContext struct {
	ProjectID      string `json:"project_id"`
	RepositoryPath string `json:"repository_path"`
}

// GitPanelTarget is UI focus, not permission to retarget a running session.
type GitPanelTarget struct {
	ProjectID     string `json:"project_id"`
	WorkspacePath string `json:"workspace_path"`
}

// SessionContexts keeps project identity, execution scope and Git UI focus
// explicit. Consumers must not substitute GitPanel for ExecutionWorkspace.
type SessionContexts struct {
	Project            ProjectContext `json:"project"`
	ExecutionWorkspace string         `json:"execution_workspace"`
	GitPanel           GitPanelTarget `json:"git_panel"`
}

// Validates a persisted or proposed identity against the owning project: a
// nil legacy CODE binding becomes local, CHAT stays nil. Returns a copy.
// See specs/domains/session-worktrees.md (§ Restore).
func NormalizeWorkspaceBinding(projectID, repoRoot string, binding *WorkspaceBinding) (*WorkspaceBinding, error) {
	if projectID == "" {
		return nil, errors.New("session project identity is required")
	}
	if projectID == project.NoProjectID {
		if binding != nil {
			return nil, errors.New("CHAT sessions cannot have a CODE workspace binding")
		}
		return nil, nil
	}
	if !filepath.IsAbs(repoRoot) || filepath.Clean(repoRoot) != repoRoot {
		return nil, errors.New("project workspace must be an absolute clean path")
	}
	b := WorkspaceBinding{Kind: WorkspaceLocal, WorkspacePath: repoRoot}
	if binding != nil {
		b = *binding
	}
	switch b.Kind {
	case WorkspaceLocal:
		// The path is always the project's current root: a local session
		// executes wherever the checkout lives; a stored absolute path would
		// go stale on re-registration.
		if b.WorktreeName != "" || b.Branch != "" {
			return nil, errors.New("local binding must identify only the project checkout")
		}
		b.WorkspacePath = repoRoot
	case WorkspaceManagedWorktree:
		// A managed path is durable identity. An EMPTY path derives the
		// historical checkout-local location from the name alone (lexical
		// on purpose — a stray legacy-container object cannot invalidate a
		// tree living elsewhere); a RECORDED path must be this repo's
		// legacy/marked container or the project-scoped
		// "<base>/<projectID>/worktrees" layout.
		var err error
		if b.WorkspacePath != "" {
			var name string
			name, err = config.ManagedWorktreeNameFromPathProject(repoRoot, projectID, b.WorkspacePath)
			if err == nil && name != b.WorktreeName {
				err = errors.New("managed worktree path does not match its name")
			}
		} else {
			b.WorkspacePath, err = config.ManagedWorktreeLegacyPath(repoRoot, b.WorktreeName)
		}
		if err != nil {
			return nil, fmt.Errorf("invalid worktree identity: %w", err)
		}
		if err := validatePinnedBranch(b.Branch); err != nil {
			return nil, err
		}
	default:
		return nil, errors.New("unknown session workspace kind")
	}
	return &b, nil
}

func validatePinnedBranch(branch string) error {
	if branch == "" || branch == "HEAD" || strings.HasPrefix(branch, "-") || strings.HasPrefix(branch, "/") || strings.HasSuffix(branch, "/") || strings.HasSuffix(branch, ".") || strings.Contains(branch, "..") || strings.Contains(branch, "@{") || strings.Contains(branch, "//") || branch == "@" {
		return errors.New("invalid pinned branch")
	}
	for _, r := range branch {
		if unicode.IsControl(r) || unicode.IsSpace(r) || strings.ContainsRune("~^:?*[\\", r) {
			return errors.New("invalid pinned branch")
		}
	}
	for _, part := range strings.Split(branch, "/") {
		if strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".lock") {
			return errors.New("invalid pinned branch")
		}
	}
	return nil
}

func decodeWorkspaceBinding(raw, projectID, repoRoot string) (*WorkspaceBinding, error) {
	var binding *WorkspaceBinding
	if raw != "" {
		if err := json.Unmarshal([]byte(raw), &binding); err != nil {
			return nil, fmt.Errorf("decode session workspace binding: %w", err)
		}
	}
	return NormalizeWorkspaceBinding(projectID, repoRoot, binding)
}
