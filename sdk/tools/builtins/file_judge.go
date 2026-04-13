package builtins

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/user/agent/sdk/tools"
)

// judgeWriteInWorkspace checks whether a write operation targets a path
// inside the session workspace. Returns (true, reason) if allowed,
// or (false, "") to defer to the LLM Judge.
func judgeWriteInWorkspace(ctx context.Context, path string) (allowed bool, reason string) {
	workspacePath := tools.WorkspacePathFrom(ctx)
	if workspacePath == "" {
		return false, ""
	}

	absPath, err := filepath.Abs(path)
	if err != nil {
		return false, ""
	}

	workspaceAbs := filepath.Clean(workspacePath)
	workspaceAbs, err = filepath.EvalSymlinks(workspaceAbs)
	if err != nil {
		return false, ""
	}

	absPathClean := filepath.Clean(absPath)
	if resolved, evalErr := filepath.EvalSymlinks(absPathClean); evalErr == nil {
		absPathClean = resolved
	} else {
		parentDir := filepath.Dir(absPathClean)
		resolvedParent, parentErr := filepath.EvalSymlinks(parentDir)
		if parentErr != nil {
			return false, ""
		}
		absPathClean = filepath.Join(resolvedParent, filepath.Base(absPathClean))
	}

	if !strings.HasSuffix(workspaceAbs, string(filepath.Separator)) {
		workspaceAbs += string(filepath.Separator)
	}

	workspaceClean := strings.TrimSuffix(workspaceAbs, string(filepath.Separator))
	if !strings.HasPrefix(absPathClean+string(filepath.Separator), workspaceAbs) && absPathClean != workspaceClean {
		return false, ""
	}

	return true, "target is within session workspace"
}
