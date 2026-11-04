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

const toolDeclareVerificationDescription = `Purpose: record the independent verifier's verdict on the goal condition — the structured outcome of the verification pass.
Use when: you are executing the verification pass itself; declare once, after actually running the checks the verify clause names. Confirming REQUIRES concrete evidence — a bare "done" is rejected.
Inputs: confirmed (true: the condition holds; false: it could not be verified); reason (narrative of what was verified or why it failed); evidence (array of {type, ref, summary} artifacts — REQUIRED when confirmed=true).
Outputs: the verdict is stored for the goal loop to read.
Example: confirmed true, reason naming the checks run, evidence citing the command and its passing output.
Anti-example: not for mid-task checks (run them directly as you work); never confirm without evidence; declaring is not a substitute for running the checks.`

// VerificationOutcome mirrors goal.Verdict in shape but is a distinct type:
// the verifier's external verdict must never be confused with the agent's
// self-evaluation.
type VerificationOutcome struct {
	Confirmed  bool                `json:"confirmed"`
	Reason     string              `json:"reason"`
	Evidence   []goal.GoalEvidence `json:"evidence"`
	DeclaredAt time.Time           `json:"declared_at"`
}

// VerificationSink is the context-injected destination for verifier verdicts,
// read back as typed objects — ToolResult.Content is a plain string and
// PostExecuteHook skips internal tools (specs/domains/goal-mode.md). The
// implementation lives in a higher layer.
type VerificationSink interface {
	// Declare records an outcome as the most recent one. Implementations must
	// be safe for the single-threaded per-turn tool call that invokes this.
	Declare(v VerificationOutcome)

	// Last returns the most recently declared outcome, or nil if none has
	// been declared since the sink was created/injected.
	Last() *VerificationOutcome
}

// DeclareVerificationTool captures the verifier's verdict via the
// context-injected sink; a no-op outside a verification run
// (specs/domains/goal-mode.md).
type DeclareVerificationTool struct {
	*sdktools.BaseTool
}

// NewDeclareVerificationTool creates the declare_verification tool.
func NewDeclareVerificationTool() *DeclareVerificationTool {
	return &DeclareVerificationTool{
		BaseTool: &sdktools.BaseTool{
			ToolGroup:       sdktools.GroupSystem,
			ToolName:        "declare_verification",
			ToolDescription: toolDeclareVerificationDescription,
			Schema: json.RawMessage(`{
	"type": "object",
	"properties": {
		"confirmed": {
			"type": "boolean",
			"description": "Your verification verdict. true means the success condition holds (REQUIRES concrete evidence); false means it could not be verified."
		},
		"evidence": {
			"type": "array",
			"description": "Concrete artifacts backing the verdict. REQUIRED when confirmed is true. Each entry is {type, ref, summary}: type is test_output|file|command|qualitative; ref is the artifact reference (path, command, id, or note); summary explains what the artifact shows.",
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
			"description": "Narrative explanation of the verdict: what was verified, what failed, or what remains unconfirmed."
		}
	},
	"required": ["confirmed", "reason"]
}`),
			Policy: sdktools.PolicyAlwaysAllow,
		},
	}
}

type declareVerificationInput struct {
	Confirmed bool                `json:"confirmed"`
	Evidence  []goal.GoalEvidence `json:"evidence"`
	Reason    string              `json:"reason"`
}

func (t *DeclareVerificationTool) Execute(ctx context.Context, input json.RawMessage) (sdktools.ToolResult, error) {
	var params declareVerificationInput
	if err := json.Unmarshal(input, &params); err != nil {
		return sdktools.ParseInputError(err)
	}

	// Evidence mandate (specs/domains/goal-mode.md): a confirmed verdict needs
	// concrete artifacts, and since the executor does not schema-validate
	// inputs, both absent evidence and present-but-empty entries (e.g.
	// [{}]) are rejected here.
	if params.Confirmed {
		if len(params.Evidence) == 0 {
			return sdktools.ToolResult{
				Content: "validation error: declaring confirmed=true requires concrete evidence. Cite at least one artifact — a changed file path, test output, or command result — as evidence. A bare \"done\" without evidence fails verification. Call declare_verification again with evidence, or use confirmed=false if the condition could not be verified.",
				IsError: true,
			}, nil
		}
		for i, ev := range params.Evidence {
			if strings.TrimSpace(ev.Type) == "" || strings.TrimSpace(ev.Ref) == "" || strings.TrimSpace(ev.Summary) == "" {
				return sdktools.ErrorResult(
					"validation error: evidence[%d] is incomplete — each evidence entry needs non-empty type, ref, and summary. A confirmed verdict must cite a concrete, inspectable artifact (e.g. {\"type\":\"file\",\"ref\":\"path/to/file\",\"summary\":\"...\"}).",
					i,
				), nil
			}
		}
	}

	sink := VerificationSinkFrom(ctx)
	if sink == nil {
		return sdktools.ErrorResult("declare_verification: no verification sink in context (not running inside a verification pass)"), nil
	}

	outcome := VerificationOutcome{
		Confirmed:  params.Confirmed,
		Evidence:   params.Evidence,
		Reason:     params.Reason,
		DeclaredAt: time.Now(),
	}
	sink.Declare(outcome)

	// Transcript echo only — the structured outcome travels via the sink.
	var summary string
	switch {
	case params.Confirmed:
		summary = fmt.Sprintf("Verification recorded: CONFIRMED with %d evidence item(s). %s", len(params.Evidence), strings.TrimSpace(params.Reason))
	default:
		summary = "Verification recorded: NOT CONFIRMED. The condition could not be verified."
	}
	return sdktools.ToolResult{Content: summary}, nil
}

// --- Context plumbing ---

type verificationSinkKey struct{}

// WithVerificationSink injects the verification sink into the context.
func WithVerificationSink(ctx context.Context, sink VerificationSink) context.Context {
	return context.WithValue(ctx, verificationSinkKey{}, sink)
}

// VerificationSinkFrom extracts the verification sink from the context, or returns nil.
func VerificationSinkFrom(ctx context.Context) VerificationSink {
	if v, ok := ctx.Value(verificationSinkKey{}).(VerificationSink); ok {
		return v
	}
	return nil
}
