package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

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

	// If all symlink traversals are OS-level infrastructure, skip interception.
	if len(outside) == 0 {
		tempDir := sdktools.TempDirFrom(ctx)
		workspace := sdktools.WorkspacePathFrom(ctx)
		allOSLevel := true
		for _, t := range inside {
			symlinkPrefix := filepath.Clean(t.SymlinkAt) + string(filepath.Separator)
			osLevel := false
			if workspace != "" {
				wsPrefix := filepath.Clean(workspace) + string(filepath.Separator)
				if strings.HasPrefix(wsPrefix, symlinkPrefix) {
					osLevel = true
				}
			}
			if !osLevel && tempDir != "" {
				tempPrefix := filepath.Clean(tempDir) + string(filepath.Separator)
				if strings.HasPrefix(tempPrefix, symlinkPrefix) {
					osLevel = true
				}
			}
			if !osLevel {
				allOSLevel = false
				break
			}
		}
		if allOSLevel {
			return false, sdktools.ToolResult{}, nil
		}
	}

	if len(outside) > 0 {
		tempDir := sdktools.TempDirFrom(ctx)
		if tempDir != "" {
			allOSLevel := true
			for _, t := range outside {
				symlinkPrefix := filepath.Clean(t.SymlinkAt) + string(filepath.Separator)
				tempPrefix := filepath.Clean(tempDir) + string(filepath.Separator)
				if !strings.HasPrefix(tempPrefix, symlinkPrefix) {
					allOSLevel = false
					break
				}
			}
			if allOSLevel {
				return false, sdktools.ToolResult{}, nil
			}
		}
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
