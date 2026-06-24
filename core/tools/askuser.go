package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	sdktools "github.com/v0lka/c0wrk/sdk/tools"
)

const toolAskUserDescription = `Ask the user one or more questions and present selectable answer options for each. Supports single-select (default) and multi-select modes per question. The user can pick from predefined options or provide a custom free-text answer for each question. Not available in non-interactive (CLI) mode.`

// AskUserTool asks the user a question and returns their answer.
type AskUserTool struct {
	*sdktools.BaseTool
	askFunc AskUserFunc
}

// NewAskUserTool creates a new AskUserTool with the given callback function.
func NewAskUserTool(fn AskUserFunc) *AskUserTool {
	return &AskUserTool{
		BaseTool: &sdktools.BaseTool{
			ToolName:        "ask_user",
			ToolDescription: toolAskUserDescription,
			Schema: json.RawMessage(`{
		"type": "object",
		"properties": {
			"questions": {
				"type": "array",
				"items": {
					"type": "object",
					"properties": {
						"id": {"type": "string", "description": "Unique identifier for this question (e.g. q1, q2)"},
						"question": {"type": "string", "description": "The question text"},
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
					"required": ["id", "question", "options"]
				},
				"description": "One or more questions to present to the user"
			}
		},
		"required": ["questions"]
	}`),
			Policy: sdktools.PolicyAlwaysAllow,
		},
		askFunc: fn,
	}
}

type askUserInput struct {
	Questions []AskUserQuestion `json:"questions"`
}

func (t *AskUserTool) Execute(ctx context.Context, input json.RawMessage) (sdktools.ToolResult, error) {
	var params askUserInput
	if err := json.Unmarshal(input, &params); err != nil {
		return sdktools.ParseInputError(err)
	}

	if len(params.Questions) == 0 {
		return sdktools.ToolResult{
			Content: "validation error: questions array must not be empty",
			IsError: true,
		}, nil
	}

	for _, q := range params.Questions {
		if strings.TrimSpace(q.Question) == "" {
			return sdktools.ToolResult{
				Content: fmt.Sprintf("validation error: question %q has empty text", q.ID),
				IsError: true,
			}, nil
		}
		if len(q.Options) == 0 {
			return sdktools.ToolResult{
				Content: fmt.Sprintf("validation error: question %q must have at least one option", q.ID),
				IsError: true,
			}, nil
		}
	}

	if t.askFunc == nil {
		return sdktools.ToolResult{
			Content: "ask_user is not available in this mode",
			IsError: true,
		}, nil
	}

	req := AskUserRequest(params)

	resp, err := t.askFunc(ctx, req)
	if err != nil {
		return sdktools.ToolResult{
			Content: fmt.Sprintf("ask_user failed: %v", err),
			IsError: true,
		}, nil
	}

	// Format response
	var lines []string
	for _, a := range resp.Answers {
		// Find the question text for this answer
		qText := a.ID
		for _, q := range params.Questions {
			if q.ID == a.ID {
				qText = q.Question
				break
			}
		}

		hasSelected := len(a.Selected) > 0
		hasCustom := a.CustomText != ""

		var line string
		switch {
		case hasSelected && hasCustom:
			line = fmt.Sprintf("Q %q → User selected: %s. Additional input: %s", qText, strings.Join(a.Selected, ", "), a.CustomText)
		case hasSelected:
			line = fmt.Sprintf("Q %q → User selected: %s", qText, strings.Join(a.Selected, ", "))
		case hasCustom:
			line = fmt.Sprintf("Q %q → User answered: %s", qText, a.CustomText)
		default:
			line = fmt.Sprintf("Q %q → User provided no answer", qText)
		}
		lines = append(lines, line)
	}

	return sdktools.ToolResult{Content: strings.Join(lines, "\n"), IsError: false}, nil
}
