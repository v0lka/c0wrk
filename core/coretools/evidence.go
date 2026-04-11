package coretools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/user/agent/core"
	tools "github.com/user/agent/sdk/tools"
)

// ---------------------------------------------------------------------------
// Context helpers — attach/retrieve Blackboard via context.Context
// These delegate to core package helpers so the context key is shared.
// ---------------------------------------------------------------------------

// WithBlackboard returns a context with the blackboard attached.
func WithBlackboard(ctx context.Context, bb core.Blackboard) context.Context {
	return core.WithBlackboard(ctx, bb)
}

// BlackboardFromContext retrieves the blackboard from context.
// Returns nil if no blackboard is present.
func BlackboardFromContext(ctx context.Context) core.Blackboard {
	return core.BlackboardFromContext(ctx)
}

// ---------------------------------------------------------------------------
// evidenceTool — read_evidence tool implementation
// ---------------------------------------------------------------------------

const evidenceToolDescription = `Read execution evidence from the blackboard. Exactly one mode is active per call, resolved in priority order: step_id (fetch full step result) > query (search across all results) > file_changes (list all file modifications) > file_diff (get unified diff for a file) > list (default: show all step summaries). Use this to gather concrete evidence before reporting a verdict.`

// evidenceTool implements tools.Tool for reading blackboard evidence.
type evidenceTool struct {
	*tools.BaseTool
}

// NewEvidenceTool creates a new read_evidence tool instance.
func NewEvidenceTool() tools.Tool {
	schema := `{
	"type": "object",
	"properties": {
		"step_id": {
			"type": "string",
			"description": "Fetch the full result for a specific step by its ID, e.g. \"step_1\". Takes priority over other parameters."
		},
		"query": {
			"type": "string",
			"description": "Search keyword or phrase to find across all step results. Ignored if step_id is provided."
		},
		"list": {
			"type": "boolean",
			"description": "Set to true to list all available step summaries. This is the default when no other parameters are given."
		},
		"file_changes": {
			"type": "boolean",
			"description": "Set to true to list all file changes (creates, modifications, deletions) made during task execution."
		},
		"file_diff": {
			"type": "string",
			"description": "Absolute file path to get a unified diff for, e.g. \"/path/to/file.go\". Shows what changed in that specific file."
		}
	}
}`
	return &evidenceTool{
		BaseTool: &tools.BaseTool{
			ToolName:        "read_evidence",
			ToolDescription: evidenceToolDescription,
			Schema:          json.RawMessage(schema),
			Policy:          tools.PolicyAlwaysAllow,
		},
	}
}

// evidenceInput represents the input parameters for the evidence tool.
type evidenceInput struct {
	StepID      string `json:"step_id"`
	Query       string `json:"query"`
	List        bool   `json:"list"`
	FileChanges bool   `json:"file_changes"`
	FileDiff    string `json:"file_diff"`
}

// Execute reads evidence from the blackboard based on the input parameters.
func (t *evidenceTool) Execute(ctx context.Context, input json.RawMessage) (tools.ToolResult, error) {
	bb := BlackboardFromContext(ctx)
	if bb == nil {
		return tools.ToolResult{
			Content: "blackboard not available in context",
			IsError: true,
		}, nil
	}

	var params evidenceInput
	if err := json.Unmarshal(input, &params); err != nil {
		return tools.ParseInputError(err)
	}

	// Dispatch based on which parameter is provided.
	switch {
	case params.StepID != "":
		return t.fetchStep(bb, params.StepID)
	case params.Query != "":
		return t.searchSteps(bb, params.Query)
	case params.FileChanges:
		return t.listFileChanges(bb)
	case params.FileDiff != "":
		return t.getFileDiff(bb, params.FileDiff)
	default:
		// list=true or no params at all → list summaries
		return t.listSteps(bb)
	}
}

// maxTraceFieldLen is the maximum length for Input/Observation fields in tool
// call traces before truncation.
const maxTraceFieldLen = 500

// truncateTrace truncates s to maxLen characters, appending a suffix if trimmed.
func truncateTrace(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "...(truncated)"
}

// fetchStep returns the full output and formatted steps for a specific step ID.
func (t *evidenceTool) fetchStep(bb core.Blackboard, stepID string) (tools.ToolResult, error) {
	result, ok := bb.GetStepResult(stepID)
	if !ok {
		return tools.ToolResult{
			Content: fmt.Sprintf("no result found for step %q", stepID),
			IsError: false,
		}, nil
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "## Step: %s\n\n", result.StepID)

	if result.Error != nil {
		fmt.Fprintf(&sb, "**Error:** %v\n\n", result.Error)
	}

	// Include tool call trace when steps are available.
	if len(result.Steps) > 0 {
		fmt.Fprintf(&sb, "### Execution Trace (%d tool calls)\n\n", len(result.Steps))
		for i, step := range result.Steps {
			fmt.Fprintf(&sb, "**%d.** Thought: %s\n", i+1, step.Thought)
			if step.Action.Name != "" {
				fmt.Fprintf(&sb, "- Tool: %s\n", step.Action.Name)
				if len(step.Action.Input) > 0 {
					fmt.Fprintf(&sb, "- Input: %s\n", truncateTrace(string(step.Action.Input), maxTraceFieldLen))
				}
			}
			if step.Observation != "" {
				fmt.Fprintf(&sb, "- Result: %s\n", truncateTrace(step.Observation, maxTraceFieldLen))
			}
			sb.WriteString("\n")
		}
	}

	fmt.Fprintf(&sb, "### Final Output\n%s\n", result.FullOutput)

	return tools.ToolResult{Content: sb.String(), IsError: false}, nil
}

