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

const evidenceToolDescription = "Read evidence from the blackboard: fetch a full step result by ID, search across results, or list all available step summaries"

// evidenceTool implements tools.Tool for reading blackboard evidence.
type evidenceTool struct {
	*tools.BaseTool
}

// NewEvidenceTool creates a new read_evidence tool instance.
func NewEvidenceTool() *evidenceTool {
	schema := `{
	"type": "object",
	"properties": {
		"step_id": {
			"type": "string",
			"description": "Fetch the full result for a specific step ID"
		},
		"query": {
			"type": "string",
			"description": "Search across all step results for matching entries"
		},
		"list": {
			"type": "boolean",
			"description": "List all available step summaries (default behavior when no other params given)"
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
	StepID string `json:"step_id"`
	Query  string `json:"query"`
	List   bool   `json:"list"`
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
	default:
		// list=true or no params at all → list summaries
		return t.listSteps(bb)
	}
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

	fmt.Fprintf(&sb, "### Output\n%s\n", result.FullOutput)

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
