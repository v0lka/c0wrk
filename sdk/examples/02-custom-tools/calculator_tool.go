package main

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/v0lka/sp4rk/tools"
)

// CalculatorTool evaluates simple arithmetic expressions.
// It embeds tools.BaseTool which provides default implementations of
// Name, Description, InputSchema, DefaultPolicy, and IsUntrusted.
//
// This type lives in a tagless file so that both the classic (main.go) and
// fluent (main_fluent.go) example variants can register it.
type CalculatorTool struct {
	*tools.BaseTool
}

// NewCalculatorTool creates a new CalculatorTool.
func NewCalculatorTool() *CalculatorTool {
	return &CalculatorTool{BaseTool: &tools.BaseTool{
		ToolName:        "calculator",
		ToolDescription: "Evaluate an arithmetic expression (supports +, -, *, /, parentheses). Example: calculator(expression=\"15 * 37 + 4\")",
		Schema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"expression": {
					"type": "string",
					"description": "The arithmetic expression to evaluate, e.g. \"2 + 3 * 4\""
				}
			},
			"required": ["expression"]
		}`),
		Policy: tools.PolicyAlwaysAllow,
	}}
}

// Execute parses the expression and returns the numeric result.
func (t *CalculatorTool) Execute(_ context.Context, input json.RawMessage) (tools.ToolResult, error) {
	var params struct {
		Expression string `json:"expression"`
	}
	if err := json.Unmarshal(input, &params); err != nil {
		return tools.ParseInputError(err)
	}
	if params.Expression == "" {
		return tools.ToolResult{Content: "validation error: expression is required", IsError: true}, nil
	}

	result, err := evaluate(params.Expression)
	if err != nil {
		return tools.ToolResult{Content: fmt.Sprintf("evaluation error: %v", err), IsError: true}, nil
	}
	return tools.ToolResult{Content: fmt.Sprintf("%s = %g", params.Expression, result)}, nil
}
