package tools

import (
	"context"
	"encoding/json"

	sdktools "github.com/v0lka/sp4rk/tools"
)

// symlinkHardReason returns a HARD confirmation reason when the tool input
// contains symlink traversals that ESCAPE the session roots. Returns "" when
// there is nothing to escalate.
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
// Shell input that is not statically resolvable ($var, $(cmd), backticks,
// process substitution) no longer escalates here: the sp4rk symlink walk is a
// pure literal-path extractor, and dynamic constructs are assessed by the
// deterministic flowsh analysis (criteria C1–C8) on the same call. The deny
// policy is enforced by Execute before this runs, so an escape never bypasses
// an explicit deny.
//
// The returned code classifies the returned reason (sdktools.ReasonCode*): a
// confirmed escape reports ReasonCodeSymlinkEscape. Hosts key deterministic
// policy off the code, never off the prose.
func (r *ToolRegistry) symlinkHardReason(ctx context.Context, name string, tool sdktools.Tool, input json.RawMessage) (string, sdktools.JudgeReasonCode) {
	inside, outside := sdktools.DetectSymlinksInToolInput(ctx, name, input, tool.InputSchema(), r.log())
	if len(inside) == 0 && len(outside) == 0 {
		return "", ""
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
	if len(escapes) == 0 {
		// Only in-root (or OS-level) traversals — acceptable.
		return "", ""
	}

	return sdktools.FormatSymlinkReasoning(inside, escapes), sdktools.ReasonCodeSymlinkEscape
}
