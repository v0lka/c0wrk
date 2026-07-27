package prompts

import "strings"

// Goal-verification prompt template placeholders. The {goal_condition} and
// {goal_verify_clause} markers are shared with goal_mode.md and are defined as
// exported constants in goal_mode_substitute.go (GoalConditionPlaceholder /
// GoalVerifyClausePlaceholder) — they are reused here rather than redeclared to
// avoid duplicate-constant errors. Only the verification-specific placeholder
// is defined below.
//
// ReportedEvidencePlaceholder is the stable marker in goal_verification.md
// where the agent's self-reported evidence for a "met" goal verdict is
// injected. The verification directive treats this evidence as unverified
// claims that the verifier MUST re-check independently before confirming.
const ReportedEvidencePlaceholder = "{reported_evidence}"

// GoalVerificationSubstitute resolves the goal-verification directive's
// placeholders: the active goal's condition and verify clause (shared
// placeholders from goal_mode_substitute.go), the agent's reported evidence,
// and the platform shell-execution tool name. The {shell_tool} placeholder is
// resolved via SubstituteShellTool so the directive always names the tool that
// is actually registered on the current platform (bash_exec on Unix, posh_exec
// on Windows).
//
// This mirrors GoalModeSubstitute and is the single substitution point for the
// goal-verification directive; the embedded GoalVerification var remains a raw
// template with recoverable placeholders rather than values baked in at
// package load via init().
func GoalVerificationSubstitute(text, condition, verifyClause, reportedEvidence string) string {
	text = strings.ReplaceAll(text, GoalConditionPlaceholder, condition)
	text = strings.ReplaceAll(text, GoalVerifyClausePlaceholder, verifyClause)
	text = strings.ReplaceAll(text, ReportedEvidencePlaceholder, reportedEvidence)
	return SubstituteShellTool(text)
}
