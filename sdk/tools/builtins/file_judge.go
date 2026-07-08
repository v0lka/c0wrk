package builtins

import (
	"context"
	"encoding/json"
	"path/filepath"
)

// judgeWriteInSessionRoots checks whether a write operation targets a path
// inside the session workspace or the session temp directory. Both roots are
// treated as equal peers: writes inside either are auto-approved (when
// auto_approve_workspace_writes is enabled); writes outside both escalate to
// user confirmation. Returns (true, reason) if allowed, or (false, reason) to
// defer to the confirmation flow with an explanatory reason.
func judgeWriteInSessionRoots(ctx context.Context, path string) (allowed bool, reason string) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return false, ""
	}
	absPath = filepath.Clean(absPath)
	if resolved, evalErr := filepath.EvalSymlinks(absPath); evalErr == nil {
		absPath = resolved
	} else {
		parentDir := filepath.Dir(absPath)
		if resolvedParent, parentErr := filepath.EvalSymlinks(parentDir); parentErr == nil {
			absPath = filepath.Join(resolvedParent, filepath.Base(absPath))
		}
	}

	if isPathInSessionRoots(ctx, absPath) {
		return true, "target is within session workspace or temp directory"
	}
	return false, formatOutsideRootsError(absPath).Error()
}

// judgeReadInSessionRoots checks whether a read operation targets a path inside
// the session workspace or the session temp directory. Both roots are treated
// as equal peers: reads inside either auto-execute without confirmation;
// reads outside escalate to user confirmation.
//
// Returns (true, reason) to auto-execute, or (false, reason) to escalate to
// user confirmation.
func judgeReadInSessionRoots(ctx context.Context, input json.RawMessage) (allowed bool, reason string) {
	var params struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(input, &params); err != nil || params.Path == "" {
		// Cannot determine path — fail closed and escalate to confirmation.
		return false, "cannot determine target path"
	}

	resolved := resolvePath(ctx, params.Path)
	if err := validateResolvedPath(resolved); err != nil {
		return false, err.Error()
	}

	absPath, err := filepath.Abs(resolved)
	if err != nil {
		// Cannot determine path — fail closed and escalate to confirmation.
		return false, "cannot determine target path"
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

	if isPathInSessionRoots(ctx, absPath) {
		return true, "read-only file operation within session workspace or temp directory"
	}

	return false, formatOutsideRootsError(absPath).Error()
}
