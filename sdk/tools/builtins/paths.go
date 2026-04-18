package builtins

import (
	"context"
	"path/filepath"

	"github.com/user/agent/sdk/tools"
)

// resolvePath resolves a file path against the session workspace.
// Absolute paths are returned unchanged. Relative paths are joined with the
// workspace root obtained from the context. If no workspace is available,
// the path is returned as-is.
func resolvePath(ctx context.Context, path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	if ws := tools.WorkspacePathFrom(ctx); ws != "" {
		return filepath.Join(ws, path)
	}
	return path
}
