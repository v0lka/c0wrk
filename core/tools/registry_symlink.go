package tools

import (
	"context"
	"encoding/json"
	"fmt"

	sdktools "github.com/v0lka/sp4rk/tools"
)

// checkSymlinksAndConfirm is the integration method called from ToolRegistry.Execute().
// It detects symlinks in the tool input and, if found, forces confirmation
// (respecting sdktools.PolicyAlwaysDeny). Returns intercepted=true if the call was handled.
func (r *ToolRegistry) checkSymlinksAndConfirm(ctx context.Context, tool sdktools.Tool, name string, input json.RawMessage) (intercepted bool, result sdktools.ToolResult, err error) {
	inside, outside, suspicious := sdktools.DetectSymlinksInToolInput(ctx, name, input)
	if len(inside) == 0 && len(outside) == 0 && !suspicious {
		return false, sdktools.ToolResult{}, nil
	}

	// If every detected symlink traversal is benign OS-level infrastructure
	// (a well-known OS symlink, or a symlink that is an ancestor of the
	// workspace/temp root), skip the confirmation gate. Classification is
	// delegated to sp4rk's IsOSLevelSymlink so the well-known list is never
	// duplicated here — os_symlinks.go is the single source of truth shared by
	// both the sp4rk symlink walker and this core gate.
	workspace := sdktools.WorkspacePathFrom(ctx)
	tempDir := sdktools.TempDirFrom(ctx)
	if allTraversalsOSLevel(inside, outside, workspace, tempDir) {
		return false, sdktools.ToolResult{}, nil
	}

	policy := r.resolvePolicy(name, tool)
	if policy == sdktools.PolicyAlwaysDeny {
		return true, sdktools.ToolResult{
			Content: fmt.Sprintf("tool %q blocked by security policy", name),
			IsError: true,
		}, nil
	}

	reasoning := sdktools.FormatSymlinkReasoning(inside, outside, suspicious)
	result, err = r.confirmAndExecute(ctx, tool, name, input, reasoning)
	return true, result, err
}

// allTraversalsOSLevel reports whether every given symlink traversal is benign
// OS-level infrastructure: a well-known OS symlink, or a symlink that is an
// ancestor of the workspace or temp root. Returns true for an empty set
// (nothing to confirm). roots is the set of legitimate session roots
// (workspace, temp dir) that may legitimately be reached through a symlink.
func allTraversalsOSLevel(inside, outside []sdktools.SymlinkTraversal, roots ...string) bool {
	for _, t := range inside {
		if !sdktools.IsOSLevelSymlink(t.SymlinkAt, roots...) {
			return false
		}
	}
	for _, t := range outside {
		if !sdktools.IsOSLevelSymlink(t.SymlinkAt, roots...) {
			return false
		}
	}
	return true
}
