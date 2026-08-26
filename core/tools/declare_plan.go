package tools

import (
	"context"
	"encoding/json"
	"fmt"

	sdktools "github.com/v0lka/sp4rk/tools"
)

const toolDeclarePlanDescription = `Purpose: publish the task roadmap — ordered steps with acceptance criteria — for user sign-off before any implementation.
Use when: the user asked to plan first or the task is genuinely multi-step; call once, before acting. Steps may carry an agent field targeting a subagent for that step. Dependencies reference step ids; independent steps can run in parallel. After approval the plan is append-only — new steps may only be added at the end, never edited or deleted.
Inputs: mode (optional: "present" | "await_approval"); tasks — array of {id (stable, e.g. step_1), summary (5-7 word UI label), description (What/How/Where/Acceptance criteria), depends_on (prerequisite ids), agent (optional Subagent Profile name for this step)}.
Mode choice: pass "await_approval" when the task is large enough that committing without sign-off is risky, when the user asked to plan first, or when an active skill prescribes an approval gate before implementation. Reserve "present" (display-only, execution continues without sign-off) for low-stakes progress-shaping.
Outputs: with "present" the plan is displayed and execution continues; with "await_approval" the call blocks until the user approves, requests changes, or abandons — on "request changes" the feedback is returned; revise the tasks and call declare_plan again.
Example: step 1 "write failing tests", step 2 "implement" with depends_on ["step_1"].
Anti-example: never implement before approval in await_approval mode; single-step tasks need no plan — act directly; never rewrite approved steps — append corrections instead.`

// PlanPublisher serializes a plan, persists it to the session plans directory,
// emits the PlanGenerated event, and sets the plan on the blackboard.
// The implementation lives in the core layer.
type PlanPublisher interface {
	Publish(ctx context.Context, tasks []PlanTaskInput) (planPath string, err error)
	// LastPlanMarkdown returns the markdown content from the most recent
	// Publish call. Used by declare_plan to pass the content to the approval
	// callback without re-reading from disk.
	LastPlanMarkdown() string
}

// PlanTaskInput is the user-facing shape of a single roadmap task.
// The publisher converts these into the internal Plan/PlanStep types.
type PlanTaskInput struct {
	ID          string   `json:"id"`
	Summary     string   `json:"summary"`
	Description string   `json:"description"`
	DependsOn   []string `json:"depends_on,omitempty"`
	// Agent optionally names a Subagent Profile to execute this step with.
	// The publisher copies it onto the resulting PlanStep so the execution
	// layer (Conductor) can resolve and apply the profile. See declare_plan's
	// schema for the user-facing description.
	Agent string `json:"agent,omitempty"`
}

// DeclarePlanTool publishes a roadmap and optionally blocks for user approval.
type DeclarePlanTool struct {
	*sdktools.BaseTool
	approvalFunc ApprovalFunc
}

// ApprovalFunc is called when declare_plan runs in await_approval mode.
// Returns the user's decision: "approve", "request_changes" (with feedback),
// or "abandon". If nil, await_approval mode is unavailable.
type ApprovalFunc func(ctx context.Context, planPath, planMarkdown string) (decision string, feedback string, err error)

