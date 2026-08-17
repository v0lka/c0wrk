package tools

import (
	"context"
	"encoding/json"
	"fmt"

	sdktools "github.com/v0lka/sp4rk/tools"
)

const toolDeclareStepCompleteDescription = `Purpose: mark a declared plan step as completed or failed in the plan panel.
Use when: exactly once per step, right after finishing it — and only for steps you execute inline; delegated steps are tracked automatically by the system.
Inputs: step_id; success (bool); error (optional failure message when success=false).
Outputs: the step's status update in the plan panel.
Example: step_id "step_2", success true once its acceptance criteria verify green.
Anti-example: not for delegated steps; never call twice for one step; do not report success while acceptance criteria are unmet.`

// StepCompleteFunc is called by declare_step_complete to signal that an
// inline plan step has finished. success=false indicates failure; errMsg
// provides an optional failure reason.
type StepCompleteFunc func(stepID string, success bool, errMsg string)

// DeclareStepCompleteTool signals completion of an inline plan step.
type DeclareStepCompleteTool struct {
	*sdktools.BaseTool
}

// NewDeclareStepCompleteTool creates the declare_step_complete tool.
func NewDeclareStepCompleteTool() *DeclareStepCompleteTool {
	return &DeclareStepCompleteTool{BaseTool: &sdktools.BaseTool{
		ToolGroup:       sdktools.GroupSystem,
		ToolName:        "declare_step_complete",
		ToolDescription: toolDeclareStepCompleteDescription,
		Schema: json.RawMessage(`{
	"type": "object",
	"properties": {
		"step_id": {
			"type": "string",
			"description": "The plan step ID that is now complete."
		},
		"success": {
			"type": "boolean",
			"description": "Whether the step completed successfully. Default true.",
			"default": true
		},
		"error": {
			"type": "string",
			"description": "Optional failure message when success is false."
		}
	},
	"required": ["step_id"]
}`),
		Policy: sdktools.PolicyAlwaysAllow,
	}}
}

type declareStepCompleteInput struct {
	StepID  string `json:"step_id"`
	Success *bool  `json:"success,omitempty"`
	Error   string `json:"error,omitempty"`
}

func (t *DeclareStepCompleteTool) Execute(ctx context.Context, input json.RawMessage) (sdktools.ToolResult, error) {
	var params declareStepCompleteInput
	if err := json.Unmarshal(input, &params); err != nil {
		return sdktools.ParseInputError(err)
	}
	if params.StepID == "" {
		return sdktools.ErrorResult("validation error: step_id is required"), nil
	}

	success := true
	if params.Success != nil {
		success = *params.Success
	}

	fn := StepCompleteFuncFromContext(ctx)
	if fn == nil {
		return sdktools.ErrorResult("declare_step_complete: no step-complete callback in context (not running inside a Conductor)"), nil
	}

	fn(params.StepID, success, params.Error)

	if success {
		return sdktools.ToolResult{Content: fmt.Sprintf("Step %s marked as completed.", params.StepID)}, nil
	}
	msg := fmt.Sprintf("Step %s marked as failed.", params.StepID)
	if params.Error != "" {
		msg += " Error: " + params.Error
	}
	return sdktools.ToolResult{Content: msg}, nil
}

// --- Context plumbing ---

type stepCompleteFuncKey struct{}

// WithStepCompleteFunc injects the step-complete callback into the context.
func WithStepCompleteFunc(ctx context.Context, fn StepCompleteFunc) context.Context {
	return context.WithValue(ctx, stepCompleteFuncKey{}, fn)
}

// StepCompleteFuncFromContext extracts the step-complete callback, or returns nil.
func StepCompleteFuncFromContext(ctx context.Context) StepCompleteFunc {
	if v, ok := ctx.Value(stepCompleteFuncKey{}).(StepCompleteFunc); ok {
		return v
	}
	return nil
}
