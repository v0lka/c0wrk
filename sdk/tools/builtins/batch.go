package builtins

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/v0lka/sp4rk/tools"
)

const batchDescription = `Execute multiple tool calls sequentially in one turn. Provide an array of calls, each with a "tool" name and "input" object. All calls execute in order even if one fails — errors are captured per-call and do not abort the batch. Use this to reduce round-trips when you know all the calls you want to make.`

// BatchTool allows the LLM to batch multiple independent tool calls.
// The actual batch logic is handled at the executor level; this tool
// exists to expose the JSON schema to the LLM.
type BatchTool struct {
	*tools.BaseTool
}

// NewBatchTool creates a new BatchTool instance.
func NewBatchTool() *BatchTool {
	return &BatchTool{BaseTool: &tools.BaseTool{
		ToolName:        "batch",
		ToolDescription: batchDescription,
		Schema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"calls": {
					"type": "array",
					"items": {
						"type": "object",
						"properties": {
							"tool": {"type": "string", "description": "Name of the tool to call"},
							"input": {"type": "object", "description": "Arguments to pass to the tool"}
						},
						"required": ["tool", "input"]
					}
				}
			},
			"required": ["calls"]
		}`),
		Policy: tools.PolicyAlwaysAllow,
	}}
}

// Execute returns an error — batch is intercepted and handled at the executor level.
func (t *BatchTool) Execute(ctx context.Context, input json.RawMessage) (tools.ToolResult, error) {
	return tools.ToolResult{}, errors.New("batch is handled at the executor level and should not be called directly")
}
