package tools

import (
	"context"
	"encoding/json"
	"fmt"

	sdktools "github.com/v0lka/sp4rk/tools"
)

// ExecuteUnattended runs a tool with no interactive-confirmation layer. It
// exists solely for the verify-on-edit runner (user-configured command; see
// specs/domains/verify-on-edit.md): every hard gate applies fail-closed —
// structural validation, disabled tools, group deny, and CANONICAL hard
// reasons only (non-canonical hard reasons pass, deliberately) — while
// hooks, advisory judging and HITL confirmation are skipped. Must never be
// wired into a model-facing execution path.
func (r *ToolRegistry) ExecuteUnattended(ctx context.Context, name string, input json.RawMessage) (sdktools.ToolResult, error) {
	tool, ok := r.Get(name)
	if !ok || tool == nil {
		return sdktools.ToolResult{}, fmt.Errorf("tool %q not found", name)
	}

	// Gate 1: structural validation, same as Execute — fail-open only on
	// unmodeled constructs (empty schemas, $ref subtrees); a level with a
	// declared property set is closed.
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

	// Shell-exec tools: attach the deterministic flowsh digest once, same as
	// Execute — the tool's Judge reads its hard criteria from ctx. The
	// registered instance picks the dialect when an override is active.
	ctx = AttachShellAnalysisForTool(ctx, tool, name, input, r.log())

	// Gate 4: canonical hard reasons block (no confirmation flow); each
	// signal's canonicality is checked independently so a canonical symlink
	// escape is not masked by a non-canonical judge limitation (⊤).
	judgeOutcome := judgeToolCall(ctx, tool, input)
	symlinkReason, symlinkCode := r.symlinkHardReason(ctx, name, tool, input)
	worktreeReason, worktreeCode := worktreeTraversalHardReason(ctx, input)
	blockHard := ""
	switch {
	case !judgeOutcome.Allow && judgeOutcome.Reason != "" &&
		judgeOutcome.Severity == sdktools.JudgeSeverityHard && isCanonicalHardReason(judgeOutcome.ReasonCode):
		blockHard = judgeOutcome.Reason
	case symlinkReason != "" && isCanonicalHardReason(symlinkCode):
		blockHard = symlinkReason
	case worktreeReason != "" && isCanonicalHardReason(worktreeCode):
		blockHard = worktreeReason
	}
	if blockHard != "" {
		r.log().Warn("security: unattended tool blocked by hard safety reason",
			"tool", name, "group", string(group), "reason", blockHard)
		return sdktools.ToolResult{
			Content: "command blocked by security policy: " + blockHard,
			IsError: true,
		}, nil
	}

	return tool.Execute(ctx, input)
}
