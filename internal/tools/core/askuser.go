package core

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/user/agent/internal/tools"
)

const toolAskUserDescription = "Ask the user a question and present answer options. The user can select from predefined options or provide a custom text answer. Use this when you need user input to proceed."

// AskUserTool asks the user a question and returns their answer.
type AskUserTool struct {
	*tools.BaseTool
	askFunc tools.AskUserFunc
}

// NewAskUserTool creates a new AskUserTool with the given callback function.
func NewAskUserTool(fn tools.AskUserFunc) *AskUserTool {
	return &AskUserTool{
		BaseTool: &tools.BaseTool{
			ToolName:        "ask_user",
			ToolDescription: toolAskUserDescription,
			Schema: json.RawMessage(`{
		"type": "object",
		"properties": {
			"question": {
				"type": "string",
				"description": "The question to ask the user"
			},
			"options": {
				"type": "array",
				"items": {
					"type": "object",
					"properties": {
						"label": {"type": "string", "description": "Display label for the option"},
						"value": {"type": "string", "description": "Value identifier for the option"}
					},
					"required": ["label", "value"]
				},
				"description": "Answer options to present to the user"
			},
			"multi_select": {
				"type": "boolean",
				"description": "Whether multiple options can be selected (default false = exclusive/radio)"
			},
			"recommended": {
				"type": "array",
				"items": {"type": "string"},
				"description": "Values of options to mark as recommended"
			}
		},
		"required": ["question", "options"]
	}`),
			Policy: tools.PolicyAlwaysAllow,
		},
		askFunc: fn,
	}
}

// askUserInput represents the input parameters for the ask_user tool.
type askUserInput struct {
	Question    string              `json:"question"`
	Options     []tools.AskUserOption `json:"options"`
	MultiSelect bool                `json:"multi_select"`
	Recommended []string            `json:"recommended"`
}

// Execute asks the user a question and returns the response.
func (t *AskUserTool) Execute(ctx context.Context, input json.RawMessage) (tools.ToolResult, error) {
	var params askUserInput
	if err := json.Unmarshal(input, &params); err != nil {
		return tools.ParseInputError(err)
	}

	if strings.TrimSpace(params.Question) == "" {
		return tools.ToolResult{
			Content: "validation error: question must not be empty",
			IsError: true,
		}, nil
	}

	if len(params.Options) == 0 {
		return tools.ToolResult{
			Content: "validation error: options must have at least one entry",
			IsError: true,
		}, nil
	}

	if t.askFunc == nil {
		return tools.ToolResult{
			Content: "ask_user is not available in this mode",
			IsError: true,
		}, nil
	}

	req := tools.AskUserRequest{
		Question:    params.Question,
		Options:     params.Options,
		MultiSelect: params.MultiSelect,
		Recommended: params.Recommended,
	}

	resp, err := t.askFunc(ctx, req)
	if err != nil {
		return tools.ToolResult{
			Content: fmt.Sprintf("ask_user failed: %v", err),
			IsError: true,
		}, nil
	}

	hasSelected := len(resp.Selected) > 0
	hasCustom := resp.CustomText != ""

	var content string
	switch {
	case hasSelected && hasCustom:
		content = fmt.Sprintf("User selected: %s. Additional input: %s", strings.Join(resp.Selected, ", "), resp.CustomText)
	case hasSelected:
		content = "User selected: " + strings.Join(resp.Selected, ", ")
	case hasCustom:
		content = "User answered: " + resp.CustomText
	default:
		content = "User provided no answer"
	}

	return tools.ToolResult{Content: content, IsError: false}, nil
}
