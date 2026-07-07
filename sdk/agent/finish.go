package agent

import (
	"context"
	"encoding/json"

	"github.com/v0lka/sp4rk/tools"
)

const toolFinishDescription = `Signal task completion and deliver the final result. Call this tool exactly once, only after all work is done. Before calling finish, you MUST verify that every acceptance criterion from your task is satisfied — use tool calls to confirm, not assumptions. If any criterion is unmet, continue working instead of calling finish. The answer parameter should contain the complete result: findings, analysis, code summaries, or any deliverable relevant to the task. Include the specific deliverables requested by the task. Summarize key findings concisely.`

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

// IsUntrusted returns false — finish is a trusted internal tool.
func (t *FinishTool) IsUntrusted() bool { return false }

// Execute parses the input and returns the answer.
func (t *FinishTool) Execute(ctx context.Context, input json.RawMessage) (tools.ToolResult, error) {
	var params struct {
		Answer string `json:"answer"`
	}
	if err := json.Unmarshal(input, &params); err != nil {
		//nolint:nilerr // error is reported in ToolResult.Content
		return tools.ToolResult{Content: "failed to parse finish input: " + err.Error(), IsError: true}, nil
	}
	return tools.ToolResult{Content: params.Answer, IsError: false}, nil
}
