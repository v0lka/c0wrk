// Package goal defines the Goal domain: the user's declared success condition,
// the budget constraining the work spent reaching it, and the turn-by-turn
// runtime state machine. Zero-values are meaningful (MaxTurns == 0 is
// unlimited; a nil LastVerdict is "not verified yet"). See specs/domains/goal-mode.md.
package goal

import (
	"fmt"
	"time"
)

// VerificationMode selects how the goal loop verifies the success condition;
// chosen at derivation time and stored on GoalState so every turn, the
// verifier and the user-facing status share one notion of "verified".
// NormalizeVerificationMode is the single source of truth for valid values.
const (
	VerificationModeExecutable = "executable" // default: run the verify clause; its pass/fail decides

	VerificationModeReDerivation = "re_derivation" // re-derive the goal from conversation state and compare
)

// GoalStatus is the goal's lifecycle state, a string enum that round-trips
// through JSON/YAML. Transitions are driven by the orchestrator/reflector,
// not enforced here (specs/domains/goal-mode.md).
type GoalStatus string

const (
	StatusActive      GoalStatus = "active"       // in force; a cooperative pause leaves it active
	StatusMet         GoalStatus = "met"          // condition satisfied (terminal success)
	StatusExhausted   GoalStatus = "exhausted"    // budget consumed unmet (terminal failure)
	StatusBlockedIdle GoalStatus = "blocked_idle" // idle, awaiting external input; resumable, not terminal
	StatusCancelled   GoalStatus = "cancelled"    // abandoned by the user (terminal)
)

// IsTerminal reports whether the status is a terminal (non-resumable) state.
func (s GoalStatus) IsTerminal() bool {
	switch s {
	case StatusMet, StatusExhausted, StatusCancelled:
		return true
	default:
		return false
	}
}

// GoalBudget caps the resources the agent may spend pursuing a goal: a
// turn-only cap where MaxTurns == 0 imposes no limit (specs/domains/goal-mode.md).
type GoalBudget struct {
	MaxTurns int `json:"max_turns"` // 0 = unlimited
}

// IsUnlimited reports whether the budget imposes no resource cap at all.
func (b GoalBudget) IsUnlimited() bool {
	return b.MaxTurns == 0
}

// GoalEvidence is a single piece of evidence backing a verdict — each entry
// points at something concrete the agent or user can inspect. The recognized
// Type categories ride the field comment below.
type GoalEvidence struct {
	Type    string `json:"type"`    // test_output | file | command | qualitative
	Ref     string `json:"ref"`     // artifact reference (path, command, id, or note)
	Summary string `json:"summary"` // human-readable description of what this shows
}

// Recognized GoalEvidence.Type values.
const (
	EvidenceTypeTestOutput  = "test_output"
	EvidenceTypeFile        = "file"
	EvidenceTypeCommand     = "command"
	EvidenceTypeQualitative = "qualitative"
)

// Verdict is the outcome of the most recent attempt to verify the goal.
// Status is free-form rather than a GoalStatus because verdicts may describe
// partial outcomes; the orchestrator maps them onto GoalStatus.
type Verdict struct {
	Status     string         `json:"status"`      // declared outcome (e.g. "met", "not_met", "partial")
	Evidence   []GoalEvidence `json:"evidence"`    // supporting artifacts
	Reason     string         `json:"reason"`      // narrative explanation of the verdict
	DeclaredAt time.Time      `json:"declared_at"` // when the verdict was recorded
}

// GoalState is the full runtime state of a goal, mutated and persisted
// turn-by-turn (specs/domains/goal-mode.md).
type GoalState struct {
	Condition    string `json:"condition"`     // natural-language success condition
	VerifyClause string `json:"verify_clause"` // checkable predicate for the condition
	// VerificationMode selects how the condition is verified; empty means the
	// default. Normalize at every untrusted ingestion boundary.
	VerificationMode string     `json:"verification_mode"`
	Budget           GoalBudget `json:"budget"`       // resource caps (zero = unlimited)
	TurnCount        int        `json:"turn_count"`   // turns spent so far
	Status           GoalStatus `json:"status"`       // current lifecycle state
	LastVerdict      *Verdict   `json:"last_verdict"` // most recent self-evaluation verdict (nil = none yet)
	// LastError records why the most recent goal-loop turn failed, or "" after
	// a clean turn (cleared at the top of every turn) — it keeps a persistently-
	// errored turn a RESUMABLE halt instead of blocked_idle (goal-mode.md).
	LastError string `json:"last_error"`
	// LastErrorTyped is the error VALUE behind LastError: goalLoopResult
	// forwards it as HandleResult.Err so the session manager's auto-retry
	// classifier can errors.As the typed chain. Process-local — never
	// persisted, never read after a restore
	// (specs/decisions/065-auto-resend-retryable-errors.md).
	LastErrorTyped error `json:"-"`
	// LastVerification is the independent verifier's marker for the most
	// recent "met" attempt: "" / "confirmed" / "rejected" / "off" — threaded
	// through emitGoalStatus and the next-turn notice; reset each turn (one-shot).
	LastVerification string `json:"last_verification"`
	// LastVerificationReason and LastVerificationEvidence carry the verifier's
	// structured outcome for the most recent "met" attempt, paired with
	// LastVerification (one-shot, reset at the top of each agent turn) so the
	// UI shows the verifier's reasoning and artifacts, not a bare marker.
	LastVerificationReason   string         `json:"last_verification_reason"`
	LastVerificationEvidence []GoalEvidence `json:"last_verification_evidence"`
	CreatedAt                time.Time      `json:"created_at"` // when the goal was created
}

// NormalizeVerificationMode maps a verification-mode string to its canonical
// form — the single source of truth for valid values: "" maps to the default
// (VerificationModeExecutable), the two valid modes pass through, anything
// else errors. Call at every boundary where a mode enters the goal domain.
func NormalizeVerificationMode(mode string) (string, error) {
	switch mode {
	case "":
		return VerificationModeExecutable, nil
	case VerificationModeExecutable, VerificationModeReDerivation:
		return mode, nil
	default:
		return "", fmt.Errorf("unknown verification mode %q (want %q or %q)",
			mode, VerificationModeExecutable, VerificationModeReDerivation)
	}
}
