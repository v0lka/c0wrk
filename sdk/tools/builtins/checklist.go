package builtins

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/v0lka/sp4rk/agent"
	"github.com/v0lka/sp4rk/tools"
)

const toolUpdateChecklistDescription = "Update the checklist for the current step (or for the task as a whole if there is no declared plan). Call this as your FIRST tool call to initialize the checklist, and again after completing each item (mark it as '- [x]'). Use ONLY ASCII checkboxes: '- [ ]' for unchecked, '- [x]' for checked. No nested lists, no Unicode checkboxes. When executing a declared plan inline (as the Conductor), pass step_id to associate the checklist with a specific plan step. Omit step_id for a standalone checklist (no declared plan) or when running as a delegated subagent (the step ID is inferred from the execution context)."

var (
	// Strict regex: line must start with "- [ ] " or "- [x] " followed by text.
	// Must NOT start with whitespace (no nesting).
	validTodoLineRe = regexp.MustCompile(`^- \[([ x])\] (.+)$`)
	// Detects lines that look like they intend to be list items but don't match the strict format.
	looseListLineRe = regexp.MustCompile(`^\s*- `)
)

// UpdateChecklistTool validates and processes a checklist update.
type UpdateChecklistTool struct {
	*tools.BaseTool
}

// NewUpdateChecklistTool creates a new UpdateChecklistTool instance.
func NewUpdateChecklistTool() *UpdateChecklistTool {
	return &UpdateChecklistTool{BaseTool: &tools.BaseTool{
		ToolName:        "update_checklist",
		ToolDescription: toolUpdateChecklistDescription,
		Schema: json.RawMessage(`{
		"type": "object",
		"properties": {
			"todo_list": {
				"type": "string",
				"description": "The checklist as Markdown checkboxes, one per line. Example:\n- [ ] First task\n- [x] Completed task\n- [ ] Remaining task"
			},
			"step_id": {
				"type": "string",
				"description": "Optional: the plan step ID this checklist belongs to. Pass this when executing a declared plan inline (as the Conductor) to track which plan step you are working on. Omit for a standalone checklist or when running as a delegated subagent (inferred from context)."
			}
		},
		"required": ["todo_list"]
	}`),
		Policy: tools.PolicyAlwaysAllow,
	}}
}

// UpdateChecklistInput represents the input parameters for update_checklist.
type UpdateChecklistInput struct {
	TodoList string `json:"todo_list"`
	StepID   string `json:"step_id,omitempty"`
}

// todoParseResult holds the outcome of parsing a checklist.
type todoParseResult struct {
	Items []agent.TodoItem
	Valid bool
	Error string
}

// parseAndValidateTodoList parses a Markdown checklist with strict validation.
func parseAndValidateTodoList(input string) todoParseResult {
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
		return todoParseResult{Valid: false, Error: "checklist is empty — provide at least one item"}
	}

	if len(errors) > 0 {
		return todoParseResult{Valid: false, Error: strings.Join(errors, "; ")}
	}

	if len(items) == 0 {
		return todoParseResult{Valid: false, Error: "checklist is empty — provide at least one checkbox item"}
	}

	return todoParseResult{Items: items, Valid: true}
}

// Execute validates the checklist and emits a StepTodoUpdate event.
// The stepID may be empty for a standalone checklist (Conductor without a
// declared plan); in that case the update is still emitted so the UI can
// render a standalone checklist card.
func (t *UpdateChecklistTool) Execute(ctx context.Context, input json.RawMessage) (tools.ToolResult, error) {
	var params UpdateChecklistInput
	if err := json.Unmarshal(input, &params); err != nil {
		return tools.ParseInputError(err)
	}

	result := parseAndValidateTodoList(params.TodoList)
	if !result.Valid {
		return tools.ToolResult{
			Content: fmt.Sprintf("Invalid checklist format. %s.\n\nCorrect format example:\n- [ ] Analyze existing code\n- [ ] Implement core logic\n- [ ] Add tests\n\nRules:\n- Each line must start with '- [ ] ' or '- [x] ' (ASCII only)\n- No nested/indented lists\n- No Unicode checkboxes\n- At least one item required", result.Error),
			IsError: true,
		}, nil
	}

	stepID := params.StepID
	if stepID == "" {
		stepID = agent.StepIDFromContext(ctx)
	}

	// Guard: reject calls that violate plan/checklist invariants (e.g. a
	// standalone checklist when a plan has been declared).
	if guard := agent.ChecklistGuardFromContext(ctx); guard != nil {
		if msg := guard(stepID); msg != "" {
			return tools.ToolResult{
				Content: msg,
				IsError: true,
			}, nil
		}
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

	total := len(result.Items)
	suffix := ""
	if completed < total {
		suffix = " Remember to call update_checklist again after completing the next item — update it incrementally, not all at once."
	}

	if stepID == "" {
		return tools.ToolResult{
			Content: fmt.Sprintf("Checklist updated: %d/%d done.%s", completed, total, suffix),
		}, nil
	}

	return tools.ToolResult{
		Content: fmt.Sprintf("Checklist updated for step %s: %d/%d done.%s", stepID, completed, total, suffix),
	}, nil
}
