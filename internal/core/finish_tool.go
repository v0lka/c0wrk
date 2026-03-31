package core

import (
	"context"
	"encoding/json"

	"github.com/user/agent/internal/tools"
)

const toolFinishDescription = "Call this tool when the task is complete. Pass the final answer."

// FinishTool is a special tool that signals task completion.
type FinishTool struct{}

// NewFinishTool creates a new FinishTool.
func NewFinishTool() *FinishTool {
	return &FinishTool{}
}

// Name returns the tool name.
func (t *FinishTool) Name() string {
	return "finish"
}

// Description returns the tool description.
func (t *FinishTool) Description() string {
	return toolFinishDescription
}

// InputSchema returns the JSON schema for the tool input.
func (t *FinishTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"answer": {
				"type": "string",
				"description": "The final answer to the user's task"
			}
		},
		"required": ["answer"]
	}`)
}

// DefaultPolicy returns PolicyAlwaysAllow because finish tool only signals completion.
func (t *FinishTool) DefaultPolicy() tools.ToolPolicy {
	return tools.PolicyAlwaysAllow
}

// Execute parses the input and returns the answer.
func (t *FinishTool) Execute(ctx context.Context, input json.RawMessage) (tools.ToolResult, error) {
	var params struct {
		Answer string `json:"answer"`
	}
	if err := json.Unmarshal(input, &params); err != nil {
		return tools.ToolResult{Content: "failed to parse finish input: " + err.Error(), IsError: true}, nil
	}
	return tools.ToolResult{Content: params.Answer, IsError: false}, nil
}
