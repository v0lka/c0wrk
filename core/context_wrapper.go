package core

import (
	"fmt"
	"strings"

	sdkmemory "github.com/user/agent/sdk/memory"
)

// CoreContextManager wraps sdk/memory.ContextWindow to implement the core-level
// ContextManager interface which adds SetTask(task, criteria) and SetPlan(*Plan).
type CoreContextManager struct {
	*sdkmemory.ContextWindow
}

// NewCoreContextManager wraps an SDK ContextWindow into a core ContextManager.
func NewCoreContextManager(cw *sdkmemory.ContextWindow) *CoreContextManager {
	return &CoreContextManager{ContextWindow: cw}
}

// SetTask sets the task and acceptance criteria, formatting them into a user message.
func (c *CoreContextManager) SetTask(task string, criteria []AcceptanceCriterion) {
	var b strings.Builder
	b.WriteString(task)

	if len(criteria) > 0 {
		b.WriteString("\n\nAcceptance Criteria:")
		for _, ac := range criteria {
			fmt.Fprintf(&b, "\n- [%s] %s", ac.ID, ac.Description)
			if ac.CheckType == "programmatic" && ac.CheckCmd != "" {
				fmt.Fprintf(&b, " (verify: %s)", ac.CheckCmd)
			}
		}
	}

	c.ContextWindow.SetTask(b.String())
}

// SetPlanFromPlan sets the plan, formatting it into a system message.
func (c *CoreContextManager) SetPlanFromPlan(plan *Plan) {
	if plan == nil || len(plan.Steps) == 0 {
		return
	}

	var b strings.Builder
	b.WriteString("Execution Plan:")
	for _, step := range plan.Steps {
		fmt.Fprintf(&b, "\n- [%s] %s", step.ID, step.Description)
		if len(step.DependsOn) > 0 {
			fmt.Fprintf(&b, " (depends on: %s)", strings.Join(step.DependsOn, ", "))
		}
	}

	c.SetPlan(b.String())
}
