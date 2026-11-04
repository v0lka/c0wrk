package core

import (
	"context"

	sdkagents "github.com/v0lka/sp4rk/agents"
)

// Orchestrator-side capability gating for the skill and agent catalogs — the
// mirror of the per-server MCP gating in orchestrator_mcp.go. The shared
// auto/manual/disabled semantics and the once-per-task-entry snapshot:
// specs/domains/capabilities.md.

// skillModesKey carries the per-task flat skill name→mode snapshot.
type skillModesKey struct{}

// agentModesKey carries the per-task flat agent name→mode snapshot.
type agentModesKey struct{}

// WithSkillCapabilityModes attaches the task's flat skill mode snapshot; a
// nil map is equivalent to no value (every skill auto).
func WithSkillCapabilityModes(ctx context.Context, modes map[string]string) context.Context {
	return context.WithValue(ctx, skillModesKey{}, modes)
}

// SkillCapabilityModesFromContext extracts the snapshot; nil means no gating
// (the default).
func SkillCapabilityModesFromContext(ctx context.Context) map[string]string {
	if v, ok := ctx.Value(skillModesKey{}).(map[string]string); ok {
		return v
	}
	return nil
}

// WithAgentCapabilityModes attaches the task's flat agent mode snapshot; a
// nil map is equivalent to no value (every agent auto).
func WithAgentCapabilityModes(ctx context.Context, modes map[string]string) context.Context {
	return context.WithValue(ctx, agentModesKey{}, modes)
}

// AgentCapabilityModesFromContext extracts the snapshot; nil means no gating
// (the default).
func AgentCapabilityModesFromContext(ctx context.Context) map[string]string {
	if v, ok := ctx.Value(agentModesKey{}).(map[string]string); ok {
		return v
	}
	return nil
}

// currentSkillCapabilityModes returns the live resolver snapshot when wired,
// else the static constructor map.
func (o *Orchestrator) currentSkillCapabilityModes() map[string]CapabilityModes {
	if o.config.SkillModesResolver != nil {
		return o.config.SkillModesResolver()
	}
	return o.config.SkillModes
}

// currentAgentCapabilityModes returns the live resolver snapshot when wired,
// else the static constructor map.
func (o *Orchestrator) currentAgentCapabilityModes() map[string]CapabilityModes {
	if o.config.AgentModesResolver != nil {
		return o.config.AgentModesResolver()
	}
	return o.config.AgentModes
}

// attachCapabilitySnapshots resolves the session context once per capability
// kind and attaches the flat projections; an empty projection attaches
// nothing — a missing snapshot means every capability is auto.
func (o *Orchestrator) attachCapabilitySnapshots(ctx context.Context) context.Context {
	sctx := o.sessionCapabilityContext()
	if modes := capabilityModesForSession(o.currentSkillCapabilityModes(), sctx); len(modes) > 0 {
		ctx = WithSkillCapabilityModes(ctx, modes)
	}
	if modes := capabilityModesForSession(o.currentAgentCapabilityModes(), sctx); len(modes) > 0 {
		ctx = WithAgentCapabilityModes(ctx, modes)
	}
	return ctx
}

// mentionedSkillsForTask filters user-mentioned skills for the task's
// capability snapshot (auto and manual pass, disabled dropped).
func mentionedSkillsForTask(ctx context.Context, userSkills []string) []string {
	return mentionedCapabilityNames(userSkills, SkillCapabilityModesFromContext(ctx))
}

// mentionedAgentsForTask filters user-mentioned agents for the task's
// capability snapshot (auto and manual pass, disabled dropped).
func mentionedAgentsForTask(ctx context.Context, userAgents []string) []string {
	return mentionedCapabilityNames(userAgents, AgentCapabilityModesFromContext(ctx))
}

// agentDelegationBlocked reports whether the task's capability snapshot
// refuses delegation to the named agent: only "disabled" blocks; an absent
// snapshot blocks nothing.
func agentDelegationBlocked(ctx context.Context, name string) bool {
	return AgentCapabilityModesFromContext(ctx)[name] == mcpModeDisabled
}

// filterRosterAgents applies the implicit-discovery rule to the discovered
// subagent catalog for the task's agent snapshot (auto-only roster).
func filterRosterAgents(ctx context.Context, descriptors []sdkagents.AgentDescriptor) []sdkagents.AgentDescriptor {
	return availableAgentDescriptors(descriptors, AgentCapabilityModesFromContext(ctx))
}
