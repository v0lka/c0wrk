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

const toolDeclareGoalStatusDescription = `Declare your self-evaluation verdict on whether the active goal has been reached. Use this after you have VERIFIED the goal — not merely after you believe you have done the work. Execute the verify clause (running any command it names) and cite its real exit code / output as evidence before declaring "met"; do not declare the goal met from an assumption that a check would pass. This is the single channel through which the goal loop learns your structured verdict {status, evidence, reason}.

status is one of:
  - "met":      you have satisfied the goal's success condition. REQUIRES concrete evidence (file paths changed, test output, command results). A bare "done" without evidence fails evaluation.
  - "not_met":  you have made progress but the condition is not yet satisfied; continue working.
  - "blocked":  you cannot make further progress without external input or a changed situation.

Each evidence entry points at something concrete the user (or a verifier) can inspect: a file path, a test/command and its output, or a qualitative note with a clear ref.`

// GoalStatusSink is the context-injected destination for self-evaluation
// verdicts. The goal loop (runGoalLoop) injects a concrete sink into the
// per-turn context; declare_goal_status writes structured verdicts into it so
// the loop reads them as typed objects rather than parsing ToolResult.Content
// (which is a plain string, and PostExecuteHook skips internal tools anyway).
//
// The implementation lives in a higher layer; the tool only knows the interface.
type GoalStatusSink interface {
	// Declare records a verdict as the most recent one. Implementations must
	// be safe for the single-threaded per-turn tool call that invokes this.
	Declare(v goal.Verdict)
	// Last returns the most recently declared verdict, or nil if none has been
	// declared since the sink was created/injected.
	Last() *goal.Verdict
}

// DeclareGoalStatusTool captures a self-evaluation verdict via the
// context-injected GoalStatusSink. It is an internal tool
// (PolicyAlwaysAllow, bypasses the tool judge) because it is a coordination
// primitive for the goal loop, not a user-facing capability. It is a no-op
// outside a goal-loop run (the sink will be nil), matching declare_plan's and
// propose_goal's pattern.
type DeclareGoalStatusTool struct {
	*sdktools.BaseTool
}

// NewDeclareGoalStatusTool creates the declare_goal_status tool.
func NewDeclareGoalStatusTool() *DeclareGoalStatusTool {
	return &DeclareGoalStatusTool{
		BaseTool: &sdktools.BaseTool{
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
		// valid
	default:
		return sdktools.ErrorResult("validation error: status must be \"met\", \"not_met\", or \"blocked\", got %q", status), nil
	}

	// Evidence mandate: declaring the goal met REQUIRES non-empty evidence.
	// Enforced at the tool boundary so a bare "done" can never terminate the
	// goal loop without a concrete, inspectable artifact backing it.
	//
	// The tool executor does NOT validate inputs against the JSON schema, so a
	// `met` verdict must be rejected both when evidence is absent AND when an
	// entry is present but empty (e.g. evidence:[{}] or evidence:[{"ref":""}]) —
	// otherwise a bare assertion could terminate the loop with no real artifact.
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

	// Short confirmation string. The structured verdict is delivered via the
	// sink; this Content is just for the transcript so the agent sees its
	// declaration was captured.
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
