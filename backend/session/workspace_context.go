package session

import (
	"errors"
	"path/filepath"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/backend/project"
)

// ResolveSessionContexts constructs boundary DTOs without Git operations or
// runtime mutation. A nil Git target follows execution; an explicit target can
// focus the local checkout or another managed tree of the same project.
func ResolveSessionContexts(info SessionInfo, repositoryPath, chatWorkspace string, target *GitPanelTarget) (*SessionContexts, error) {
	binding, err := NormalizeWorkspaceBinding(info.ProjectID, repositoryPath, info.WorkspaceBinding)
	if err != nil {
		return nil, err
	}
	contexts := &SessionContexts{Project: ProjectContext{ProjectID: info.ProjectID, RepositoryPath: repositoryPath}}
	if info.ProjectID == project.NoProjectID {
		if target != nil {
			return nil, errors.New("CHAT has no Git panel target")
		}
		contexts.ExecutionWorkspace = chatWorkspace
		return contexts, nil
	}
	contexts.ExecutionWorkspace = binding.WorkspacePath
	contexts.GitPanel = GitPanelTarget{ProjectID: info.ProjectID, WorkspacePath: binding.WorkspacePath}
	if target == nil {
		return contexts, nil
	}
	if target.ProjectID != info.ProjectID {
		return nil, errors.New("git panel target belongs to another project")
	}
	if target.WorkspacePath != repositoryPath {
		derived, err := config.ManagedWorktreePath(repositoryPath, filepath.Base(target.WorkspacePath))
		if err != nil {
			return nil, err
		}
		if derived != target.WorkspacePath {
			return nil, errors.New("git panel target is not a project checkout or managed tree")
		}
	}
	contexts.GitPanel = *target
	return contexts, nil
}
