package coretools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	tools "github.com/user/agent/sdk/tools"
)

const verdictToolDescription = `Record a pass/fail verdict for a single acceptance criterion. Call this tool exactly once per criterion being evaluated. Use YES when the criterion is demonstrably met based on evidence, NO when it is not met or evidence is insufficient. The explanation must cite specific evidence (tool outputs, file contents, test results) that supports the verdict.`

// verdictTool implements tools.Tool for recording evaluation verdicts.
type verdictTool struct {
	*tools.BaseTool
}

// NewVerdictTool creates a new report_verdict tool instance.
func NewVerdictTool() tools.Tool {
	schema := `{
	"type": "object",
	"properties": {
		"criterion_id": {
			"type": "string",
			"description": "The exact criterion ID being evaluated, e.g. \"ac_1\". Must match a criterion from the task."
		},
		"verdict": {
			"type": "string",
			"description": "YES if the criterion is met based on evidence, NO if it is not met or evidence is insufficient.",
			"enum": ["YES", "NO"]
		},
		"explanation": {
			"type": "string",
			"description": "Brief justification citing specific evidence: tool outputs, file contents, or test results that support the verdict."
		}
	},
	"required": ["criterion_id", "verdict", "explanation"]
}`
	return &verdictTool{
		BaseTool: &tools.BaseTool{
			ToolName:        "report_verdict",
			ToolDescription: verdictToolDescription,
			Schema:          json.RawMessage(schema),
			Policy:          tools.PolicyAlwaysAllow,
		},
	}
}

type verdictInput struct {
	CriterionID string `json:"criterion_id"`
	Verdict     string `json:"verdict"`
	Explanation string `json:"explanation"`
}

func (t *verdictTool) Execute(ctx context.Context, input json.RawMessage) (tools.ToolResult, error) {
	bb := BlackboardFromContext(ctx)
	if bb == nil {
		return tools.ToolResult{
			Content: "blackboard not available in context",
			IsError: true,
		}, nil
	}

	var params verdictInput
	if err := json.Unmarshal(input, &params); err != nil {
		return tools.ParseInputError(err)
	}

	if params.CriterionID == "" {
		return tools.ToolResult{Content: "missing required parameter: criterion_id", IsError: true}, nil
	}
	if params.Verdict == "" {
		return tools.ToolResult{Content: "missing required parameter: verdict", IsError: true}, nil
	}
	if params.Explanation == "" {
		return tools.ToolResult{Content: "missing required parameter: explanation", IsError: true}, nil
	}

	// Normalize and validate verdict
	verdict := strings.ToUpper(strings.TrimSpace(params.Verdict))
	if verdict != "YES" && verdict != "NO" {
		return tools.ToolResult{
			Content: fmt.Sprintf("invalid verdict %q: must be YES or NO", params.Verdict),
			IsError: true,
		}, nil
	}

	bb.SetEvalVerdict(params.CriterionID, verdict, params.Explanation)

	return tools.ToolResult{
		Content: fmt.Sprintf("Verdict recorded for %s: %s", params.CriterionID, verdict),
	}, nil
}

// VerdictToolDescriptor returns a ToolDescriptor for the report_verdict tool.
func VerdictToolDescriptor() tools.ToolDescriptor {
	t := NewVerdictTool()
	return tools.ToolDescriptor{
		Name:        t.Name(),
		Description: t.Description(),
		InputSchema: t.InputSchema(),
		Source:      "core",
	}
}
