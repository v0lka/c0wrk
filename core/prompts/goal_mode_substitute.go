package prompts

import "strings"

// Goal-mode prompt template placeholders. Each is a stable marker in
// goal_mode.md that renderGoalModeSection (core/systemprompt.go) substitutes
// from the active GoalState at prompt-assembly time. Keeping them as
// recoverable placeholders — rather than baking values into the embedded var
// at package load — mirrors SubstituteShellTool and avoids an init-time
// global mutation.
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
