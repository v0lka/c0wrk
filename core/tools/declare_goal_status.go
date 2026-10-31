package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/v0lka/c0wrk/core/goal"
	sdktools "github.com/v0lka/sp4rk/tools"
)

const toolDeclareGoalStatusDescription = `Purpose: declare your verdict on the active goal and END the current turn.
Use when: at the end of this turn's bounded attempt — exactly once per turn. Calling this tool ends your turn: the goal loop reads the verdict, charges the turn budget, and starts the next turn with the accumulated context. Report your own assessment only; do NOT run the goal's verification yourself — an independent verifier re-checks every "met" claim after you declare it. Make no tool calls after this one.
Inputs: status ("met" | "not_met" | "blocked"); reason (narrative: what was done, what remains, or what blocks); evidence (array of {type, ref, summary} artifacts — REQUIRED when status="met").
Outputs: the verdict is recorded and the current turn ends.
Example: status "met" with evidence citing the artifacts your work produced (changed files, command output you observed).
Anti-example: not for intermediate progress (report in replies); never more than once per turn; never as your own verification pass; "not_met" means keep working next turn, "blocked" means external input is required.`

// GoalStatusSink is the context-injected destination for structured
// self-evaluation verdicts (typed goal.Verdict). See specs/domains/goal-mode.md.
type GoalStatusSink interface {
	// Declare records the verdict as the most recent one.
	Declare(v goal.Verdict)
	// Last returns the most recently declared verdict, or nil if none has been
	// declared since the sink was created/injected.
	Last() *goal.Verdict
}

// DeclareGoalStatusTool captures the agent's self-evaluation verdict via the
// context-injected GoalStatusSink — a goal-loop coordination primitive; outside
// a goal loop (nil sink) it returns an error. See specs/domains/goal-mode.md.
type DeclareGoalStatusTool struct {
	*sdktools.BaseTool
}

// NewDeclareGoalStatusTool creates the declare_goal_status tool.
func NewDeclareGoalStatusTool() *DeclareGoalStatusTool {
	return &DeclareGoalStatusTool{
		BaseTool: &sdktools.BaseTool{
			ToolGroup:       sdktools.GroupSystem,
			ToolName:        "declare_goal_status",
			ToolDescription: toolDeclareGoalStatusDescription,
			Schema: json.RawMessage(`{
	"type": "object",
	"properties": {
		"status": {
			"type": "string",
			"enum": ["met", "not_met", "blocked"],
			"description": "Your verdict on the goal. \"met\" requires concrete evidence; \"not_met\" means keep working; \"blocked\" means you need external input or a changed situation to proceed."
		},
		"evidence": {
			"type": "array",
			"description": "Concrete artifacts backing the verdict. REQUIRED when status is \"met\". Each entry is {type, ref, summary}: type is test_output|file|command|qualitative; ref is the artifact reference (path, command, id, or note); summary explains what the artifact shows.",
			"items": {
				"type": "object",
				"properties": {
					"type": {"type": "string", "description": "test_output | file | command | qualitative"},
					"ref": {"type": "string", "description": "Artifact reference: a file path, a command string, a test id, or a free-text note for qualitative evidence."},
					"summary": {"type": "string", "description": "Human-readable description of what this evidence shows."}
				},
				"required": ["type", "ref", "summary"]
			}
		},
		"reason": {
			"type": "string",
			"description": "Narrative explanation of the verdict: what was done, what remains, or what is blocking progress."
		}
	},
	"required": ["status", "reason"]
}`),
			Policy: sdktools.PolicyAlwaysAllow,
		},
	}
}

type declareGoalStatusInput struct {
	Status   string              `json:"status"`
	Evidence []goal.GoalEvidence `json:"evidence"`
	Reason   string              `json:"reason"`
}

func (t *DeclareGoalStatusTool) Execute(ctx context.Context, input json.RawMessage) (sdktools.ToolResult, error) {
	var params declareGoalStatusInput
	if err := json.Unmarshal(input, &params); err != nil {
		return sdktools.ParseInputError(err)
	}

	status := params.Status
	switch status {
	case "met", "not_met", "blocked":
	default:
		return sdktools.ErrorResult("validation error: status must be \"met\", \"not_met\", or \"blocked\", got %q", status), nil
	}

	// Evidence mandate: a "met" verdict requires complete evidence entries;
	// rejected here (the executor skips JSON-schema validation) so absent arrays
	// and present-but-empty entries fail alike — see specs/domains/goal-mode.md.
	if status == "met" {
		if len(params.Evidence) == 0 {
			return sdktools.ToolResult{
				Content: "validation error: declaring status \"met\" requires concrete evidence. Cite at least one artifact — a changed file path, test output, or command result — as evidence. A bare \"done\" without evidence fails evaluation. Call declare_goal_status again with evidence, or use \"not_met\"/\"blocked\" if the goal is not actually satisfied.",
				IsError: true,
			}, nil
		}
		for i, ev := range params.Evidence {
			if strings.TrimSpace(ev.Type) == "" || strings.TrimSpace(ev.Ref) == "" || strings.TrimSpace(ev.Summary) == "" {
				return sdktools.ErrorResult(
					"validation error: evidence[%d] is incomplete — each evidence entry needs non-empty type, ref, and summary. A met verdict must cite a concrete, inspectable artifact (e.g. {\"type\":\"file\",\"ref\":\"path/to/file\",\"summary\":\"...\"}).",
					i,
				), nil
			}
		}
	}

	sink := GoalStatusSinkFrom(ctx)
	if sink == nil {
		return sdktools.ErrorResult("declare_goal_status: no goal status sink in context (not running inside a goal loop)"), nil
	}

	verdict := goal.Verdict{
		Status:     status,
		Evidence:   params.Evidence,
		Reason:     params.Reason,
		DeclaredAt: time.Now(),
	}
	sink.Declare(verdict)

	// Transcript-only Content — the verdict itself travels via the sink.
	var summary string
	switch status {
	case "met":
		summary = fmt.Sprintf("Verdict recorded: goal MET with %d evidence item(s). %s", len(params.Evidence), strings.TrimSpace(params.Reason))
	case "not_met":
		summary = "Verdict recorded: goal NOT YET MET. Continue working toward the condition."
	case "blocked":
		summary = "Verdict recorded: BLOCKED. Awaiting external input or a changed situation."
	}
	return sdktools.ToolResult{Content: summary}, nil
}

// --- Context plumbing ---

type goalStatusSinkKey struct{}

// WithGoalStatusSink injects the goal status sink into the context.
func WithGoalStatusSink(ctx context.Context, sink GoalStatusSink) context.Context {
	return context.WithValue(ctx, goalStatusSinkKey{}, sink)
}

// GoalStatusSinkFrom extracts the goal status sink from the context, or returns nil.
func GoalStatusSinkFrom(ctx context.Context) GoalStatusSink {
	if v, ok := ctx.Value(goalStatusSinkKey{}).(GoalStatusSink); ok {
		return v
	}
	return nil
}
