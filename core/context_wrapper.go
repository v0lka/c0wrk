package core

import (
	"fmt"
	"strings"

	sdkmemory "github.com/user/agent/sdk/memory"
)

// CoreContextManager wraps sdk/memory.ContextWindow to implement the core-level
// ContextManager interface which adds SetTask and SetPlan(*Plan).
type CoreContextManager struct {
	*sdkmemory.ContextWindow
}

// NewCoreContextManager wraps an SDK ContextWindow into a core ContextManager.
func NewCoreContextManager(cw *sdkmemory.ContextWindow) *CoreContextManager {
	return &CoreContextManager{ContextWindow: cw}
}

// SetTask sets the task into the context window.
func (c *CoreContextManager) SetTask(task string) {
	c.ContextWindow.SetTask(task)
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
