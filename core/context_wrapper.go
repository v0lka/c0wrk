package core

import (
	"context"
	"fmt"
	"strings"

	"github.com/v0lka/c0wrk/sdk/llm"
	sdkmemory "github.com/v0lka/c0wrk/sdk/memory"
)

// domainKey is the context key for the routing domain.
type domainKey struct{}

// WithDomain returns a new context with the routing domain attached.
func WithDomain(ctx context.Context, domain string) context.Context {
	return context.WithValue(ctx, domainKey{}, domain)
}

// DomainFromContext extracts the routing domain from the context.
// Returns an empty string if not found.
func DomainFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(domainKey{}).(string); ok {
		return v
	}
	return ""
}

// complexityKey is the context key for the routing complexity.
type complexityKey struct{}

// userSkillsKey is the context key for explicitly user-activated skill names.
type userSkillsKey struct{}

// WithComplexity returns a new context with the routing complexity attached.
func WithComplexity(ctx context.Context, complexity int) context.Context {
	return context.WithValue(ctx, complexityKey{}, complexity)
}

// ComplexityFromContext extracts the routing complexity from the context.
// Returns 0 if not found.
func ComplexityFromContext(ctx context.Context) int {
	if v, ok := ctx.Value(complexityKey{}).(int); ok {
		return v
	}
	return 0
}

// WithUserSkills returns a new context with the explicitly user-activated skill names attached.
func WithUserSkills(ctx context.Context, skills []string) context.Context {
	return context.WithValue(ctx, userSkillsKey{}, skills)
}

// UserSkillsFromContext extracts the user-activated skill names from the context.
// Returns nil if not found.
func UserSkillsFromContext(ctx context.Context) []string {
	if v, ok := ctx.Value(userSkillsKey{}).([]string); ok {
		return v
	}
	return nil
}

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

// ContextTracker returns the underlying ContextTokenTracker for this context manager.
// This allows the orchestrator to wire it to the TrackingCaller per step.
func (c *CoreContextManager) ContextTracker() *llm.ContextTokenTracker {
	return c.Tracker()
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
