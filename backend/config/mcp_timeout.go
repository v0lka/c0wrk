package config

import (
	"errors"
	"fmt"
	"sort"
	"time"
)

// ParseMCPDuration parses an MCP server duration string (`timeout` /
// `call_timeout`) into a time.Duration: empty resolves to 0 — the "unset,
// fall back to the engine default" signal — and a non-empty value must parse
// as a positive Go duration. The shared duration rule:
// specs/decisions/063-mcp-tool-timeouts.md.
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
// `call_timeout` and returns a load warning for each invalid one — fail-soft,
// the raw string left untouched (the config→builder adapter resolves it to 0
// at build time), so a hand-edited typo can never prevent a server from
// starting (specs/decisions/063-mcp-tool-timeouts.md). The load pipeline is
// the single production surfacing point: it feeds the UI's configLoadErrors
// channel and logs each warning at WARN, while the builder adapter logs
// nothing — a bad duration yields exactly one WARN line. Must run after
// ApplyDefaults and before validate, mirroring normalizeShellExec.
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
