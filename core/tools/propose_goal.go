package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/v0lka/c0wrk/core/goal"
	sdktools "github.com/v0lka/sp4rk/tools"
)

const toolProposeGoalDescription = `Purpose: propose a session goal — the verifiable success condition the agent will pursue and be measured against.
Use when: at the start of a substantial task whose desired end state can be phrased as a checkable condition (tests, build, grep). Skip for trivial or exploratory asks.
Inputs: condition (one precise sentence, verifiable after implementation); verify (how you will prove it — a runnable check); verification_mode (optional: "executable" | "re_derivation", default executable).
Outputs: a pending goal proposal confirmed or declined by the user; once confirmed it drives the loop.
Example: condition "go test ./... passes for package core", verify "run go test ./core and check exit 0", before a bug-fix task.
Anti-example: no vague conditions ("improve the code") — unverifiable; do not re-propose mid-task (declare_goal_status is the update path); not for read-only questions.`

// GoalProposal is the {condition, verify} pair the agent submits for sign-off;
// an omitted VerificationMode defaults to goal.VerificationModeExecutable and
// the sign-off response round-trips user edits. See specs/domains/goal-mode.md.
type GoalProposal struct {
	Condition        string `json:"condition"`
	Verify           string `json:"verify"`
	VerificationMode string `json:"verification_mode,omitempty"` // default: goal.VerificationModeExecutable
}

// GoalProposalResponse is the user's decision on a proposed goal.
type GoalProposalResponse struct {
	Decision         string `json:"decision"`                    // "approve", "cancel"
	Condition        string `json:"condition,omitempty"`         // set when Decision == "approve" (edited values)
	Verify           string `json:"verify,omitempty"`            // set when Decision == "approve" (edited values)
	VerificationMode string `json:"verification_mode,omitempty"` // set when Decision == "approve" (edited value); echoes proposal when unchanged
}

// GoalProposer submits a goal proposal to the user and blocks until they
// respond (goal_proposal event). See specs/domains/goal-mode.md.
type GoalProposer interface {
	Propose(ctx context.Context, proposal GoalProposal) (GoalProposalResponse, error)
}

// ProposeGoalTool submits a {condition, verify} goal proposal and blocks for
// user approval — a coordination primitive, not a user-facing capability.
// See specs/domains/goal-mode.md.
type ProposeGoalTool struct {
	*sdktools.BaseTool
}

// NewProposeGoalTool creates the propose_goal tool.
func NewProposeGoalTool() *ProposeGoalTool {
	return &ProposeGoalTool{
		BaseTool: &sdktools.BaseTool{
			ToolGroup:       sdktools.GroupSystem,
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
		"verification_mode": {
			"type": "string",
			"description": "Optional. How the goal will be verified: 'executable' (default — the verify clause is an executable check) or 're_derivation' (the goal is verified by re-deriving it and comparing). Omit to use the default.",
			"enum": ["executable", "re_derivation"]
		}
	},
	"required": ["condition", "verify"]
}`),
			Policy: sdktools.PolicyAlwaysAllow,
		},
	}
}

type proposeGoalInput struct {
	Condition        string `json:"condition"`
	Verify           string `json:"verify"`
	VerificationMode string `json:"verification_mode"`
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

	// Normalize + reject unknown modes at the tool boundary so GoalState can
	// never hold an invalid mode; the canonical form rides the proposal.
	normalizedMode, verr := goal.NormalizeVerificationMode(params.VerificationMode)
	if verr != nil {
		return sdktools.ErrorResult("validation error: %v", verr), nil
	}
	params.VerificationMode = normalizedMode

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
		// Echo the user's edited wording so the agent commits to it.
		if resp.Condition != "" && resp.Condition != params.Condition {
			return sdktools.ToolResult{
				Content: fmt.Sprintf("Goal approved with edits. Condition: %s | Verify: %s", resp.Condition, coalesce(resp.Verify, params.Verify)),
			}, nil
		}
		return sdktools.ToolResult{
			Content: fmt.Sprintf("Goal approved by user. Condition: %s | Verify: %s", params.Condition, params.Verify),
		}, nil
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
