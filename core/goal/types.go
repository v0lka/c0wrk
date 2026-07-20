// Package goal defines the Goal domain: the user's declared success condition,
// the budget that constrains how much work the agent may spend reaching it, and
// the runtime state machine that tracks progress toward (or away from) that
// condition.
//
// A Goal is the persistent answer to "are we done yet?". The orchestrator
// consults GoalState after each turn to decide whether to continue, pause, or
// stop. The Verdict captures the most recent machine- or user-declared outcome
// of attempting to verify the condition, along with the evidence backing it.
//
// Zero-values are meaningful throughout this package:
//   - A GoalBudget with MaxTurns == 0 means "unlimited" — the goal imposes no
//     turn cap.
//   - A nil LastVerdict means no verification has been performed yet.
package goal

import "time"

// GoalStatus is the lifecycle state of a goal. It is a string enum so it
// round-trips cleanly through JSON/YAML and is human-readable in logs.
//
// State transitions are driven by the orchestrator/reflector and are not
// enforced inside this package; the values are documented here to make the
// intended state machine explicit:
//
//	active        — goal is in force; the agent is working toward it.
//	paused        — goal temporarily suspended (e.g. user pause, hand-off).
//	met           — the condition has been satisfied (terminal success).
//	exhausted     — the budget was consumed without meeting the condition
//	                 (terminal failure: turns/tokens/deadline hit).
//	blocked_idle  — the agent cannot make further progress and is idle,
//	                 awaiting external input or a changed situation.
//	cancelled     — the goal was abandoned by the user (terminal).
type GoalStatus string

const (
	StatusActive      GoalStatus = "active"
	StatusPaused      GoalStatus = "paused"
	StatusMet         GoalStatus = "met"
	StatusExhausted   GoalStatus = "exhausted"
	StatusBlockedIdle GoalStatus = "blocked_idle"
	StatusCancelled   GoalStatus = "cancelled"
)

// GoalBudget caps the resources the agent may spend pursuing a goal.
//
// This is a turn-only cap: MaxTurns == 0 imposes no turn limit. The agent stops
// when the turn budget that IS set is exceeded (e.g. MaxTurns == 5 allows five
// turns).
type GoalBudget struct {
	MaxTurns int `json:"max_turns"` // 0 = unlimited
}

// IsUnlimited reports whether the budget imposes no resource cap at all.
func (b GoalBudget) IsUnlimited() bool {
	return b.MaxTurns == 0
}

// GoalEvidence is a single piece of evidence supporting a verdict. Evidence is
// what makes a verdict trustworthy rather than a bare assertion: each entry
// points at something concrete the agent (or user) can inspect.
//
// Type categorizes the evidence. Recognized categories:
//
//	test_output — output of a test run (Ref = test name/id or command).
//	file        — a file on disk (Ref = path).
//	command     — a shell command and its output (Ref = command string).
//	qualitative — a human judgment with no machine-checkable artifact
//	               (Ref is free text, e.g. a user confirmation).
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

// Verdict is the outcome of the most recent attempt to verify whether the goal
// has been met. It records the declared status, the evidence backing it, and a
// human-readable reason.
//
// Status is a free-form string rather than a GoalStatus because a verdict may
// describe partial outcomes (e.g. "met_with_caveats") that do not map cleanly
// onto the goal's terminal lifecycle states. The orchestrator maps verdict
// statuses onto GoalStatus values.
type Verdict struct {
	Status     string         `json:"status"`      // declared outcome (e.g. "met", "not_met", "partial")
	Evidence   []GoalEvidence `json:"evidence"`    // supporting artifacts
	Reason     string         `json:"reason"`      // narrative explanation of the verdict
	DeclaredAt time.Time      `json:"declared_at"` // when the verdict was recorded
}

// GoalState is the full runtime state of a goal. It is the object the
// orchestrator mutates and persists turn-by-turn.
//
// Condition is the declarative success condition ("what does done look like?").
// VerifyClause is the machine-/agent-checkable predicate used to test the
// condition. The two are kept separate because a natural-language condition is
// useful for display and user edits, while the verify clause drives automated
// checking.
type GoalState struct {
	Condition    string     `json:"condition"`     // natural-language success condition
	VerifyClause string     `json:"verify_clause"` // checkable predicate for the condition
	Budget       GoalBudget `json:"budget"`        // resource caps (zero = unlimited)
	TurnCount    int        `json:"turn_count"`    // turns spent so far
	Status       GoalStatus `json:"status"`        // current lifecycle state
	LastVerdict  *Verdict   `json:"last_verdict"`  // most recent verification outcome (nil = none yet)
	CreatedAt    time.Time  `json:"created_at"`    // when the goal was created
}

// IsTerminal reports whether the status is a terminal (non-resumable) state.
// Met, exhausted, and cancelled are terminal; paused and blocked_idle are not.
func (s GoalStatus) IsTerminal() bool {
	switch s {
	case StatusMet, StatusExhausted, StatusCancelled:
		return true
	default:
		return false
	}
}
