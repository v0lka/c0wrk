package prompts

import "strings"

// Goal-mode prompt template placeholders: stable markers in goal_mode.md that
// renderGoalModeSection (core/systemprompt.go) substitutes from the active
// GoalState at prompt-assembly time. They stay recoverable placeholders (no
// init-time baking), mirroring SubstituteShellTool.
const (
	GoalConditionPlaceholder    = "{goal_condition}"
	GoalVerifyClausePlaceholder = "{goal_verify_clause}"
	GoalBudgetLinePlaceholder   = "{goal_budget_line}"
)

// GoalModeSubstitute resolves the goal-mode placeholders in text to the active
// goal's condition, verify clause, and budget line. It is the single
// substitution point for goal-mode prompt data; the embedded GoalMode var
// remains a raw template with recoverable placeholders.
func GoalModeSubstitute(text, condition, verifyClause, budgetLine string) string {
	text = strings.ReplaceAll(text, GoalConditionPlaceholder, condition)
	text = strings.ReplaceAll(text, GoalVerifyClausePlaceholder, verifyClause)
	text = strings.ReplaceAll(text, GoalBudgetLinePlaceholder, budgetLine)
	return text
}
