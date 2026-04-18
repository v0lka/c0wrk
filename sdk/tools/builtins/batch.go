package builtins

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/user/agent/sdk/tools"
)

const toolBatchDescription = `Execute multiple independent tools in parallel. Recursive batch calls are not allowed.

Use this tool to run several tool calls at once when they do NOT depend on each other's results and do NOT produce side effects that affect other tools in the same batch.

Good examples:
- Reading multiple files at once
- Running several grep searches in parallel
- Fetching metadata for different paths

Bad examples (do NOT batch these):
- Writing a file then reading it back
- Creating a directory then listing its contents
- Any sequence where one call's output is another call's input`

// toolDispatcher abstracts tool execution to avoid circular imports with the agent package.
type toolDispatcher interface {
	Execute(ctx context.Context, name string, input json.RawMessage) (tools.ToolResult, error)
}

// BatchCall represents a single tool invocation within a batch.
type BatchCall struct {
	Tool  string          `json:"tool"`
	Input json.RawMessage `json:"input"`
}

// BatchInput is the input schema for BatchTool.
type BatchInput struct {
	Calls []BatchCall `json:"calls"`
}

// BatchCallResult is the outcome of a single tool invocation.
type BatchCallResult struct {
	Tool    string `json:"tool"`
	Success bool   `json:"success"`
	Output  string `json:"output,omitempty"`
	Error   string `json:"error,omitempty"`
}

// BatchOutput is the aggregated result of all tool invocations.
type BatchOutput struct {
	Results []BatchCallResult `json:"results"`
}

// BatchTool executes multiple independent tools in parallel.
type BatchTool struct {
	*tools.BaseTool
	dispatcher toolDispatcher
}

// NewBatchTool creates a new BatchTool with the given dispatcher.
func NewBatchTool(dispatcher toolDispatcher) *BatchTool {
	return &BatchTool{
		BaseTool: &tools.BaseTool{
			ToolName:        "batch",
			ToolDescription: toolBatchDescription,
			Schema: json.RawMessage(`{
	"type": "object",
	"properties": {
		"calls": {
			"type": "array",
			"description": "List of tool calls to execute in parallel. Each call must be independent.",
			"items": {
				"type": "object",
				"properties": {
					"tool": {
						"type": "string",
						"description": "Name of the tool to execute"
					},
					"input": {
						"type": "object",
						"description": "Input arguments for the tool"
					}
				},
				"required": ["tool", "input"]
			}
		}
	},
	"required": ["calls"]
}`),
			Policy: tools.PolicyAlwaysAllow,
		},
		dispatcher: dispatcher,
	}
}

// Execute runs all calls in parallel, respecting concurrency limits, and returns aggregated results.
func (bt *BatchTool) Execute(ctx context.Context, input json.RawMessage) (tools.ToolResult, error) {
	var params BatchInput
	if err := json.Unmarshal(input, &params); err != nil {
		return tools.ParseInputError(err)
	}

	if len(params.Calls) == 0 {
		return tools.ErrorResult("batch calls list is empty"), nil
	}

	// Recursion guard: disallow nested batch calls.
	for _, call := range params.Calls {
		if call.Tool == "batch" {
			return tools.ErrorResult("recursive batch calls are not allowed"), nil
		}
	}

	results := make([]BatchCallResult, len(params.Calls))
	var wg sync.WaitGroup

	for i, call := range params.Calls {
		wg.Add(1)
		go func(idx int, c BatchCall) {
			defer wg.Done()

			res, err := bt.dispatcher.Execute(ctx, c.Tool, c.Input)
			if err != nil {
				results[idx] = BatchCallResult{Tool: c.Tool, Success: false, Error: err.Error()}
				return
			}
			if res.IsError {
				results[idx] = BatchCallResult{Tool: c.Tool, Success: false, Error: res.Content}
				return
			}

			results[idx] = BatchCallResult{Tool: c.Tool, Success: true, Output: res.Content}
		}(i, call)
	}

	wg.Wait()

	out, err := json.Marshal(BatchOutput{Results: results})
	if err != nil {
		return tools.ErrorResult("failed to marshal batch output: %v", err), nil
	}

	return tools.ToolResult{Content: string(out), IsError: false}, nil
}
