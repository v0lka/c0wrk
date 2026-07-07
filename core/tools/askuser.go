package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	sdktools "github.com/v0lka/sp4rk/tools"
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
						"id": {"type": "string", "description": "Unique non-empty identifier for this question (e.g. q1, q2). Must be unique across all questions in this call."},
						"question": {"type": "string", "description": "The question text"},
						"options": {
							"type": "array",
							"items": {
								"type": "object",
								"properties": {
									"label": {"type": "string", "description": "Display label for the option"},
									"value": {"type": "string", "description": "Unique non-empty value identifier for the option. Must be unique within the question — do NOT reuse the same value across options."}
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

	seenIDs := make(map[string]struct{}, len(params.Questions))
	for _, q := range params.Questions {
		if strings.TrimSpace(q.ID) == "" {
			return sdktools.ToolResult{
				Content: "validation error: question has empty id",
				IsError: true,
			}, nil
		}
		if _, dup := seenIDs[q.ID]; dup {
			return sdktools.ToolResult{
				Content: fmt.Sprintf("validation error: duplicate question id %q", q.ID),
				IsError: true,
			}, nil
		}
		seenIDs[q.ID] = struct{}{}

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
		seenValues := make(map[string]struct{}, len(q.Options))
		for _, opt := range q.Options {
			if strings.TrimSpace(opt.Label) == "" {
				return sdktools.ToolResult{
					Content: fmt.Sprintf("validation error: question %q has an option with empty label", q.ID),
					IsError: true,
				}, nil
			}
			if strings.TrimSpace(opt.Value) == "" {
				return sdktools.ToolResult{
					Content: fmt.Sprintf("validation error: question %q option %q has empty value", q.ID, opt.Label),
					IsError: true,
				}, nil
			}
			if _, dup := seenValues[opt.Value]; dup {
				return sdktools.ToolResult{
					Content: fmt.Sprintf("validation error: question %q has duplicate option value %q", q.ID, opt.Value),
					IsError: true,
				}, nil
			}
			seenValues[opt.Value] = struct{}{}
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
