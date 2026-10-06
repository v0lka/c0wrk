package config

import (
	"fmt"
	"sort"
)

// Per-server MCP activation modes (`mcp.servers.<name>.mode`).
const (
	// MCPServerModeAuto connects the server whenever the MCP gateway starts
	// or reconfigures — the behavior of every server before the mode existed
	// and the effective value for an omitted or empty key.
	MCPServerModeAuto = "auto"
	// MCPServerModeManual keeps the server's configuration live and surfaces
	// it as mentionable (GetMCPMentionableServers) for the chat-input
	// completion / send flow instead of treating it as always-on.
	MCPServerModeManual = "manual"
	// MCPServerModeDisabled never dials the server: it is skipped entirely
	// when the gateway config is built, so no process is spawned and no
	// connection is opened — neither at startup nor on reconfigure.
	MCPServerModeDisabled = "disabled"
)

// ValidMCPServerMode reports whether raw is a recognized per-server mode.
// Empty is deliberately NOT valid here — it is the "unset, use the default"
// signal that callers resolve to auto themselves (silently on load, as the
// accepted default on the UI save path).
//
// Together with NormalizeMCPServerMode it is the single source of truth for
// the mode enum, shared by the load-time normalization (normalizeMCPModes),
// the UI save-path validation (validateMCPServerConfig) and the status /
// mentionable RPCs, so all of them agree on exactly which values are valid.
func ValidMCPServerMode(raw string) bool {
	switch raw {
	case MCPServerModeAuto, MCPServerModeManual, MCPServerModeDisabled:
		return true
	}
	return false
}

// NormalizeMCPServerMode resolves a raw mode value to the canonical enum:
// a recognized value passes through, empty and unrecognized values resolve
// to the default "auto". It never warns — distinguishing a silent default
// (empty) from a reported fallback (invalid) is the load path's job
// (normalizeMCPModes).
func NormalizeMCPServerMode(raw string) string {
	if !ValidMCPServerMode(raw) {
		return MCPServerModeAuto
	}
	return raw
}

// normalizeMCPModes canonicalizes every configured MCP server's `mode` in
// place and returns a load warning for each invalid one. It is FAIL-SOFT: an
// unrecognized mode is reported, never fatal, and reset to the default
// "auto" — so a hand-edited typo can never prevent a server from starting.
// Unlike the timeout normalization (which leaves the raw string untouched for
// the adapter to resolve), the mode is REWRITTEN in place: the enum is
// closed, so every consumer downstream of the load pipeline (the config→
// builder adapter, the status merge, the mentionable RPCs, the frontend)
// sees a canonical value without re-normalizing. Empty is the default, not
// an error — it resolves to "auto" silently; only a non-empty unrecognized
// value warns. The load pipeline is the single surfacing point, mirroring
// normalizeMCPTimeouts: it feeds the UI's configLoadErrors channel.
//
// Must run after ApplyDefaults and before validate, mirroring
// normalizeMCPTimeouts.
func normalizeMCPModes(cfg *Config) []string {
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
			srv.Mode = MCPServerModeAuto // silent: empty IS the default
		case ValidMCPServerMode(srv.Mode):
			// Already canonical — nothing to do.
		default:
			warnings = append(warnings, fmt.Sprintf(
				"mcp.servers.%s.mode ignored (falling back to %q): unrecognized mode %q",
				name, MCPServerModeAuto, srv.Mode))
			srv.Mode = MCPServerModeAuto
		}
		cfg.MCP.Servers[name] = srv // map values are not addressable
	}
	return warnings
}
