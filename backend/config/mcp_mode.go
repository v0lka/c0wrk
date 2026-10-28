package config

import (
	"fmt"
	"sort"
)

// Capability activation modes: the canonical enum behind both
// `capabilities.*.{chat_mode,code_mode}` slots and the legacy per-server
// `mcp.servers.<name>.mode` key (specs/domains/capabilities.md).
const (
	// MCPServerModeAuto — the default (also the resolved value for an
	// omitted/empty key): dialed at gateway start/reconfigure, advertised
	// everywhere.
	MCPServerModeAuto = "auto"
	// MCPServerModeManual — stays dialed; advertised only after a
	// `/`-mention enables it task-wide.
	MCPServerModeManual = "manual"
	// MCPServerModeDisabled — excluded from that context's availability; the
	// dial decision is shared across contexts, so the server is skipped only
	// when disabled in both.
	MCPServerModeDisabled = "disabled"
)

// ValidMCPServerMode reports whether raw is a recognized mode. Empty is
// deliberately NOT valid — it is the "unset, resolve to auto" signal
// (silently on load, accepted default on the UI save path). The closed-enum
// contract shared by every consumer: specs/domains/capabilities.md.
func ValidMCPServerMode(raw string) bool {
	switch raw {
	case MCPServerModeAuto, MCPServerModeManual, MCPServerModeDisabled:
		return true
	}
	return false
}

// NormalizeMCPServerMode resolves a raw mode value to the canonical enum:
// recognized values pass through, empty and unrecognized resolve to the
// default "auto". It never warns — distinguishing a silent default (empty)
// from a reported fallback (invalid) is the load path's job.
func NormalizeMCPServerMode(raw string) string {
	if !ValidMCPServerMode(raw) {
		return MCPServerModeAuto
	}
	return raw
}

// migrateLegacyMCPServerModes folds the legacy per-server activation key
// (mcp.servers.<name>.mode) into capabilities.mcp.<name> — both contexts, an
// unrecognized value reported as a load warning and folded as "auto", an
// explicit entry outranking the legacy key (specs/domains/capabilities.md).
// The Mode field is ALWAYS reset to "" afterwards: left set it would
// resurrect the legacy key in YAML on the next save. Fail-soft; must run
// after ApplyDefaults and before validate; warnings surface through
// configLoadErrors, mirroring normalizeMCPTimeouts.
func migrateLegacyMCPServerModes(cfg *Config) []string {
	if len(cfg.MCP.Servers) == 0 {
		return nil
	}
	// Iterate server names in sorted order so the warning order — surfaced
	// verbatim in the UI — is deterministic.
	names := make([]string, 0, len(cfg.MCP.Servers))
	for name := range cfg.MCP.Servers {
		names = append(names, name)
	}
	sort.Strings(names)

	var warnings []string
	for _, name := range names {
		srv := cfg.MCP.Servers[name]
		switch {
		case srv.Mode == "":
			// The config predates the mode key (or has already migrated):
			// nothing to fold; the erase below is a no-op.
		case ValidMCPServerMode(srv.Mode):
			foldLegacyMCPMode(cfg, name, srv.Mode)
		default:
			warnings = append(warnings, fmt.Sprintf(
				"mcp.servers.%s.mode ignored (falling back to %q): unrecognized mode %q",
				name, MCPServerModeAuto, srv.Mode))
			foldLegacyMCPMode(cfg, name, MCPServerModeAuto)
		}
		srv.Mode = ""               // ALWAYS erase the legacy key (see doc comment).
		cfg.MCP.Servers[name] = srv // map values are not addressable
	}
	return warnings
}

// foldLegacyMCPMode copies a legacy per-server mode into
// capabilities.mcp.<name> unless the entry already carries an explicit value
// — an operator-authored entry always outranks the legacy key. The fold is
// all-or-nothing per entry: a partially authored entry keeps its other
// context at the "auto" default rather than inheriting the legacy mode.
func foldLegacyMCPMode(cfg *Config, name, mode string) {
	if cfg.Capabilities.MCP == nil {
		cfg.Capabilities.MCP = make(map[string]CapabilityModes)
	}
	cur, ok := cfg.Capabilities.MCP[name]
	if ok && (cur.ChatMode != "" || cur.CodeMode != "") {
		return
	}
	cfg.Capabilities.MCP[name] = CapabilityModes{ChatMode: mode, CodeMode: mode}
}

