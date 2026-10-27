package backend

import (
	"errors"
	"fmt"

	"github.com/v0lka/c0wrk/backend/config"
)

// Capabilities section identifiers accepted by UpdateCapabilityModes. The
// per-context MODE values reuse the MCP server enum — no enum of their own.
const (
	CapabilitySectionSkills = "skills"
	CapabilitySectionAgents = "agents"
	CapabilitySectionMCP    = "mcp"
)

// CapabilityModesInfo is one capability entry's per-context wire modes; both
// fields always carry a canonical enum value — a missing entry reads "auto".
type CapabilityModesInfo struct {
	ChatMode string `json:"chat_mode"`
	CodeMode string `json:"code_mode"`
}

// CapabilitiesInfo is the GetCapabilityModes wire shape; the section maps are
// never nil, so the frontend receives empty objects rather than nulls.
// See specs/domains/capabilities.md.
type CapabilitiesInfo struct {
	Skills map[string]CapabilityModesInfo `json:"skills"`
	Agents map[string]CapabilityModesInfo `json:"agents"`
	MCP    map[string]CapabilityModesInfo `json:"mcp"`
}

// capabilitiesSnapshot is a coherent copy of the capabilities section plus
// the configured MCP server names, taken under one configMu read lock.
type capabilitiesSnapshot struct {
	caps config.CapabilitiesConfig
	// mcp carries capabilities.mcp with stale names (no longer configured)
	// already removed — the load pipeline keeps stored configs clean, so this
	// only guards configs built in memory (tests, in-process writers).
	mcp map[string]config.CapabilityModes
}

func (f *FrontendAPI) capabilitiesSnapshot() capabilitiesSnapshot {
	f.configMu.RLock()
	defer f.configMu.RUnlock()
	return capabilitiesSnapshot{
		caps: config.CapabilitiesConfig{
			Skills: copyCapabilityModesMap(f.configCapSkills()),
			Agents: copyCapabilityModesMap(f.configCapAgents()),
		},
		mcp: f.configMCPCapsCopy(),
	}
}

// configMCPCapsCopy copies capabilities.mcp minus stale names (the load
// normalization's rule), so the read surface never resurrects a deleted
// server's entry. Callers MUST hold configMu; a nil section yields empty.
func (f *FrontendAPI) configMCPCapsCopy() map[string]config.CapabilityModes {
	out := make(map[string]config.CapabilityModes, len(f.configCapMCP()))
	for name, modes := range f.configCapMCP() {
		if _, known := f.configMCPServerNames()[name]; !known {
			continue
		}
		out[name] = modes
	}
	return out
}

// The configCap* accessors read the raw sections. They exist so the snapshot
// builder stays readable; callers MUST hold configMu.

func (f *FrontendAPI) configCapSkills() map[string]config.CapabilityModes {
	if f.config == nil {
		return nil
	}
	return f.config.Capabilities.Skills
}

func (f *FrontendAPI) configCapAgents() map[string]config.CapabilityModes {
	if f.config == nil {
		return nil
	}
	return f.config.Capabilities.Agents
}

func (f *FrontendAPI) configCapMCP() map[string]config.CapabilityModes {
	if f.config == nil {
		return nil
	}
	return f.config.Capabilities.MCP
}

// configMCPServerNames returns the configured MCP server names as a set.
// Callers MUST hold configMu; reading a nil map yields an empty set.
func (f *FrontendAPI) configMCPServerNames() map[string]bool {
	names := make(map[string]bool, len(f.configMCPServers()))
	for name := range f.configMCPServers() {
		names[name] = true
	}
	return names
}

func (f *FrontendAPI) configMCPServers() map[string]config.MCPServerConfig {
	if f.config == nil {
		return nil
	}
	return f.config.MCP.Servers
}

func copyCapabilityModesMap(m map[string]config.CapabilityModes) map[string]config.CapabilityModes {
	out := make(map[string]config.CapabilityModes, len(m))
	for name, modes := range m {
		out[name] = modes
	}
	return out
}

