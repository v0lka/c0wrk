package prompts

import (
	"strings"

	"github.com/v0lka/c0wrk/core/goal"
)

// Goal-verification prompt template placeholders. The {goal_condition} and
// {goal_verify_clause} markers are shared with goal_mode.md and are defined as
// exported constants in goal_mode_substitute.go (GoalConditionPlaceholder /
// GoalVerifyClausePlaceholder) — they are reused here rather than redeclared to
// avoid duplicate-constant errors. Only the verification-specific placeholder
// is defined below.
//
// ReportedEvidencePlaceholder is the stable marker in goal_verification.md (and
// the re-derivation directive) where the agent's self-reported evidence for a
// "met" goal verdict is injected. Both verification directives treat this
// evidence as unverified claims that the verifier MUST re-check independently
// before confirming.
const ReportedEvidencePlaceholder = "{reported_evidence}"

// GoalVerificationSubstitute resolves the goal-verification directive's
// placeholders: the active goal's condition and verify clause (shared
// placeholders from goal_mode_substitute.go), the agent's reported evidence,
// and the platform shell-execution tool name. The {shell_tool} placeholder is
// resolved via SubstituteShellTool so the directive always names the tool that
// is actually registered on the current platform (bash_exec on Unix, posh_exec
// on Windows).
//
// This function is directive-agnostic: it resolves the SAME placeholder set for
// BOTH verification directives — the executable directive (GoalVerification)
// and the re-derivation directive (GoalReDerivation). Callers that already know
// which directive text to render pass it here directly; callers that want the
// prompts layer to pick the directive from the goal's VerificationMode should
// use GoalVerificationDirectiveByMode instead. This mirrors GoalModeSubstitute
// and is the single substitution point for the goal-verification directives;
// the embedded GoalVerification / GoalReDerivation vars remain raw templates
// with recoverable placeholders rather than values baked in at package load via
// init().
func GoalVerificationSubstitute(text, condition, verifyClause, reportedEvidence string) string {
	text = strings.ReplaceAll(text, GoalConditionPlaceholder, condition)
	text = strings.ReplaceAll(text, GoalVerifyClausePlaceholder, verifyClause)
	text = strings.ReplaceAll(text, ReportedEvidencePlaceholder, reportedEvidence)
	return SubstituteShellTool(text)
}

// GoalVerificationDirectiveByMode selects the appropriate verification directive
// for the given mode and returns it with all placeholders resolved, ready to be
// injected as the verifier's system-prompt directive.
//
// The selection is driven by the goal's VerificationMode:
//   - goal.VerificationModeExecutable (the default, including the empty string)
//     selects the executable directive (GoalVerification), whose verifier
//     independently re-runs the verify clause.
//   - goal.VerificationModeReDerivation selects the re-derivation directive
//     (GoalReDerivation), whose verifier delegates a fresh, read-only execution
//     of the goal's process and confirms only on a clean outcome.
//
// This is the prompts-layer source of truth for the mode -> directive mapping;
// it resolves the directive via GoalVerificationSubstitute (the shared
// placeholder set). Toolset selection by mode stays with the orchestrator.
func GoalVerificationDirectiveByMode(mode, condition, verifyClause, reportedEvidence string) string {
	directive := GoalVerification
	if mode == goal.VerificationModeReDerivation {
		directive = GoalReDerivation
	}
	return GoalVerificationSubstitute(directive, condition, verifyClause, reportedEvidence)
}