// normalizeCapabilities canonicalizes every capabilities entry in place,
// fail-soft: empty resolves to "auto" silently, an unrecognized value is
// reported and rewritten in place so every downstream consumer sees a
// canonical enum (specs/domains/capabilities.md). Two silent structural
// cleanups run alongside: a capabilities.mcp name absent from mcp.servers is
// stale and removed, and a both-empty entry carries no information and is
// removed — in every section. Must run after migrateLegacyMCPServerModes and
// ApplyDefaults, before validate.
func normalizeCapabilities(cfg *Config) []string {
	var warnings []string
	for _, section := range capabilitySections(cfg) {
		if len(section.entries) == 0 {
			continue
		}
		for _, name := range sortedCapabilityNames(section.entries) {
			if section.name == capSectionMCP {
				if _, known := cfg.MCP.Servers[name]; !known {
					delete(section.entries, name) // stale: silently dropped
					continue
				}
			}
			entry := section.entries[name]
			if entry.ChatMode == "" && entry.CodeMode == "" {
				delete(section.entries, name) // meaningless: silently dropped
				continue
			}
			for _, field := range []struct{ key, value string }{
				{"chat_mode", entry.ChatMode},
				{"code_mode", entry.CodeMode},
			} {
				switch {
				case field.value == "":
					// Silent: empty IS the default.
					if field.key == "chat_mode" {
						entry.ChatMode = MCPServerModeAuto
					} else {
						entry.CodeMode = MCPServerModeAuto
					}
				case ValidMCPServerMode(field.value):
					// Already canonical — nothing to do.
				default:
					warnings = append(warnings, fmt.Sprintf(
						"%s.%s.%s ignored (falling back to %q): unrecognized mode %q",
						section.label, name, field.key, MCPServerModeAuto, field.value))
					if field.key == "chat_mode" {
						entry.ChatMode = MCPServerModeAuto
					} else {
						entry.CodeMode = MCPServerModeAuto
					}
				}
			}
			section.entries[name] = entry // map values are not addressable
		}
	}
	return warnings
}

// The capabilities section identifiers, used for both the load-warning paths
// and the save-path validation errors.
const (
	capSectionSkills = "skills"
	capSectionAgents = "agents"
	capSectionMCP    = "mcp"
)

// capabilitySections returns the three capabilities sections in the fixed
// order the load normalization walks them and load warnings surface them.
func capabilitySections(cfg *Config) []struct {
	name    string
	label   string
	entries map[string]CapabilityModes
} {
	return []struct {
		name    string
		label   string
		entries map[string]CapabilityModes
	}{
		{capSectionSkills, "capabilities.skills", cfg.Capabilities.Skills},
		{capSectionAgents, "capabilities.agents", cfg.Capabilities.Agents},
		{capSectionMCP, "capabilities.mcp", cfg.Capabilities.MCP},
	}
}

func sortedCapabilityNames(m map[string]CapabilityModes) []string {
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// ValidateCapabilities strictly validates a capabilities section for a SAVE
// path: every non-empty chat_mode / code_mode must be a recognized mode
// (the same closed enum the load path normalizes fail-soft —
// specs/domains/capabilities.md); empty is the "auto" default everywhere.
// Structural state (stale capabilities.mcp names, both-empty entries) is
// deliberately NOT a save error — the load normalization cleans it up.
// Invalid values are rejected here because Save does not run validate(): a
// value stored past this gate would surface only at the next launch, where
// the failed load discards the whole config.
func ValidateCapabilities(c *CapabilitiesConfig) error {
	for _, section := range capabilitySections(&Config{Capabilities: *c}) {
		for _, name := range sortedCapabilityNames(section.entries) {
			entry := section.entries[name]
			for _, field := range []struct{ key, value string }{
				{"chat_mode", entry.ChatMode},
				{"code_mode", entry.CodeMode},
			} {
				if field.value == "" || ValidMCPServerMode(field.value) {
					continue
				}
				return fmt.Errorf("%s.%s: invalid %s %q: must be %q, %q or %q",
					section.label, name, field.key, field.value,
					MCPServerModeAuto, MCPServerModeManual, MCPServerModeDisabled)
			}
		}
	}
	return nil
}
