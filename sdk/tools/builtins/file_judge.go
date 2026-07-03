package builtins

import (
	"context"
	"encoding/json"
	"github.com/v0lka/c0wrk/sdk/pathutil"
	"github.com/v0lka/c0wrk/sdk/tools"
	"path/filepath"
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

	ok, err := pathutil.IsWithinPath(workspaceAbs, absPathClean)
	if err != nil || !ok {
		return false, ""
	}

	return true, "target is within session workspace"
}

// judgeReadInWorkspace checks whether a read operation targets a path inside
// the session workspace or temp directory. For PolicyAlwaysAllow read tools:
//   - Returns (true, reason) to auto-execute without confirmation.
//   - Returns (false, reason) to escalate to user confirmation when outside workspace.
func judgeReadInWorkspace(ctx context.Context, input json.RawMessage) (allowed bool, reason string) {
	var params struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(input, &params); err != nil || params.Path == "" {
		// Cannot determine path; allow execution — Execute() will validate.
		return true, "read-only file operation"
	}

	resolved := resolvePath(ctx, params.Path)
	if resolved == "" {
		return false, "path is outside the session workspace"
	}
	if err := validatePathInWorkspace(ctx, resolved); err != nil {
		return false, err.Error()
	}
	absPath, err := filepath.Abs(resolved)
	if err != nil {
		return true, "read-only file operation"
	}

	absPath = filepath.Clean(absPath)
	if evaled, evalErr := filepath.EvalSymlinks(absPath); evalErr == nil {
		absPath = evaled
	} else {
		parentDir := filepath.Dir(absPath)
		if resolvedParent, parentErr := filepath.EvalSymlinks(parentDir); parentErr == nil {
			absPath = filepath.Join(resolvedParent, filepath.Base(absPath))
		}
	}

	// Check workspace
	if ws := tools.WorkspacePathFrom(ctx); ws != "" {
		wsAbs := filepath.Clean(ws)
		if evaled, evalErr := filepath.EvalSymlinks(wsAbs); evalErr == nil {
			wsAbs = evaled
		}
		ok, err := pathutil.IsWithinPath(wsAbs, absPath)
		if err == nil && ok {
			return true, "read-only file operation within workspace"
		}
	}

	// Check temp directory
	if tmp := tools.TempDirFrom(ctx); tmp != "" {
		tmpAbs := filepath.Clean(tmp)
		if evaled, evalErr := filepath.EvalSymlinks(tmpAbs); evalErr == nil {
			tmpAbs = evaled
		}
		ok, err := pathutil.IsWithinPath(tmpAbs, absPath)
		if err == nil && ok {
			return true, "read-only file operation within temp directory"
		}
	}

	return false, "reading outside workspace: " + absPath
}
