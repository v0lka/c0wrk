package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	sdktools "github.com/v0lka/sp4rk/tools"
)

const toolProposeGoalDescription = `Propose a goal — a {condition, verify} pair — for user sign-off before committing to it. Use this when the derivation agent has crystallized a goal from the user's request. The call blocks until the user approves (optionally with edits), provides a clarification, or cancels. Returns the user's decision: the approved condition/verify (which may differ from the proposal if the user edited them), a clarification string if the user asked for more detail, or an error message if the user cancelled.`

// GoalProposal is the {condition, verify} pair the agent submits for sign-off.
// Clarification is an optional question the agent includes when it needs
// disambiguation from the user; NeedsClarification flags that intent.
type GoalProposal struct {
	Condition          string `json:"condition"`
	Verify             string `json:"verify"`
	Clarification      string `json:"clarification,omitempty"`
	NeedsClarification bool   `json:"needs_clarification,omitempty"`
}

// GoalProposalResponse is the user's decision on a proposed goal.
//
// Decision is one of:
//   - "approve":           the user accepted the goal. Condition/Verify carry
//     the (possibly edited) approved values.
//   - "clarify":           the user answered the agent's clarification request
//     (or asked their own). Clarification holds the answer.
//   - "cancel":            the user rejected the goal outright.
type GoalProposalResponse struct {
	Decision      string `json:"decision"`                // "approve", "clarify", "cancel"
	Condition     string `json:"condition,omitempty"`     // set when Decision == "approve" (edited values)
	Verify        string `json:"verify,omitempty"`        // set when Decision == "approve" (edited values)
	Clarification string `json:"clarification,omitempty"` // set when Decision == "clarify"
}

// GoalProposer is the backend hook that submits a goal proposal to the user and
// blocks until the user responds. The implementation (which emits the
// goal_proposal event and waits on a pending-confirmation channel) lives in the
// backend layer; the tool only knows the interface.
type GoalProposer interface {
	Propose(ctx context.Context, proposal GoalProposal) (GoalProposalResponse, error)
}

// ProposeGoalTool submits a {condition, verify} goal proposal and blocks for
// user approval. It is an internal tool (PolicyAlwaysAllow, bypasses the tool
// judge) because it is a coordination primitive, not a user-facing capability.
type ProposeGoalTool struct {
	*sdktools.BaseTool
}

// NewProposeGoalTool creates the propose_goal tool.
func NewProposeGoalTool() *ProposeGoalTool {
	return &ProposeGoalTool{
		BaseTool: &sdktools.BaseTool{
			ToolName:        "propose_goal",
			ToolDescription: toolProposeGoalDescription,
			Schema: json.RawMessage(`{
	"type": "object",
	"properties": {
		"condition": {
			"type": "string",
			"description": "The success condition — a declarative statement of what 'done' means for this task. Must be specific and verifiable."
		},
		"verify": {
			"type": "string",
			"description": "The verification clause — how the agent will prove the condition is met (e.g. 'all tests in package X pass', 'the endpoint returns 200 with body matching schema Y')."
		},
		"clarification": {
			"type": "string",
			"description": "Optional. A question for the user when the goal cannot be fully determined from the request. Pair with needs_clarification=true."
		},
		"needs_clarification": {
			"type": "boolean",
			"description": "If true, the call signals that the agent needs user input to finalize the goal. The user's reply is returned in the response. Default false (goal is ready for approve/cancel)."
		}
	},
	"required": ["condition", "verify"]
}`),
			Policy: sdktools.PolicyAlwaysAllow,
		},
	}
}

type proposeGoalInput struct {
	Condition          string `json:"condition"`
	Verify             string `json:"verify"`
	Clarification      string `json:"clarification"`
	NeedsClarification bool   `json:"needs_clarification"`
}

func (t *ProposeGoalTool) Execute(ctx context.Context, input json.RawMessage) (sdktools.ToolResult, error) {
	var params proposeGoalInput
	if err := json.Unmarshal(input, &params); err != nil {
		return sdktools.ParseInputError(err)
	}

	if strings.TrimSpace(params.Condition) == "" {
		return sdktools.ErrorResult("validation error: condition must not be empty"), nil
	}
	if strings.TrimSpace(params.Verify) == "" {
		return sdktools.ErrorResult("validation error: verify must not be empty"), nil
	}

	proposer := GoalProposerFrom(ctx)
	if proposer == nil {
		return sdktools.ErrorResult("propose_goal: no goal proposer in context (not running inside a Conductor)"), nil
	}

	resp, err := proposer.Propose(ctx, GoalProposal(params))
	if err != nil {
		return sdktools.ErrorResult("propose_goal: proposer failed: %v", err), nil
	}

	switch resp.Decision {
	case "approve":
		// If the user edited the condition/verify, echo the approved values so
		// the agent commits to the user's wording rather than its own.
		if resp.Condition != "" && resp.Condition != params.Condition {
			return sdktools.ToolResult{
				Content: fmt.Sprintf("Goal approved with edits. Condition: %s | Verify: %s", resp.Condition, coalesce(resp.Verify, params.Verify)),
			}, nil
		}
		return sdktools.ToolResult{
			Content: fmt.Sprintf("Goal approved by user. Condition: %s | Verify: %s", params.Condition, params.Verify),
		}, nil
	case "clarify":
		msg := "User provided a clarification."
		if resp.Clarification != "" {
			msg += " " + resp.Clarification
		}
		msg += "\n\nRevise the goal and call propose_goal again with the updated condition/verify."
		return sdktools.ToolResult{Content: msg}, nil
	case "cancel":
		return sdktools.ToolResult{
			Content: "User cancelled the goal proposal. Do not proceed with this goal unless the user gives new instructions.",
			IsError: true,
		}, nil
	default:
		return sdktools.ToolResult{
			Content: fmt.Sprintf("Goal proposer returned unknown decision %q; treating as cancel.", resp.Decision),
			IsError: true,
		}, nil
	}
}

// coalesce returns the first non-empty string.
func coalesce(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// --- Context plumbing ---

type goalProposerKey struct{}

// WithGoalProposer injects the goal proposer into the context.
func WithGoalProposer(ctx context.Context, proposer GoalProposer) context.Context {
	return context.WithValue(ctx, goalProposerKey{}, proposer)
}

// GoalProposerFrom extracts the goal proposer from the context, or returns nil.
func GoalProposerFrom(ctx context.Context) GoalProposer {
	if v, ok := ctx.Value(goalProposerKey{}).(GoalProposer); ok {
		return v
	}
	return nil
}
