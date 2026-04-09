package builtins

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/user/agent/sdk/agent"
	"github.com/user/agent/sdk/tools"
)

const toolReadStepOutputDescription = "Read the complete output of a specific completed step by its ID. Use this when you need the full output from a dependency step that was only summarized in your task description."

const toolListStepOutputsDescription = "List all available step outputs with previews. Use this to discover what step outputs are available to read."

// ReadStepOutputTool reads the full output of a completed step from SharedWorkspace.
type ReadStepOutputTool struct {
	*tools.BaseTool
}

// NewReadStepOutputTool creates a new ReadStepOutputTool instance.
func NewReadStepOutputTool() *ReadStepOutputTool {
	return &ReadStepOutputTool{BaseTool: &tools.BaseTool{
		ToolName:        "read_step_output",
		ToolDescription: toolReadStepOutputDescription,
		Schema: json.RawMessage(`{
		"type": "object",
		"properties": {
			"step_id": {
				"type": "string",
				"description": "The ID of the step whose output you want to read"
			}
		},
		"required": ["step_id"]
	}`),
		Policy: tools.PolicyAlwaysAllow,
	}}
}

// ReadStepOutputInput represents the input parameters for read_step_output.
type ReadStepOutputInput struct {
	StepID string `json:"step_id"`
}

// Execute reads the step output from SharedWorkspace.
func (t *ReadStepOutputTool) Execute(ctx context.Context, input json.RawMessage) (tools.ToolResult, error) {
	var params ReadStepOutputInput
	if err := json.Unmarshal(input, &params); err != nil {
		return tools.ParseInputError(err)
	}

	ws := agent.SharedWorkspaceFromContext(ctx)
	if ws == nil {
		return tools.ErrorResult("Workspace not available"), nil
	}

	artifact, ok := ws.Get(params.StepID + "/output")
	if !ok {
		return tools.ErrorResult("No output found for step: %s", params.StepID), nil
	}

	return tools.ToolResult{Content: artifact.Content}, nil
}

// ListStepOutputsTool lists all available step outputs with previews.
type ListStepOutputsTool struct {
	*tools.BaseTool
}

// NewListStepOutputsTool creates a new ListStepOutputsTool instance.
func NewListStepOutputsTool() *ListStepOutputsTool {
	return &ListStepOutputsTool{BaseTool: &tools.BaseTool{
		ToolName:        "list_step_outputs",
		ToolDescription: toolListStepOutputsDescription,
		Schema: json.RawMessage(`{
		"type": "object",
		"properties": {},
		"required": []
	}`),
		Policy: tools.PolicyAlwaysAllow,
	}}
}

// ListStepOutputsInput represents the input parameters for list_step_outputs.
type ListStepOutputsInput struct{}

const previewMaxLen = 200

// Execute lists all step outputs from SharedWorkspace with previews.
func (t *ListStepOutputsTool) Execute(ctx context.Context, input json.RawMessage) (tools.ToolResult, error) {
	// Validate input (should be empty object)
	var params ListStepOutputsInput
	if err := json.Unmarshal(input, &params); err != nil {
		return tools.ParseInputError(err)
	}

	ws := agent.SharedWorkspaceFromContext(ctx)
	if ws == nil {
		return tools.ErrorResult("Workspace not available"), nil
	}

	artifacts := ws.List()
	if len(artifacts) == 0 {
		return tools.ToolResult{Content: "No step outputs available yet"}, nil
	}

	var b strings.Builder
	for _, artifact := range artifacts {
		// Extract step ID from key (format: "stepID/output")
		stepID := artifact.Key
		if idx := strings.LastIndex(stepID, "/output"); idx != -1 {
			stepID = stepID[:idx]
		}

		preview := artifact.Content
		if len(preview) > previewMaxLen {
			preview = preview[:previewMaxLen] + "..."
		}
		preview = strings.ReplaceAll(preview, "\n", " ")
		preview = strings.TrimSpace(preview)

		fmt.Fprintf(&b, "- %s: %s\n", stepID, preview)
	}

	return tools.ToolResult{Content: b.String()}, nil
}
