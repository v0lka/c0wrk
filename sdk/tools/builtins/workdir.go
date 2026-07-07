package builtins

import (
	"fmt"
	"os"

	"github.com/v0lka/sp4rk/pathutil"
)

// validateWorkDir checks that dir is within the workspace root or a temp
// directory. If workspaceRoot is empty, any directory is accepted (no
// workspace context means no containment is possible). ctxTempDir is the
// per-request temp dir from context (may be empty).
//
// Containment is evaluated through pathutil.IsWithinPath (symlink-resolved)
// rather than inline prefix checks, per the project's centralized path API.
//
// Shared by all shell-execution tools (bash_exec on Unix, posh_exec on
// Windows) so it must build on every platform.
func validateWorkDir(dir, workspaceRoot, ctxTempDir string) error {
	if workspaceRoot == "" {
		return nil // no workspace context — cannot enforce containment
	}

	// Allow workspace root or subdirectory.
	if ok, _ := pathutil.IsWithinPath(workspaceRoot, dir); ok {
		return nil
	}

	// Allow context-provided temp directory.
	if ctxTempDir != "" {
		if ok, _ := pathutil.IsWithinPath(ctxTempDir, dir); ok {
			return nil
		}
	}

	// Allow system temp directory.
	if ok, _ := pathutil.IsWithinPath(os.TempDir(), dir); ok {
		return nil
	}

	return fmt.Errorf("path %q is outside workspace (%s) and temp directory", dir, workspaceRoot)
}
