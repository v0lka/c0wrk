package core

import (
	"maps"

	sdkagents "github.com/v0lka/sp4rk/agents"
	"github.com/v0lka/sp4rk/skills"
)

// Builder-side capability plumbing for the skill and agent catalogs — the
// same immutable-snapshot pattern the MCP server modes use (published once,
// republished on reconfigure, read through cloning getters).

// capabilityModesFromConfig projects a capabilities section into the builder's
// immutable snapshot (both-empty entries omitted — absent = auto). See specs/domains/capabilities.md.
func capabilityModesFromConfig(entries map[string]CapabilityModes) map[string]CapabilityModes {
	if len(entries) == 0 {
		return nil
	}
	modes := make(map[string]CapabilityModes, len(entries))
	for name, cm := range entries {
		if cm.ChatMode == "" && cm.CodeMode == "" {
			continue // both auto — absent entry
		}
		modes[name] = CapabilityModes{ChatMode: cm.ChatMode, CodeMode: cm.CodeMode}
	}
	if len(modes) == 0 {
		return nil
	}
	return modes
}

// currentSkillModes returns an independent snapshot of the skill-catalog modes.
func (b *OrchestratorBuilder) currentSkillModes() map[string]CapabilityModes {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return maps.Clone(b.skillModes)
}

// currentAgentModes returns an independent snapshot of the subagent-catalog modes.
func (b *OrchestratorBuilder) currentAgentModes() map[string]CapabilityModes {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return maps.Clone(b.agentModes)
}

// UpdateCapabilities republishes all three capability snapshots from cfg — a
// pure in-memory write under b.mu (mode edits are policy, not topology,
// ADR-084: no gateway work; orchestrators pick snapshots up at next task
// entry). A nil section gates nothing. See specs/domains/capabilities.md.
func (b *OrchestratorBuilder) UpdateCapabilities(cfg *BuilderConfig) {
	b.mu.Lock()
	b.mcpModes = mcpServerModesFromConfig(cfg)
	b.skillModes = capabilityModesFromConfig(cfg.Capabilities.Skills)
	b.agentModes = capabilityModesFromConfig(cfg.Capabilities.Agents)
	b.mu.Unlock()
}

// implicitSkillDescriptors filters the skill catalog to implicit (router)
// visibility — auto or absent only. See specs/domains/capabilities.md (skills).
func implicitSkillDescriptors(descs []skills.SkillDescriptor, modes map[string]string) []skills.SkillDescriptor {
	if len(modes) == 0 || len(descs) == 0 {
		return descs
	}
	out := make([]skills.SkillDescriptor, 0, len(descs))
	for _, d := range descs {
		switch modes[d.Name] {
		case mcpModeManual, mcpModeDisabled:
			continue // not implicitly discoverable
		}
		out = append(out, d)
	}
	return out
}

// availableAgentDescriptors filters the discovered subagent catalog to implicit
// roster visibility — auto or absent only. See specs/domains/capabilities.md (subagents).
func availableAgentDescriptors(descs []sdkagents.AgentDescriptor, modes map[string]string) []sdkagents.AgentDescriptor {
	if len(modes) == 0 || len(descs) == 0 {
		return descs
	}
	out := make([]sdkagents.AgentDescriptor, 0, len(descs))
	for _, d := range descs {
		switch modes[d.Name] {
		case mcpModeManual, mcpModeDisabled:
			continue // not implicitly discoverable
		}
		out = append(out, d)
	}
	return out
}

// implicitCapabilityName: auto (or absent) passes implicit discovery; manual
// and disabled do not (mentions differ — manual passes, disabled refused).
func implicitCapabilityName(name string, modes map[string]string) bool {
	switch modes[name] {
	case mcpModeManual, mcpModeDisabled:
		return false
	}
	return true
}

// implicitSkillNames applies the implicit-discovery rule to routing-derived names
// (router matches + RESTORED routing decisions). See specs/domains/capabilities.md (skills).
func implicitSkillNames(names []string, modes map[string]string) []string {
	if len(modes) == 0 || len(names) == 0 {
		return names
	}
	out := make([]string, 0, len(names))
	for _, n := range names {
		if implicitCapabilityName(n, modes) {
			out = append(out, n)
		}
	}
	return out
}

// mentionedCapabilityNames filters explicitly-mentioned names: auto and manual
// pass, disabled is dropped (never an activation). See specs/domains/capabilities.md.
func mentionedCapabilityNames(names []string, modes map[string]string) []string {
	if len(modes) == 0 || len(names) == 0 {
		return names
	}
	out := make([]string, 0, len(names))
	for _, n := range names {
		if modes[n] == mcpModeDisabled {
			continue
		}
		out = append(out, n)
	}
	return out
}