// searchSteps searches the blackboard and returns matching entries.
func (t *evidenceTool) searchSteps(bb core.Blackboard, query string) (tools.ToolResult, error) {
	entries := bb.Search(query)
	if len(entries) == 0 {
		return tools.ToolResult{
			Content: fmt.Sprintf("no matches found for query %q", query),
			IsError: false,
		}, nil
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "Found %d match(es) for %q:\n\n", len(entries), query)
	for i, e := range entries {
		fmt.Fprintf(&sb, "%d. [%s] %s — %s\n", i+1, e.Type, e.Key, e.Summary)
	}
	return tools.ToolResult{Content: sb.String(), IsError: false}, nil
}

// listSteps returns a compact list of all step summaries.
func (t *evidenceTool) listSteps(bb core.Blackboard) (tools.ToolResult, error) {
	all := bb.GetAllStepResults()
	if len(all) == 0 {
		return tools.ToolResult{
			Content: "no step results available",
			IsError: false,
		}, nil
	}

	// Sort keys for deterministic output.
	keys := make([]string, 0, len(all))
	for k := range all {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var sb strings.Builder
	fmt.Fprintf(&sb, "Available steps (%d):\n\n", len(all))
	for _, k := range keys {
		sr := all[k]
		status := "ok"
		if sr.Error != nil {
			status = "error"
		}
		fmt.Fprintf(&sb, "- %s [%s]: %s\n", sr.StepID, status, sr.Summary)
	}
	return tools.ToolResult{Content: sb.String(), IsError: false}, nil
}

// listFileChanges returns a formatted summary of all file changes in the session.
func (t *evidenceTool) listFileChanges(bb core.Blackboard) (tools.ToolResult, error) {
	changes := bb.GetSessionFileChanges()
	if len(changes) == 0 {
		return tools.ToolResult{Content: "No file changes recorded"}, nil
	}

	var b strings.Builder
	b.WriteString("## File Changes Summary\n\n")

	var created, modified, deleted int
	for _, fc := range changes {
		switch fc.Operation {
		case "CREATE":
			created++
			fmt.Fprintf(&b, "- **CREATE**: %s (%d bytes)\n", fc.Path, fc.SizeBytes)
		case "MODIFY":
			modified++
			added, removed := countDiffLines(fc.Diff)
			fmt.Fprintf(&b, "- **MODIFY**: %s (+%d -%d lines)\n", fc.Path, added, removed)
		case "DELETE":
			deleted++
			fmt.Fprintf(&b, "- **DELETE**: %s\n", fc.Path)
		}
	}

	fmt.Fprintf(&b, "\nTotal: %d created, %d modified, %d deleted\n", created, modified, deleted)

	return tools.ToolResult{Content: b.String()}, nil
}

// getFileDiff returns the diff for a specific file path.
func (t *evidenceTool) getFileDiff(bb core.Blackboard, path string) (tools.ToolResult, error) {
	changes := bb.GetSessionFileChanges()

	for _, fc := range changes {
		if fc.Path == path {
			var b strings.Builder
			fmt.Fprintf(&b, "## Diff: %s (%s)\n\n", fc.Path, fc.Operation)

			switch fc.Operation {
			case "MODIFY":
				if fc.Diff != "" {
					b.WriteString("```diff\n")
					b.WriteString(fc.Diff)
					b.WriteString("\n```\n")
				}
			case "CREATE":
				fmt.Fprintf(&b, "New file created (%d bytes)\n", fc.SizeBytes)
			case "DELETE":
				b.WriteString("File was deleted\n")
			}

			return tools.ToolResult{Content: b.String()}, nil
		}
	}

	return tools.ErrorResult("No changes found for file: %s", path), nil
}

// countDiffLines counts added and removed lines in a unified diff.
func countDiffLines(diff string) (added, removed int) {
	for _, line := range strings.Split(diff, "\n") {
		if strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++") {
			added++
		} else if strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---") {
			removed++
		}
	}
	return
}

// ---------------------------------------------------------------------------
// Descriptor
// ---------------------------------------------------------------------------

// EvidenceToolDescriptor returns a ToolDescriptor for the read_evidence tool
// (metadata only, no execution capability).
func EvidenceToolDescriptor() tools.ToolDescriptor {
	t := NewEvidenceTool()
	return tools.ToolDescriptor{
		Name:        t.Name(),
		Description: t.Description(),
		InputSchema: t.InputSchema(),
		Source:      "core",
	}
}