// GetCapabilityModes returns the normalized per-context activation modes for
// skills, agents and MCP servers: canonical enum values, non-nil sections.
// See specs/domains/capabilities.md.
func (f *FrontendAPI) GetCapabilityModes() CapabilitiesInfo {
	snap := f.capabilitiesSnapshot()
	return CapabilitiesInfo{
		Skills: capabilityModesInfo(snap.caps.Skills),
		Agents: capabilityModesInfo(snap.caps.Agents),
		MCP:    capabilityModesInfo(snap.mcp),
	}
}

// capabilityModesInfo renders a section for the wire: normalized values,
// non-nil map.
func capabilityModesInfo(m map[string]config.CapabilityModes) map[string]CapabilityModesInfo {
	out := make(map[string]CapabilityModesInfo, len(m))
	for name, modes := range m {
		out[name] = CapabilityModesInfo{
			ChatMode: config.NormalizeMCPServerMode(modes.ChatMode),
			CodeMode: config.NormalizeMCPServerMode(modes.CodeMode),
		}
	}
	return out
}

// UpdateCapabilityModes replaces one capabilities section wholesale (never
// merges): names absent from the payload fall back to "auto". Validation is
// the strict save-path gate; structural cleanup mirrors the load
// normalization; the edit is persisted and republished to the live builder,
// never a gateway reconfigure (mode edits are policy, not topology).
// See specs/domains/capabilities.md.
func (f *FrontendAPI) UpdateCapabilityModes(kind string, modes map[string]CapabilityModesInfo) error {
	converted := make(map[string]config.CapabilityModes, len(modes))
	for name, m := range modes {
		converted[name] = config.CapabilityModes{ChatMode: m.ChatMode, CodeMode: m.CodeMode}
	}
	var candidate config.CapabilitiesConfig
	switch kind {
	case CapabilitySectionSkills:
		candidate.Skills = converted
	case CapabilitySectionAgents:
		candidate.Agents = converted
	case CapabilitySectionMCP:
		candidate.MCP = converted
	default:
		return fmt.Errorf("unknown capability kind %q: must be %q, %q or %q",
			kind, CapabilitySectionSkills, CapabilitySectionAgents, CapabilitySectionMCP)
	}
	if err := config.ValidateCapabilities(&candidate); err != nil {
		return fmt.Errorf("invalid capability modes for %q: %w", kind, err)
	}

	f.saveMu.Lock()
	defer f.saveMu.Unlock()

	f.configMu.Lock()
	defer f.configMu.Unlock()

	if f.config == nil {
		return errors.New("config not initialized")
	}

	mcpServers := f.configMCPServerNames()
	cleaned := make(map[string]config.CapabilityModes, len(converted))
	for name, m := range converted {
		if kind == CapabilitySectionMCP {
			if _, known := mcpServers[name]; !known {
				continue // stale name: trimmed, mirroring the load path
			}
		}
		if m.ChatMode == "" && m.CodeMode == "" {
			continue // both-empty: no explicit value, dropped
		}
		cleaned[name] = config.CapabilityModes{
			ChatMode: config.NormalizeMCPServerMode(m.ChatMode),
			CodeMode: config.NormalizeMCPServerMode(m.CodeMode),
		}
	}
	if len(cleaned) == 0 {
		cleaned = nil // an empty section disappears from YAML entirely
	}
	switch kind {
	case CapabilitySectionSkills:
		f.config.Capabilities.Skills = cleaned
	case CapabilitySectionAgents:
		f.config.Capabilities.Agents = cleaned
	case CapabilitySectionMCP:
		f.config.Capabilities.MCP = cleaned
	}

	if err := f.persistConfig(); err != nil {
		f.log().Warn("failed to persist capability modes", "error", err)
	}

	// Publish to the live builder while configMu is still held: orchestrators
	// read modes from the builder's immutable snapshots at task entry, so
	// without this push a saved change would wait for the next app restart.
	// The push is a bounded in-memory write; no builder yet is a no-op —
	// construction projects the persisted section.
	if b := f.builder(); b != nil {
		b.UpdateCapabilities(f.toBuilderConfigLocked())
	}

	return nil
}
