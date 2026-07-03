package builtins

import (
	"context"
	"errors"
	"fmt"
	"github.com/v0lka/c0wrk/sdk/pathutil"
	"github.com/v0lka/c0wrk/sdk/tools"
	"os"
	"path/filepath"
)

// resolvePath resolves a file path against the session workspace.
// Relative paths are joined with the workspace root obtained from the context.
// Absolute paths are validated to be within the workspace when a workspace is
// available; paths outside the workspace are rejected (returns an empty string
// so the caller can produce a clear error).
// If no workspace is available, the path is returned as-is (callers should
// validate on their own).
func resolvePath(ctx context.Context, path string) string {
	ws := tools.WorkspacePathFrom(ctx)
	if ws == "" {
		return path
	}

	// Resolve symlinks on the workspace root (fall back to unresolved path
	// if the directory doesn't exist yet — common for No Project sessions).
	realWS, err := resolveWorkspaceRoot(ws)
	if err != nil {
		return "" // unresolvable workspace — reject
	}

	if filepath.IsAbs(path) {
		// For the target path, resolve the longest existing prefix
		// (the file may not exist yet).
		realPath := pathutil.ResolveExistingPrefix(path)
		ok, err := pathutil.IsWithinPath(realWS, realPath)
		if err != nil || !ok {
			return "" // caller will produce "path is outside workspace" error
		}
		// Return the symlink-resolved path to prevent TOCTOU race between
		// containment check and the actual file operation.
		return realPath
	}

	// Relative path: join with workspace then resolve to absolute for
	// containment validation. filepath.Join resolves ".." components,
	// so the result may escape the workspace — validatePathInWorkspace
	// performs the final containment check.
	joined := filepath.Join(ws, path)
	absJoined, absErr := filepath.Abs(joined)
	if absErr != nil {
		return ""
	}
	// Resolve symlinks on the longest existing prefix.
	resolved := pathutil.ResolveExistingPrefix(absJoined)
	// Verify containment.
	ok, err := pathutil.IsWithinPath(realWS, resolved)
	if err != nil || !ok {
		return ""
	}
	return resolved
}

// resolveWorkspaceRoot resolves symlinks on the workspace root path.
// Falls back to the unresolved clean path when the directory doesn't
// exist yet (e.g., brand-new No Project session workspace).
func resolveWorkspaceRoot(ws string) (string, error) {
	resolved, err := filepath.EvalSymlinks(filepath.Clean(ws))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return filepath.Clean(ws), nil
		}
		return "", err
	}
	return resolved, nil
}

// validatePathInWorkspace checks whether the given path (already resolved by
// resolvePath) lies within the session workspace. The path may be absolute
// (from resolvePath's containment path or filepath.Abs resolution) or may
// represent a path that resolvePath rejected (empty string). Always performs
// a containment check — never delegates it — so that relative paths containing
// ".." are caught regardless of how they arrive.
func validatePathInWorkspace(ctx context.Context, resolved string) error {
	if resolved == "" {
		return errors.New("path is outside the session workspace")
	}
	ws := tools.WorkspacePathFrom(ctx)
	if ws == "" {
		return nil // no workspace configured; caller validates on its own
	}

	// Absolute path needed for containment check.
	absPath, err := filepath.Abs(resolved)
	if err != nil {
		return fmt.Errorf("cannot resolve path: %w", err)
	}

	// Resolve workspace root (with fallback for non-existent directories).
	wsAbs, err := resolveWorkspaceRoot(ws)
	if err != nil {
		return fmt.Errorf("cannot resolve workspace: %w", err)
	}

	resolvedAbs := pathutil.ResolveExistingPrefix(absPath)
	ok, err := pathutil.IsWithinPath(wsAbs, resolvedAbs)
	if err != nil || !ok {
		return fmt.Errorf("path is outside the session workspace: %s", resolved)
	}
	return nil
}
