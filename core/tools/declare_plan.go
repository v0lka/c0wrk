package tools

import (
	"context"
	"encoding/json"
	"fmt"

	sdktools "github.com/v0lka/c0wrk/sdk/tools"
)

const toolDeclarePlanDescription = `Publish a roadmap to the plan panel and blackboard. Use this to surface a structured plan to the user before large implementations, or to track a multi-step task's shape. With mode="present" the plan is displayed but execution continues. With mode="await_approval" the tool blocks until the user approves, requests changes, or abandons — use this when an active skill prescribes an approval gate before implementation, or when the task is large enough that committing without sign-off is risky. On "request changes" the tool returns the feedback; revise and call declare_plan again.`

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
					"depends_on": {"type": "array", "items": {"type": "string"}, "description": "IDs of tasks that must complete before this one"}
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
