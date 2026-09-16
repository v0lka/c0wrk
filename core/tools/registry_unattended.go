package tools

import (
	"context"
	"encoding/json"
	"fmt"

	sdktools "github.com/v0lka/sp4rk/tools"
)

// ExecuteUnattended runs a tool without any interactive-confirmation layer.
// It exists for verify-on-edit (see core/verify_on_edit.go and
// specs/domains/verify-on-edit.md): after a successful file edit the executor
// runs the USER-CONFIGURED verification command (config.yaml
// `executor.verify_on_edit.command`), which must not be routed through
// interactive confirmation — the user already approved it by writing it into
// their config. Model-authored calls NEVER reach this path.
//
// Every hard security gate still applies, fail-closed:
//
//  1. structural input validation (sdktools.ValidateToolInput — required
//     keys, JSON types, unknown keys, recursively into nested objects and
//     array items),
//  2. disabled tools (No Project mode),
//  3. group policy deny,
//  4. hard safety reasons from the tool's Judge + symlink detection (command
//     blacklist, SSRF, symlink escapes) — a hard reason BLOCKS here, because
//     there is no confirmation flow to escalate to.
//
// Deliberately skipped, because the input is fixed config (not model output):
// the pre/post-execute hooks, Smart Approve / advisory judging of soft
// reasons, and HITL confirmation. The Judge itself still runs — its hard
// reasons (notably the execute-group command blacklist compiled into the bash
// tool) must keep firing.
//
// The method is intentionally narrow: it is exported only so the core
// orchestrator layer can build the verify-on-edit runner; it must NOT be
// wired into any model-facing tool-execution path.
func (r *ToolRegistry) ExecuteUnattended(ctx context.Context, name string, input json.RawMessage) (sdktools.ToolResult, error) {
	tool, ok := r.Get(name)
	if !ok || tool == nil {
		return sdktools.ToolResult{}, fmt.Errorf("tool %q not found", name)
	}

	// Gate 1: structural input validation — the SDK's general validator
	// (sdktools.ValidateToolInput), same as Execute: required keys, JSON
	// types, unknown keys, recursively into nested objects and array items.
	// Fail-open ONLY on unmodeled constructs (empty schemas, $ref subtrees,
	// levels without a declared property set); a level with a declared
	// property set is closed, so tolerated-by-the-tool payloads (extra keys,
	// off-type values) are rejected here before dispatch — same as Execute.
	if verr := sdktools.ValidateToolInput(name, tool.InputSchema(), input); verr != nil {
		return sdktools.ErrorResult("%s", verr), nil
	}

	// Gate 2: disabled tools (No Project mode).
	r.mu.RLock()
	disabled := r.disabledTools
	r.mu.RUnlock()
	if disabled != nil && disabled[name] {
		r.log().Warn("security: unattended tool blocked in No Project mode", "tool", name)
		return sdktools.ToolResult{
			Content: fmt.Sprintf("tool %q is not available in No Project mode", name),
			IsError: true,
		}, nil
	}

	group := sdktools.ToolGroupOf(tool)

	// Gate 3: group policy deny.
	if policy := r.groupPolicy(group); policy == sdktools.PolicyAlwaysDeny {
		r.log().Warn("security: unattended tool blocked by group policy (deny)", "tool", name, "group", string(group))
		return sdktools.ToolResult{
			Content: fmt.Sprintf("tool %q blocked by security policy (group %q is set to deny)", name, group),
			IsError: true,
		}, nil
	}

	// Gate 4: hard safety reasons block outright (no confirmation flow here).
	judgeOutcome := judgeToolCall(ctx, tool, input)
	symlinkReason, symlinkCode := r.symlinkHardReason(ctx, name, tool, input)
	reasons := splitSafetyReasons(judgeOutcome, symlinkReason, symlinkCode)
	if reasons.hard != "" {
		r.log().Warn("security: unattended tool blocked by hard safety reason",
			"tool", name, "group", string(group), "reason", reasons.hard)
		return sdktools.ToolResult{
			Content: "command blocked by security policy: " + reasons.hard,
			IsError: true,
		}, nil
	}

	return tool.Execute(ctx, input)
}
