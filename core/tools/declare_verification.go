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

const toolDeclareVerificationDescription = `Declare your verification verdict on whether the goal's success condition has been satisfied. Use this after running the verification clause — it is the single channel through which the verification pass reports its structured outcome {confirmed, reason, evidence}.

confirmed is a boolean:
  - true:   you have verified the success condition holds. REQUIRES concrete evidence (file paths changed, test output, command results). A bare "done" without evidence fails verification.
  - false:  the condition could not be verified (still unmet, or verification failed).

Each evidence entry points at something concrete the user (or a verifier) can inspect: a file path, a test/command and its output, or a qualitative note with a clear ref.`

// VerificationOutcome is the structured result of an independent verification
// pass. It mirrors goal.Verdict in shape but is a distinct type so the goal
// loop's self-evaluation verdict (agent judging its own work) is never
// confused with the verifier's external verdict.
type VerificationOutcome struct {
	Confirmed  bool                `json:"confirmed"`
	Reason     string              `json:"reason"`
	Evidence   []goal.GoalEvidence `json:"evidence"`
	DeclaredAt time.Time           `json:"declared_at"`
}

// VerificationSink is the context-injected destination for verifier verdicts.
// The verification pass injects a concrete sink into the per-turn context;
// declare_verification writes structured outcomes into it so the loop reads
// them as typed objects rather than parsing ToolResult.Content (which is a
// plain string, and PostExecuteHook skips internal tools anyway).
//
// The implementation lives in a higher layer; the tool only knows the interface.
type VerificationSink interface {
	// Declare records an outcome as the most recent one. Implementations must
	// be safe for the single-threaded per-turn tool call that invokes this.
	Declare(v VerificationOutcome)
	// Last returns the most recently declared outcome, or nil if none has been
	// declared since the sink was created/injected.
	Last() *VerificationOutcome
}

// DeclareVerificationTool captures a verifier verdict via the
// context-injected VerificationSink. It is an internal tool
// (PolicyAlwaysAllow, bypasses the tool judge) because it is a coordination
// primitive for the verification pass, not a user-facing capability. It is a
// no-op outside a verification run (the sink will be nil), matching
// declare_goal_status's and propose_goal's pattern.
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

	// Evidence mandate: confirming the goal REQUIRES non-empty evidence. This
	// mirrors declare_goal_status's "met" enforcement so a bare "done" can
	// never pass verification without a concrete, inspectable artifact backing
	// it.
	//
	// The tool executor does NOT validate inputs against the JSON schema, so a
	// confirmed verdict must be rejected both when evidence is absent AND when
	// an entry is present but empty (e.g. evidence:[{}] or evidence:[{"ref":""}])
	// — otherwise a bare assertion could pass verification with no real artifact.
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

	// Short confirmation string. The structured outcome is delivered via the
	// sink; this Content is just for the transcript so the agent sees its
	// declaration was captured.
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
