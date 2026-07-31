package core

import (
	"context"
	"fmt"
	"strings"

	"github.com/v0lka/sp4rk/agents"
	"github.com/v0lka/sp4rk/llm"
	sdkmemory "github.com/v0lka/sp4rk/memory"
	"github.com/v0lka/sp4rk/orchestration"
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

// availableAgentsKey is the context key for the discovered subagent catalog.
// It carries []agents.AgentDescriptor (the discovery-time name/description/
// hidden representation) used by the "Available Subagents" system-prompt
// section. Populated from AgentManager.List() after routing.
type availableAgentsKey struct{}

// WithAvailableAgents returns a new context with the discovered subagent
// descriptors attached. The list is the full catalog (including hidden
// agents); the prompt formatter filters hidden entries for the public
// "Available Subagents" section but keeps them resolvable for explicit
// #mentions (see formatRequestedAgents).
func WithAvailableAgents(ctx context.Context, descriptors []agents.AgentDescriptor) context.Context {
	return context.WithValue(ctx, availableAgentsKey{}, descriptors)
}

// AvailableAgentsFromContext extracts the discovered subagent descriptors from
// the context. Returns nil if not present.
func AvailableAgentsFromContext(ctx context.Context) []agents.AgentDescriptor {
	if v, ok := ctx.Value(availableAgentsKey{}).([]agents.AgentDescriptor); ok {
		return v
	}
	return nil
}

// userAgentsKey is the context key for subagent names the user explicitly
// requested via #agent-name mentions. It carries []string and drives the
// "Requested Subagents" directive section. Reused by the message-preprocessor
// wiring (populated from opts.UserAgents).
type userAgentsKey struct{}

// WithUserAgents returns a new context with the explicitly-requested subagent
// names attached.
func WithUserAgents(ctx context.Context, names []string) context.Context {
	return context.WithValue(ctx, userAgentsKey{}, names)
}

// UserAgentsFromContext extracts the explicitly-requested subagent names from
// the context. Returns nil if not found.
func UserAgentsFromContext(ctx context.Context) []string {
	if v, ok := ctx.Value(userAgentsKey{}).([]string); ok {
		return v
	}
	return nil
}

// CoreContextManager wraps github.com/v0lka/sp4rk/memory.ContextWindow to implement the core-level
// ContextManager interface which adds SetTask and SetPlan(*Plan).
type CoreContextManager struct {
	*sdkmemory.ContextWindow
}

// NewCoreContextManager wraps a sp4rk ContextWindow into a core ContextManager.
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
func (c *CoreContextManager) SetPlanFromPlan(plan *orchestration.Plan) {
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
