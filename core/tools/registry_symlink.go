package tools

import (
	"context"
	"encoding/json"

	sdktools "github.com/v0lka/sp4rk/tools"
)

// symlinkHardReason returns a HARD confirmation reason (ReasonCodeSymlinkEscape)
// when the input's literal symlink traversals escape the session roots; in-root
// and OS-level resolutions are exempt (os_symlinks.go is the shared source of
// truth). Semantics: specs/architecture/security-model.md, specs/decisions/055-symlink-gate-literal-paths-only.md.
func (r *ToolRegistry) symlinkHardReason(ctx context.Context, name string, tool sdktools.Tool, input json.RawMessage) (string, sdktools.JudgeReasonCode) {
	inside, outside := sdktools.DetectSymlinksInToolInput(ctx, name, input, tool.InputSchema(), r.log())
	if len(inside) == 0 && len(outside) == 0 {
		return "", ""
	}

	roots := sdktools.SessionRoots(ctx)

	escapes := make([]sdktools.SymlinkTraversal, 0, len(outside))
	for _, t := range outside {
		if !sdktools.IsOSLevelSymlink(t.SymlinkAt, roots...) {
			escapes = append(escapes, t)
		}
	}
	if len(escapes) == 0 {
		return "", ""
	}

	return sdktools.FormatSymlinkReasoning(inside, escapes), sdktools.ReasonCodeSymlinkEscape
}
