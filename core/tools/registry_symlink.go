package tools

import (
	"context"
	"encoding/json"

	sdktools "github.com/v0lka/sp4rk/tools"
)

// symlinkHardReason returns a HARD confirmation reason when the tool input
// contains symlink traversals that ESCAPE the session roots, or shell input
// that cannot be resolved at all (unexpandable/dynamic tokens). Returns ""
// when there is nothing to escalate.
//
// A symlink whose resolution stays INSIDE the session roots is not a concern:
// every containment check in the pipeline reasons about resolved paths, so an
// in-root resolution qualifies for auto-approval exactly like a direct path.
// Benign OS-level infrastructure (well-known OS symlinks such as /tmp →
// /private/tmp, or symlinks that are ancestors of a session root) is exempt
// even when the traversal technically lands outside, so the classification is
// delegated to sp4rk's IsOSLevelSymlink — os_symlinks.go is the single source
// of truth shared by the sp4rk symlink walker and this core gate.
//
// Unresolvable input escalates fail-closed (hard), mirroring the SSRF judge's
// "unassessable" posture. The deny policy is enforced by Execute before this
// runs, so an escape never bypasses an explicit deny.
func (r *ToolRegistry) symlinkHardReason(ctx context.Context, name string, tool sdktools.Tool, input json.RawMessage) string {
	inside, outside, suspicious := sdktools.DetectSymlinksInToolInput(ctx, name, input, tool.InputSchema(), r.log())
	if len(inside) == 0 && len(outside) == 0 && !suspicious {
		return ""
	}

	roots := sdktools.SessionRoots(ctx)

	// Escapes are only the traversals that are neither OS-level
	// infrastructure nor resolvable inside the session roots.
	escapes := make([]sdktools.SymlinkTraversal, 0, len(outside))
	for _, t := range outside {
		if !sdktools.IsOSLevelSymlink(t.SymlinkAt, roots...) {
			escapes = append(escapes, t)
		}
	}
	if len(escapes) == 0 && !suspicious {
		// Only in-root (or OS-level) traversals — acceptable.
		return ""
	}

	return sdktools.FormatSymlinkReasoning(inside, escapes, suspicious)
}
