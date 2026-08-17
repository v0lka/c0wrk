package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

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
//  1. required-field validation (defense-in-depth),
//  2. disabled tools (No Project mode),
//  3. the per-session extra shell blacklist (No Project mode),
//  4. group policy deny,
//  5. hard safety reasons from the tool's Judge + symlink detection (command
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

	// Gate 1: required-field validation — same fail-safe behavior as Execute.
	if missing := validateRequiredFields(tool.InputSchema(), input); len(missing) > 0 {
		return sdktools.ToolResult{
			Content: "validation error: missing required parameter(s): " + strings.Join(missing, ", "),
			IsError: true,
		}, nil
	}

	// Gate 2: disabled tools (No Project mode).
	r.mu.RLock()
	disabled := r.disabledTools
	extraShellBL := r.extraShellBlacklist
	r.mu.RUnlock()
	if disabled != nil && disabled[name] {
		r.log().Warn("security: unattended tool blocked in No Project mode", "tool", name)
		return sdktools.ToolResult{
			Content: fmt.Sprintf("tool %q is not available in No Project mode", name),
			IsError: true,
		}, nil
	}

	// Gate 3: extra shell blacklist — hard block that no policy can weaken.
	if pattern, command := matchExtraShellBlacklist(name, input, extraShellBL); pattern != "" {
		r.log().Warn("security: unattended shell command blocked by extra blacklist",
			"tool", name, "command", command, "pattern", pattern)
		return sdktools.ToolResult{
			Content: fmt.Sprintf("command %q blocked by blacklist (matched pattern %q)", command, pattern),
			IsError: true,
		}, nil
	}

	group := tool.Group()

	// Gate 4: group policy deny.
	if policy := r.groupPolicy(group); policy == sdktools.PolicyAlwaysDeny {
		r.log().Warn("security: unattended tool blocked by group policy (deny)", "tool", name, "group", string(group))
		return sdktools.ToolResult{
			Content: fmt.Sprintf("tool %q blocked by security policy (group %q is set to deny)", name, group),
			IsError: true,
		}, nil
	}

	// Gate 5: hard safety reasons block outright (no confirmation flow here).
	judgeOutcome := judgeToolCall(ctx, tool, input)
	symlinkReason := r.symlinkHardReason(ctx, name, tool, input)
	hardReason, _ := splitSafetyReasons(judgeOutcome, symlinkReason)
	if hardReason != "" {
		r.log().Warn("security: unattended tool blocked by hard safety reason",
			"tool", name, "group", string(group), "reason", hardReason)
		return sdktools.ToolResult{
			Content: "command blocked by security policy: " + hardReason,
			IsError: true,
		}, nil
	}

	return tool.Execute(ctx, input)
}
