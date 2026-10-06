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

// WorkspaceEnsurer guarantees the execution workspace named by a managed
// binding physically exists on its pinned branch before a restored session's
// orchestrator is built on top of it. It is the seam through which the
// backend's Git lifecycle owner (worktrees.Owner) participates in lazy
// restore: validate the stored Git identity against the repository, recreate
// a missing tree from the pinned branch, refuse a mismatched tree, and fail
// explicitly when the branch itself is gone. It returns recreated=true when
// the tree was absent (or only stale metadata remained) and had to be
// recreated — the caller surfaces the accompanying data-loss warning. An
// error aborts the restore; the caller must never fall back to the project
// checkout. repoRoot is the project's registered repository root; binding is
// always a normalized managed binding.
type WorkspaceEnsurer func(ctx context.Context, repoRoot string, binding *WorkspaceBinding) (recreated bool, err error)

// SessionDraft reserves identity without creating an orchestrator, emitting an
// event or writing a row. The lifecycle owner provisions a tree before commit.
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
	WorkspaceLocal           WorkspaceKind = "local"
	WorkspaceManagedWorktree WorkspaceKind = "managed_worktree"
)

// WorkspaceBinding is immutable after session creation. Branch is the selected
// branch identity, not a live reading of HEAD. CHAT sessions have no binding.
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

// NormalizeWorkspaceBinding validates a persisted or proposed identity against
// the owning project. A nil legacy CODE binding becomes local; CHAT stays nil.
// It returns a copy, so callers cannot mutate the supplied object by aliasing.
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
		// The checkout path is always the project's current root: a local
		// session executes wherever the project checkout lives, and a stored
		// absolute path would go stale the moment the project re-registers
		// with a moved checkout. Worktree/branch fields are meaningless here.
		if b.WorktreeName != "" || b.Branch != "" {
			return nil, errors.New("local binding must identify only the project checkout")
		}
		b.WorkspacePath = repoRoot
	case WorkspaceManagedWorktree:
		// The execution path is derived from the project root and the tree
		// name (containment enforced by ManagedWorktreePath), never trusted
		// from the stored copy: the tree is recreated at the derived location
		// on restore, so a stale or tampered stored path must not redirect it.
		expected, err := config.ManagedWorktreePath(repoRoot, b.WorktreeName)
		if err != nil {
			return nil, fmt.Errorf("invalid worktree identity: %w", err)
		}
		b.WorkspacePath = expected
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
