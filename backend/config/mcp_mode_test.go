package config

import (
	"strings"
	"testing"
)

// TestValidMCPServerMode pins the closed mode enum shared by the load path,
// the UI save path and the status/mentionable RPCs: exactly the three
// canonical values are recognized; empty is the "unset" signal and is
// deliberately not valid (callers resolve it to the default themselves).
func TestValidMCPServerMode(t *testing.T) {
	for _, mode := range []string{MCPServerModeAuto, MCPServerModeManual, MCPServerModeDisabled} {
		if !ValidMCPServerMode(mode) {
			t.Errorf("ValidMCPServerMode(%q) = false, want true", mode)
		}
	}
	for _, mode := range []string{"", "Auto", "AUTO", "on", "off", "enabled", "weird"} {
		if ValidMCPServerMode(mode) {
			t.Errorf("ValidMCPServerMode(%q) = true, want false", mode)
		}
	}
}

// TestNormalizeMCPServerMode pins the canonicalization rule: recognized
// values pass through; empty and unrecognized values resolve to the default
// "auto" — silently, because distinguishing a silent default (empty) from a
// reported fallback (invalid) is the load path's job.
func TestNormalizeMCPServerMode(t *testing.T) {
	cases := []struct{ raw, want string }{
		{MCPServerModeAuto, MCPServerModeAuto},
		{MCPServerModeManual, MCPServerModeManual},
		{MCPServerModeDisabled, MCPServerModeDisabled},
		{"", MCPServerModeAuto},
		{"weird", MCPServerModeAuto},
	}
	for _, tc := range cases {
		if got := NormalizeMCPServerMode(tc.raw); got != tc.want {
			t.Errorf("NormalizeMCPServerMode(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}
}

func TestNormalizeMCPModes(t *testing.T) {
	cfg := &Config{}
	cfg.MCP.Servers = map[string]MCPServerConfig{
		"valid":   {Command: "cmd", Mode: MCPServerModeManual},
		"empty":   {Command: "cmd"},
		"off":     {Command: "cmd", Mode: MCPServerModeDisabled},
		"invalid": {Command: "cmd", Mode: "weird"},
	}

	warnings := normalizeMCPModes(cfg)
	if len(warnings) != 1 {
		t.Fatalf("warnings = %v, want exactly 1 (invalid.mode)", warnings)
	}
	if !strings.Contains(warnings[0], "mcp.servers.invalid.mode") {
		t.Errorf("missing invalid.mode warning: %v", warnings)
	}
	// The mode is REWRITTEN to the canonical enum (unlike timeouts, which
	// leave the raw string for the adapter to resolve): every consumer
	// downstream of the load pipeline sees a canonical value.
	if got := cfg.MCP.Servers["invalid"].Mode; got != MCPServerModeAuto {
		t.Errorf("invalid.Mode = %q, want %q (reset to the default)", got, MCPServerModeAuto)
	}
	if got := cfg.MCP.Servers["empty"].Mode; got != MCPServerModeAuto {
		t.Errorf("empty.Mode = %q, want %q (silent default)", got, MCPServerModeAuto)
	}
	// Canonical values pass through untouched.
	if got := cfg.MCP.Servers["valid"].Mode; got != MCPServerModeManual {
		t.Errorf("valid.Mode = %q, want it preserved", got)
	}
	if got := cfg.MCP.Servers["off"].Mode; got != MCPServerModeDisabled {
		t.Errorf("off.Mode = %q, want it preserved", got)
	}
}

func TestNormalizeMCPModes_ValidHasNoWarnings(t *testing.T) {
	cfg := &Config{}
	cfg.MCP.Servers = map[string]MCPServerConfig{
		"valid": {Command: "cmd", Mode: MCPServerModeAuto},
		"empty": {Command: "cmd"},
	}
	if w := normalizeMCPModes(cfg); len(w) != 0 {
		t.Errorf("warnings = %v, want none", w)
	}
}

// TestLoadWithResult_InvalidMCPModeWarns pins the fail-soft load contract
// (mirroring the timeout rule): a hand-edited config with an unrecognized
// mode must surface a warning through the load path (LoadErrors → the UI's
// configLoadErrors) AND canonicalize the value to "auto" in place — a bad
// value can never prevent a server from starting, and every downstream
// consumer sees a canonical enum value.
func TestLoadWithResult_InvalidMCPModeWarns(t *testing.T) {
	content := `
llm:
  default_model: claude-3-haiku
  anthropic:
    api_key: "test-key"
    models:
      - claude-3-haiku
mcp:
  servers:
    broken:
      command: cmd
      mode: "weird"
    fine:
      command: cmd
      mode: "manual"
`
	result, err := LoadWithResult(writeTestConfig(t, content))
	if err != nil {
		t.Fatalf("LoadWithResult() failed: %v", err)
	}
	joined := strings.Join(result.LoadErrors, "\n")
	if !strings.Contains(joined, `mcp.servers.broken.mode`) {
		t.Errorf("LoadErrors must warn about mcp.servers.broken.mode, got %v", result.LoadErrors)
	}
	if strings.Contains(joined, "mcp.servers.fine.mode") {
		t.Errorf("LoadErrors must not warn about the valid fine.mode, got %v", result.LoadErrors)
	}
	// Fail-soft: the invalid mode is reset to the canonical default.
	if got := result.Config.MCP.Servers["broken"].Mode; got != MCPServerModeAuto {
		t.Errorf("broken.Mode = %q, want %q", got, MCPServerModeAuto)
	}
	// A valid mode survives the load pipeline.
	if got := result.Config.MCP.Servers["fine"].Mode; got != MCPServerModeManual {
		t.Errorf("fine.Mode = %q, want %q preserved", got, MCPServerModeManual)
	}
}

// TestLoadWithResult_EmptyModeDefaultsSilently pins that an omitted mode is
// the accepted default, not a warning: every server that predates the mode
// key (i.e. every existing config) must load warning-free with mode "auto".
func TestLoadWithResult_EmptyModeDefaultsSilently(t *testing.T) {
	content := `
llm:
  default_model: claude-3-haiku
  anthropic:
    api_key: "test-key"
    models:
      - claude-3-haiku
mcp:
  servers:
    legacy:
      command: cmd
`
	result, err := LoadWithResult(writeTestConfig(t, content))
	if err != nil {
		t.Fatalf("LoadWithResult() failed: %v", err)
	}
	for _, w := range result.LoadErrors {
		if strings.Contains(w, "mcp.servers.legacy.mode") {
			t.Errorf("empty mode must not warn, got %q", w)
		}
	}
	if got := result.Config.MCP.Servers["legacy"].Mode; got != MCPServerModeAuto {
		t.Errorf("legacy.Mode = %q, want %q (canonicalized default)", got, MCPServerModeAuto)
	}
}
