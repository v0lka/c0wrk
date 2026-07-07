package builtins

import (
	"context"
	"errors"
	"fmt"
	"github.com/v0lka/sp4rk/pathutil"
	"github.com/v0lka/sp4rk/tools"
	"os"
	"path/filepath"
)

// resolvePath resolves a file path against the session roots (workspace and
// temp directory, treated as equal peers).
//
// Relative paths are joined with the workspace root and MUST stay within it —
// escaping via ".." components is rejected (returns ""). Relative paths cannot
// target the temp directory; callers must use absolute paths for temp access.
//
// Absolute paths are symlink-resolved via pathutil.ResolveExistingPrefix and
// returned regardless of whether they fall inside or outside the session
// roots. Containment is NOT enforced here: operations outside the session
// roots are allowed after user confirmation (gated by the Judge layer and
// registry confirmation flow). Callers that need to know whether the resolved
// path is inside the session roots should use isPathInSessionRoots.
//
// If no workspace is available in the context, the path is returned as-is
// (callers validate on their own).
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
		// Resolve symlinks on the longest existing prefix (the file may not
		// exist yet). Return the resolved path regardless of containment;
		// the Judge layer and registry confirmation flow handle access
		// control for paths outside the session roots.
		return pathutil.ResolveExistingPrefix(path)
	}

	// Relative path: join with workspace then resolve to absolute for
	// containment validation. filepath.Join resolves ".." components,
	// so the result may escape the workspace — reject if it does.
	joined := filepath.Join(ws, path)
	absJoined, absErr := filepath.Abs(joined)
	if absErr != nil {
		return ""
	}
	resolved := pathutil.ResolveExistingPrefix(absJoined)
	ok, err := pathutil.IsWithinPath(realWS, resolved)
	if err != nil || !ok {
		return ""
	}
	return resolved
}

// resolveWorkspaceRoot resolves symlinks on a session root path (workspace or
// temp directory). Falls back to the unresolved clean path when the directory
// doesn't exist yet (e.g., brand-new No Project session workspace).
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

// validateResolvedPath checks that the resolved path is non-empty. A non-empty
// result from resolvePath indicates a usable path; an empty result means the
// input was a relative path that escaped the workspace (rejected by resolvePath).
//
// Containment within the session roots is NOT enforced here — operations
// outside workspace/temp are allowed after user confirmation. Use
// isPathInSessionRoots when containment must be known.
func validateResolvedPath(resolved string) error {
	if resolved == "" {
		return errors.New("path is outside the session workspace")
	}
	return nil
}

// isPathInSessionRoots reports whether absPath is contained within the session
// workspace or the session temp directory. Both roots are treated as equal
// peers: any operation permitted inside the workspace is permitted inside the
// temp directory and vice versa. Symlinks are resolved through the longest
// existing prefix so that OS-level symlinks (e.g., macOS /tmp → /private/tmp)
// do not cause false negatives.
func isPathInSessionRoots(ctx context.Context, absPath string) bool {
	if ok := isPathInRoot(ctx, absPath, tools.WorkspacePathFrom); ok {
		return true
	}
	if ok := isPathInRoot(ctx, absPath, tools.TempDirFrom); ok {
		return true
	}
	return false
}

// isPathInRoot is a helper for isPathInSessionRoots that checks absPath against
// a single root extracted from ctx by rootFn. Returns false when the root is
// empty or unresolvable, or when absPath is not contained within it.
func isPathInRoot(ctx context.Context, absPath string, rootFn func(context.Context) string) bool {
	root := rootFn(ctx)
	if root == "" {
		return false
	}
	rootAbs, err := resolveWorkspaceRoot(root)
	if err != nil {
		return false
	}
	ok, _ := pathutil.IsWithinPath(rootAbs, absPath)
	return ok
}

// formatOutsideRootsError returns a descriptive error for a path that falls
// outside both session roots. Used by Judge helpers when escalating to user
// confirmation.
func formatOutsideRootsError(absPath string) error {
	return fmt.Errorf("path is outside the session workspace and temp directory: %s", absPath)
}
