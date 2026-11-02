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

// availableAgentsKey is the context key for the discovered subagent catalog:
// []agents.AgentDescriptor (name/description/hidden) feeding the "Available
// Subagents" prompt section. Populated from AgentManager.List().
type availableAgentsKey struct{}

// WithAvailableAgents attaches the FULL catalog (hidden entries included);
// the prompt formatters filter hidden entries for the public roster and
// resolve explicit /-mention descriptions from it (formatRequestedAgents).
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
// requested via /-mentions (ADR-076 grammar); drives the "Requested
// Subagents" directive section, populated from HandleOptions.UserAgents.
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

// userMCPServersKey is the context key for MCP server names the user
// explicitly mentioned for this task via /-mentions (plain "/server-name" or
// qualified "/mcp: name"); drives the soft "## Requested MCP Servers" prompt
// section. A mention enables a manual-mode server for the WHOLE task —
// specs/domains/tool-system/mcp-gateway.md; populated from HandleOptions.UserMCPServers.
type userMCPServersKey struct{}

// WithUserMCPServers returns a new context with the explicitly-mentioned MCP
// server names attached.
func WithUserMCPServers(ctx context.Context, servers []string) context.Context {
	return context.WithValue(ctx, userMCPServersKey{}, servers)
}

// UserMCPServersFromContext extracts the explicitly-mentioned MCP server
// names from the context. Returns nil if not found.
func UserMCPServersFromContext(ctx context.Context) []string {
	if v, ok := ctx.Value(userMCPServersKey{}).([]string); ok {
		return v
	}
	return nil
}

// CoreContextManager wraps github.com/v0lka/sp4rk/memory.ContextWindow to satisfy core's
// ContextManager (agent.ContextManager + SetTask); SetPlanFromPlan adapts a
// *orchestration.Plan into the formatted SetPlan string.
type CoreContextManager struct {
	*sdkmemory.ContextWindow
}

// NewCoreContextManager wraps a sp4rk ContextWindow into a core ContextManager.
func NewCoreContextManager(cw *sdkmemory.ContextWindow) *CoreContextManager {
	return &CoreContextManager{ContextWindow: cw}
}

func (c *CoreContextManager) SetTask(task string) {
	c.ContextWindow.SetTask(task)
}

// ContextTracker exposes the tracker so the orchestrator can wire it to the
// TrackingCaller per step.
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
