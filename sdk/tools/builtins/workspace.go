package builtins

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/v0lka/sp4rk/agent"
	"github.com/v0lka/sp4rk/tools"
)

const toolReadStepOutputDescription = "Read the complete output of a specific completed step by its ID. Use this when the summary of a dependency step in your task description is insufficient and you need the full, untruncated result. Returns the raw text output exactly as the step produced it."

const toolListStepOutputsDescription = "List all available step outputs with short previews (up to 200 characters each). Use this to discover which completed step results are available before fetching a specific one with read_step_output."

// ReadStepOutputTool reads the full output of a completed step from StepOutputStore.
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
				"description": "The ID of the completed step whose full output you want to read, e.g. \"step_1\""
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

// Execute reads the step output from StepOutputStore.
func (t *ReadStepOutputTool) Execute(ctx context.Context, input json.RawMessage) (tools.ToolResult, error) {
	var params ReadStepOutputInput
	if err := json.Unmarshal(input, &params); err != nil {
		return tools.ParseInputError(err)
	}

	if params.StepID == "" {
		return tools.ToolResult{Content: "validation error: step_id is required", IsError: true}, nil
	}

	store := agent.StepOutputStoreFromContext(ctx)
	if store == nil {
		return tools.ErrorResult("Step output store not available"), nil
	}

	output, ok := store.GetStepOutput(params.StepID)
	if !ok {
		return tools.ErrorResult("No output found for step: %s", params.StepID), nil
	}

	return tools.ToolResult{Content: output}, nil
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

// Execute lists all step outputs from StepOutputStore with previews.
func (t *ListStepOutputsTool) Execute(ctx context.Context, input json.RawMessage) (tools.ToolResult, error) {
	// Validate input (should be empty object)
	var params ListStepOutputsInput
	if err := json.Unmarshal(input, &params); err != nil {
		return tools.ParseInputError(err)
	}

	store := agent.StepOutputStoreFromContext(ctx)
	if store == nil {
		return tools.ErrorResult("Step output store not available"), nil
	}

	entries := store.ListStepOutputs()
	if len(entries) == 0 {
		return tools.ToolResult{Content: "No step outputs available yet"}, nil
	}

	var b strings.Builder
	for _, e := range entries {
		preview := e.FullOutput
		if len(preview) > previewMaxLen {
			preview = preview[:previewMaxLen] + "..."
		}
		preview = strings.ReplaceAll(preview, "\n", " ")
		preview = strings.TrimSpace(preview)

		fmt.Fprintf(&b, "- %s: %s\n", e.StepID, preview)
	}

	return tools.ToolResult{Content: b.String()}, nil
}

// -----------------------------------------------------------------------------
// read_final_result — read the prior task's final result from the blackboard
// -----------------------------------------------------------------------------

const toolReadFinalResultDescription = "Read the final result of the previously completed task on this blackboard. Use this to retrieve the outcome of the prior exchange when it is not visible in the conversation history (e.g. after a backend restart, or when the result was too large to inject verbatim). Returns the raw final answer exactly as the prior task produced it, or an error if no final result is recorded. This tool takes no parameters."

// ReadFinalResultTool reads the final result of a previously completed task
// from FinalResultStore (backed by the blackboard).
type ReadFinalResultTool struct {
	*tools.BaseTool
}

// NewReadFinalResultTool creates a new ReadFinalResultTool instance.
func NewReadFinalResultTool() *ReadFinalResultTool {
	return &ReadFinalResultTool{BaseTool: &tools.BaseTool{
		ToolName:        "read_final_result",
		ToolDescription: toolReadFinalResultDescription,
		Schema: json.RawMessage(`{
		"type": "object",
		"properties": {},
		"required": []
	}`),
		Policy: tools.PolicyAlwaysAllow,
	}}
}

// Execute reads the final result from FinalResultStore.
func (t *ReadFinalResultTool) Execute(ctx context.Context, input json.RawMessage) (tools.ToolResult, error) {
	// Validate input (should be empty object).
	var params struct{}
	if err := json.Unmarshal(input, &params); err != nil {
		return tools.ParseInputError(err)
	}

	store := agent.FinalResultStoreFromContext(ctx)
	if store == nil {
		return tools.ErrorResult("Final result store not available"), nil
	}

	output, ok := store.GetFinalResult()
	if !ok {
		return tools.ErrorResult("No final result is recorded on the blackboard"), nil
	}

	return tools.ToolResult{Content: output}, nil
}