// NewDeclarePlanTool creates the declare_plan tool. approvalFunc may be nil;
// in that case await_approval mode returns an error if invoked.
func NewDeclarePlanTool(approvalFunc ApprovalFunc) *DeclarePlanTool {
	return &DeclarePlanTool{
		BaseTool: &sdktools.BaseTool{
			ToolGroup:       sdktools.GroupSystem,
			ToolName:        "declare_plan",
			ToolDescription: toolDeclarePlanDescription,
			Schema: json.RawMessage(`{
	"type": "object",
	"properties": {
		"mode": {"type": "string", "enum": ["present", "await_approval"], "description": "present (default) displays the plan; await_approval blocks for user approval before returning"},
		"tasks": {
			"type": "array",
			"minItems": 1,
			"items": {
				"type": "object",
				"properties": {
					"id": {"type": "string", "description": "Unique task identifier (e.g. step_1)"},
					"summary": {"type": "string", "description": "5-7 word label for UI display"},
					"description": {"type": "string", "description": "Full task description with What/How/Where/Acceptance Criteria"},
					"depends_on": {"type": "array", "items": {"type": "string"}, "description": "IDs of tasks that must complete before this one"},
					"agent": {"type": "string", "description": "Optional Subagent Profile name to execute this step with (e.g. \"code-reviewer\"). When set, the step runs with that profile's system prompt, tools, max-steps, and model instead of the orchestrator defaults. Omit for a generic step."}
				},
				"required": ["id", "summary", "description"]
			}
		}
	},
	"required": ["tasks"]
}`),
			Policy: sdktools.PolicyAlwaysAllow,
		},
		approvalFunc: approvalFunc,
	}
}

type declarePlanInput struct {
	Mode  string          `json:"mode"`
	Tasks []PlanTaskInput `json:"tasks"`
}

func (t *DeclarePlanTool) Execute(ctx context.Context, input json.RawMessage) (sdktools.ToolResult, error) {
	var params declarePlanInput
	if err := json.Unmarshal(input, &params); err != nil {
		return sdktools.ParseInputError(err)
	}
	if len(params.Tasks) == 0 {
		return sdktools.ErrorResult("validation error: tasks array must not be empty"), nil
	}
	mode := params.Mode
	if mode == "" {
		mode = "present"
	}
	if mode != "present" && mode != "await_approval" {
		return sdktools.ErrorResult("validation error: mode must be \"present\" or \"await_approval\", got %q", mode), nil
	}

	publisher := PlanPublisherFrom(ctx)
	if publisher == nil {
		return sdktools.ErrorResult("declare_plan: no plan publisher in context (not running inside a Conductor)"), nil
	}

	planPath, err := publisher.Publish(ctx, params.Tasks)
	if err != nil {
		return sdktools.ErrorResult("declare_plan: failed to publish plan: %v", err), nil
	}

	if mode == "present" {
		return sdktools.ToolResult{Content: fmt.Sprintf("Plan published to %s and displayed in the plan panel. Execution continues.", planPath)}, nil
	}

	if t.approvalFunc == nil {
		return sdktools.ErrorResult("declare_plan: await_approval mode is not available (no approval callback configured)"), nil
	}

	decision, feedback, err := t.approvalFunc(ctx, planPath, publisher.LastPlanMarkdown())
	if err != nil {
		return sdktools.ErrorResult("declare_plan: approval callback failed: %v", err), nil
	}

	switch decision {
	case "approve":
		return sdktools.ToolResult{Content: "Plan approved by user. Proceeding with implementation."}, nil
	case "request_changes":
		msg := "User requested changes to the plan."
		if feedback != "" {
			msg += " Feedback: " + feedback
		}
		msg += "\n\nRevise the plan and call declare_plan again with the updated tasks."
		return sdktools.ToolResult{Content: msg}, nil
	case "abandon":
		return sdktools.ToolResult{Content: "User abandoned the plan. Do not proceed with implementation unless the user gives new instructions.", IsError: true}, nil
	default:
		return sdktools.ToolResult{Content: fmt.Sprintf("Approval callback returned unknown decision %q; treating as request_changes.", decision)}, nil
	}
}

// --- Context plumbing ---

type planPublisherKey struct{}

// WithPlanPublisher injects the publisher into the context.
func WithPlanPublisher(ctx context.Context, publisher PlanPublisher) context.Context {
	return context.WithValue(ctx, planPublisherKey{}, publisher)
}

// PlanPublisherFrom extracts the publisher from the context, or returns nil.
func PlanPublisherFrom(ctx context.Context) PlanPublisher {
	if v, ok := ctx.Value(planPublisherKey{}).(PlanPublisher); ok {
		return v
	}
	return nil
}
