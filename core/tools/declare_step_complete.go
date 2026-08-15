package tools

import (
	"context"
	"encoding/json"
	"fmt"

	sdktools "github.com/v0lka/sp4rk/tools"
)

const toolDeclareStepCompleteDescription = `Signal that an inline plan step is complete. Call this after you finish executing a plan step inline (without delegating to a subagent) to mark it as completed or failed in the plan panel. Do NOT call this for steps you delegated via delegate — the system tracks delegated step completion automatically. Pass success=false with an error message if the step could not be completed. If you are not executing a declared plan inline, do not call this tool.`

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
