package builtins

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/v0lka/c0wrk/sdk/agent"
	"github.com/v0lka/c0wrk/sdk/tools"
)

const toolSetStepStatusDescription = "Update the to-do checklist for the current step. Call this as your FIRST tool call to initialize the checklist, and again after completing each item (mark it as '- [x]'). Use ONLY ASCII checkboxes: '- [ ]' for unchecked, '- [x]' for checked. No nested lists, no Unicode checkboxes."

var (
	// Strict regex: line must start with "- [ ] " or "- [x] " followed by text.
	// Must NOT start with whitespace (no nesting).
	validTodoLineRe = regexp.MustCompile(`^- \[([ x])\] (.+)$`)
	// Detects lines that look like they intend to be list items but don't match the strict format.
	looseListLineRe = regexp.MustCompile(`^\s*- `)
)

// SetStepStatusTool validates and processes the step's to-do checklist.
type SetStepStatusTool struct {
	*tools.BaseTool
}

// NewSetStepStatusTool creates a new SetStepStatusTool instance.
func NewSetStepStatusTool() *SetStepStatusTool {
	return &SetStepStatusTool{BaseTool: &tools.BaseTool{
		ToolName:        "set_step_status",
		ToolDescription: toolSetStepStatusDescription,
		Schema: json.RawMessage(`{
		"type": "object",
		"properties": {
			"todo_list": {
				"type": "string",
				"description": "The to-do list as Markdown checkboxes, one per line. Example:\n- [ ] First task\n- [x] Completed task\n- [ ] Remaining task"
			}
		},
		"required": ["todo_list"]
	}`),
		Policy: tools.PolicyAlwaysAllow,
	}}
}

// SetStepStatusInput represents the input parameters for set_step_status.
type SetStepStatusInput struct {
	TodoList string `json:"todo_list"`
}

// TodoParseResult holds the outcome of parsing a to-do list.
type TodoParseResult struct {
	Items []agent.TodoItem
	Valid bool
	Error string
}

// parseAndValidateTodoList parses a Markdown to-do list with strict validation.
func parseAndValidateTodoList(input string) TodoParseResult {
	lines := strings.Split(input, "\n")
	var items []agent.TodoItem
	var errors []string
	var nonEmptyCount int

	for i, line := range lines {
		trimmed := strings.TrimRight(line, " \t\r")
		if strings.TrimSpace(trimmed) == "" {
			continue // skip blank lines
		}
		nonEmptyCount++

		// Detect nested lists (leading whitespace before '-')
		if trimmed != "" && (trimmed[0] == ' ' || trimmed[0] == '\t') {
			errors = append(errors, fmt.Sprintf("line %d: nested lists are not allowed", i+1))
			continue
		}

		// Check if it matches the strict format
		matches := validTodoLineRe.FindStringSubmatch(trimmed)
		if matches == nil {
			if looseListLineRe.MatchString(trimmed) {
				errors = append(errors, fmt.Sprintf("line %d: invalid checkbox format (must be exactly '- [ ] ' or '- [x] ')", i+1))
			} else {
				errors = append(errors, fmt.Sprintf("line %d: each non-empty line must be a checkbox item starting with '- [ ] ' or '- [x] '", i+1))
			}
			continue
		}

		checked := matches[1] == "x"
		text := matches[2]
		items = append(items, agent.TodoItem{Text: text, Checked: checked})
	}

	if nonEmptyCount == 0 {
		return TodoParseResult{Valid: false, Error: "to-do list is empty — provide at least one item"}
	}

	if len(errors) > 0 {
		return TodoParseResult{Valid: false, Error: strings.Join(errors, "; ")}
	}

	if len(items) == 0 {
		return TodoParseResult{Valid: false, Error: "to-do list is empty — provide at least one checkbox item"}
	}

	return TodoParseResult{Items: items, Valid: true}
}

// Execute validates the to-do list and emits a StepTodoUpdate event.
func (t *SetStepStatusTool) Execute(ctx context.Context, input json.RawMessage) (tools.ToolResult, error) {
	var params SetStepStatusInput
	if err := json.Unmarshal(input, &params); err != nil {
		return tools.ParseInputError(err)
	}

	result := parseAndValidateTodoList(params.TodoList)
	if !result.Valid {
		return tools.ToolResult{
			Content: fmt.Sprintf("Invalid to-do list format. %s.\n\nCorrect format example:\n- [ ] Analyze existing code\n- [ ] Implement core logic\n- [ ] Add tests\n\nRules:\n- Each line must start with '- [ ] ' or '- [x] ' (ASCII only)\n- No nested/indented lists\n- No Unicode checkboxes\n- At least one item required", result.Error),
			IsError: true,
		}, nil
	}

	stepID := agent.StepIDFromContext(ctx)
	if stepID == "" {
		// No plan step context — silently succeed (e.g., planner exploration)
		return tools.ToolResult{Content: "To-do list accepted (no active plan step)"}, nil
	}

	updateFn := agent.StepTodoUpdateFuncFromContext(ctx)
	if updateFn != nil {
		updateFn(stepID, result.Items)
	}

	completed := 0
	for _, item := range result.Items {
		if item.Checked {
			completed++
		}
	}

	return tools.ToolResult{
		Content: fmt.Sprintf("To-do list updated for step %s: %d/%d done", stepID, completed, len(result.Items)),
	}, nil
}
