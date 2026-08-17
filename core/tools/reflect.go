package tools

import (
	"context"
	"encoding/json"
	"fmt"

	sdktools "github.com/v0lka/sp4rk/tools"
)

const toolReflectDescription = `Purpose: pause and re-evaluate the strategy — replan instead of repeating a failing approach.
Use when: multiple attempts at the same goal have failed, or an earlier step already satisfied the user's request, or the goal turns out to be already achieved. Reflection is the runtime's escape hatch from unproductive loops.
Inputs: none required — the runtime triggers reflection when due; optionally scope ("trajectory" | "delegation") narrows the target and delegation_id (required with scope "delegation") picks the subagent run.
Outputs: a revised plan of action for the remaining work.
Example: two consecutive failed test runs -> reflect reconsiders the approach rather than launching a third identical run.
Anti-example: not a progress report (use your reply); not after every minor error — only when the strategy itself is in doubt; once the goal is met, finish instead of reflecting.`

// ReflectionResult is the surfaced outcome of a reflection call.
type ReflectionResult struct {
	Summary         string `json:"summary"`
	SuggestedAction string `json:"suggested_action"`
	RootCause       string `json:"root_cause"`
	ActionPlan      string `json:"action_plan"`
}

// ReflectionRunner builds the trajectory, invokes the Reflector, persists the
// reflection to the blackboard, and emits the OnReflected event.
// The implementation lives in the core layer.
type ReflectionRunner interface {
	Reflect(ctx context.Context, scope, delegationID string) (ReflectionResult, error)
}

// ReflectTool invokes the reflector via a runner injected through context.
type ReflectTool struct {
	*sdktools.BaseTool
}

// NewReflectTool creates the reflect tool.
func NewReflectTool() *ReflectTool {
	return &ReflectTool{
		BaseTool: &sdktools.BaseTool{
			ToolGroup:       sdktools.GroupSystem,
			ToolName:        "reflect",
			ToolDescription: toolReflectDescription,
			Schema: json.RawMessage(`{
	"type": "object",
	"properties": {
		"scope": {"type": "string", "enum": ["trajectory", "delegation"], "description": "trajectory (default) reflects on the whole Conductor run so far; delegation reflects on a specific subagent's steps"},
		"delegation_id": {"type": "string", "description": "Required when scope=delegation; the delegation ID to reflect on"}
	}
}`),
			Policy: sdktools.PolicyAlwaysAllow,
		},
	}
}

type reflectInput struct {
	Scope        string `json:"scope"`
	DelegationID string `json:"delegation_id"`
}

func (t *ReflectTool) Execute(ctx context.Context, input json.RawMessage) (sdktools.ToolResult, error) {
	var params reflectInput
	if err := json.Unmarshal(input, &params); err != nil {
		return sdktools.ParseInputError(err)
	}
	scope := params.Scope
	if scope == "" {
		scope = "trajectory"
	}
	if scope != "trajectory" && scope != "delegation" {
		return sdktools.ErrorResult("validation error: scope must be \"trajectory\" or \"delegation\", got %q", scope), nil
	}
	if scope == "delegation" && params.DelegationID == "" {
		return sdktools.ErrorResult("validation error: delegation_id is required when scope=delegation"), nil
	}

	runner := ReflectionRunnerFrom(ctx)
	if runner == nil {
		return sdktools.ErrorResult("reflect: no reflection runner in context (not running inside a Conductor)"), nil
	}

	result, err := runner.Reflect(ctx, scope, params.DelegationID)
	if err != nil {
		return sdktools.ErrorResult("reflect failed: %v", err), nil
	}

	out := fmt.Sprintf("## Reflection\n\n**Summary:** %s\n\n**Suggested action:** %s\n\n**Root cause:** %s\n\n**Action plan:** %s\n",
		result.Summary, result.SuggestedAction, result.RootCause, result.ActionPlan)
	out += "\nBased on the suggested action, decide how to proceed: retry the failed work (possibly with a revised delegation), revise your approach (replan), or abort and report the failure to the user."
	return sdktools.ToolResult{Content: out}, nil
}

// --- Context plumbing ---

type reflectionRunnerKey struct{}

// WithReflectionRunner injects the runner into the context.
func WithReflectionRunner(ctx context.Context, runner ReflectionRunner) context.Context {
	return context.WithValue(ctx, reflectionRunnerKey{}, runner)
}

// ReflectionRunnerFrom extracts the runner from the context, or returns nil.
func ReflectionRunnerFrom(ctx context.Context) ReflectionRunner {
	if v, ok := ctx.Value(reflectionRunnerKey{}).(ReflectionRunner); ok {
		return v
	}
	return nil
}
