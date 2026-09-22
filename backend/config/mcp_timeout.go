package config

import (
	"errors"
	"fmt"
	"sort"
	"time"
)

// ParseMCPDuration parses an MCP server duration string (`timeout` /
// `call_timeout`) into a time.Duration. An empty string resolves to 0 — the
// "unset, fall back to the engine default" signal. A non-empty value must parse
// as a positive Go duration.
//
// It is the single source of truth for the MCP-duration rule, shared by the
// load-time warning (normalizeMCPTimeouts), the UI save-path validation
// (validateMCPServerConfig) and the config→builder adapter (parseMCPDuration),
// so all three agree on exactly which values are valid.
func ParseMCPDuration(raw string) (time.Duration, error) {
	if raw == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("expected a Go duration such as \"30s\": %w", err)
	}
	if d <= 0 {
		return 0, errors.New("must be positive")
	}
	return d, nil
}

// normalizeMCPTimeouts validates every configured MCP server's `timeout` /
// `call_timeout` and returns a load warning for each invalid one. It is
// FAIL-SOFT: a bad duration is reported, never fatal, and the raw string is
// left untouched — the config→builder adapter resolves it to 0 (the engine
// default) at build time — so a hand-edited typo can never prevent a server
// from starting. The load pipeline is the SINGLE production surfacing point: it
// is what feeds the UI's `configLoadErrors` channel, which the builder adapter
// cannot reach. Concretely, config.ResolveAndLoad calls LoadWithResult and then
// both copies the returned LoadResult.LoadErrors into configLoadErrors and logs
// each at WARN — so the ADR-063 / mcp-gateway "logged warning" claim holds on
// the production path. The builder adapter (ToBuilderConfig → parseMCPDuration)
// logs nothing for an invalid value, so a bad duration yields exactly one WARN
// line, not two. (config.Load is the error-only convenience wrapper and drops
// these warnings, so callers that must surface them go through
// LoadWithResult/ResolveAndLoad; the frontend config→builder rebuild paths —
// frontend_api_config.go / frontend_api_mcp.go — start from an already-validated
// config, so no warning is needed there.)
//
// Must run after ApplyDefaults and before validate, mirroring normalizeShellExec.
func normalizeMCPTimeouts(cfg *Config) []string {
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
		// `call_timeout` inherits `timeout` (which itself defaults to 60s), so the
		// warning names the fallback that actually applies.
		callFallback := "the built-in 60s default"
		if _, err := ParseMCPDuration(srv.Timeout); err == nil && srv.Timeout != "" {
			callFallback = `the resolved "timeout"`
		}
		for _, f := range []struct{ field, raw, fallback string }{
			{"timeout", srv.Timeout, "the built-in 60s default"},
			{"call_timeout", srv.CallTimeout, callFallback},
		} {
			if _, err := ParseMCPDuration(f.raw); err != nil {
				warnings = append(warnings, fmt.Sprintf(
					"mcp.servers.%s.%s ignored (falling back to %s): %v (%q)",
					name, f.field, f.fallback, err, f.raw))
			}
		}
	}
	return warnings
}
