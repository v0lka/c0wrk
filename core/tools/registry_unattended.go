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
//  4. CANONICAL hard safety reasons from the tool's Judge + symlink
//     detection — a fired security control (command blocklist; the flowsh
//     shell-analysis controls: exfiltration flow, privilege escalation,
//     system-path/raw-device write, irreversible destructive write outside
//     the session roots, download cradle) or a symlink escape out of the
//     session roots BLOCKS here, because there is no confirmation flow to
//     escalate to. A NON-canonical hard reason — most notably
//     ReasonCodeCommandUnboundedAnalysis, the analyzer's ⊤ "could not bound
//     this command" limitation — does NOT block: the command is the user's
//     own verification config, and an analysis limitation is not grounds to
//     break the verification loop the user explicitly opted into.
//
// Deliberately skipped, because the input is fixed config (not model output):
// the pre/post-execute hooks, Smart Approve / advisory judging of soft
// reasons, and HITL confirmation. The Judge itself still runs — with the
// flowsh digest attached (AttachShellAnalysis), exactly as in Execute — so
// its canonical hard controls (the compiled-in command blocklist plus the
// flowsh criteria) keep firing.
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

	// Shell-exec tools: attach the deterministic flowsh digest once, same as
	// Execute — the tool's Judge reads its hard criteria from ctx. The
	// registered instance picks the dialect when an override is active.
	ctx = AttachShellAnalysisForTool(ctx, tool, name, input, r.log())

	// Gate 4: canonical hard safety reasons block outright (no confirmation
	// flow here). Both signals are checked for canonicality independently —
	// splitSafetyReasons folds judge-hard ahead of the symlink signal, and a
	// canonical symlink escape must not be masked by a non-canonical judge
	// limitation (⊤). Non-canonical hard reasons and soft reasons pass to
	// execution: see the method doc for the verify-on-edit rationale.
	judgeOutcome := judgeToolCall(ctx, tool, input)
	symlinkReason, symlinkCode := r.symlinkHardReason(ctx, name, tool, input)
	blockHard := ""
	if !judgeOutcome.Allow && judgeOutcome.Reason != "" &&
		judgeOutcome.Severity == sdktools.JudgeSeverityHard && isCanonicalHardReason(judgeOutcome.ReasonCode) {
		blockHard = judgeOutcome.Reason
	} else if symlinkReason != "" && isCanonicalHardReason(symlinkCode) {
		blockHard = symlinkReason
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
