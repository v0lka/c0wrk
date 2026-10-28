package prompts

import (
	"strings"

	"github.com/v0lka/c0wrk/core/goal"
)

// ReportedEvidencePlaceholder is the stable marker in goal_verification.md and
// the re-derivation directive where the agent's self-reported evidence for a
// "met" verdict is injected. Both verification directives treat that evidence
// as unverified claims the verifier MUST re-check independently before
// confirming (the shared {goal_condition}/{goal_verify_clause} markers live in
// goal_mode_substitute.go).
const ReportedEvidencePlaceholder = "{reported_evidence}"

// GoalVerificationSubstitute resolves the goal-verification directives' shared
// placeholder set — condition, verify clause, reported evidence, and the
// platform shell tool (via SubstituteShellTool) — for BOTH verification
// directives, which stay raw templates with recoverable placeholders.
// See specs/domains/goal-mode.md (independent verification).
func GoalVerificationSubstitute(text, condition, verifyClause, reportedEvidence string) string {
	text = strings.ReplaceAll(text, GoalConditionPlaceholder, condition)
	text = strings.ReplaceAll(text, GoalVerifyClausePlaceholder, verifyClause)
	text = strings.ReplaceAll(text, ReportedEvidencePlaceholder, reportedEvidence)
	return SubstituteShellTool(text)
}

// GoalVerificationDirectiveByMode selects the verification directive for the
// goal's VerificationMode — executable/empty selects GoalVerification (the
// verifier re-runs the verify clause), re_derivation selects GoalReDerivation
// (the verifier delegates a fresh, read-only execution) — and returns it with
// all placeholders resolved. Toolset selection by mode stays with the
// orchestrator.
// See specs/domains/goal-mode.md (mode-driven verification).
func GoalVerificationDirectiveByMode(mode, condition, verifyClause, reportedEvidence string) string {
	directive := goalVerificationDirectiveForMode(mode, GoalVerification, GoalReDerivation)
	return GoalVerificationSubstitute(directive, condition, verifyClause, reportedEvidence)
}

// GoalVerificationLiteDirectiveByMode is the Lite counterpart of
// GoalVerificationDirectiveByMode: it selects the compact Lite variant for the
// given mode and resolves the same placeholder set via
// GoalVerificationSubstitute. It shares goalVerificationDirectiveForMode with
// the verbose wrapper, so the two variants can never drift.
// See specs/domains/model-profiles.md (goal-agent Lite parity).
func GoalVerificationLiteDirectiveByMode(mode, condition, verifyClause, reportedEvidence string) string {
	directive := goalVerificationDirectiveForMode(mode, GoalVerificationLite, GoalReDerivationLite)
	return GoalVerificationSubstitute(directive, condition, verifyClause, reportedEvidence)
}

// goalVerificationDirectiveForMode is the single source of truth for the
// mode → directive mapping shared by the verbose and Lite wrappers.
func goalVerificationDirectiveForMode(mode, executable, reDerivation string) string {
	if mode == goal.VerificationModeReDerivation {
		return reDerivation
	}
	return executable
}
