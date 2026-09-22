package config

import (
	"strings"
	"testing"
)

// TestParseMCPDuration pins the single duration rule shared by the load path,
// the UI save path and the builder adapter: empty is valid (the engine
// default), a positive Go duration parses, and anything else is rejected.
func TestParseMCPDuration(t *testing.T) {
	cases := []struct {
		raw     string
		wantErr bool
	}{
		{"", false},
		{"30s", false},
		{"2m", false},
		{"500ms", false},
		{"0s", true},
		{"-5s", true},
		{"abc", true},
	}
	for _, tc := range cases {
		if _, err := ParseMCPDuration(tc.raw); (err != nil) != tc.wantErr {
			t.Errorf("ParseMCPDuration(%q) err = %v, wantErr %v", tc.raw, err, tc.wantErr)
		}
	}
}

func TestNormalizeMCPTimeouts(t *testing.T) {
	cfg := &Config{}
	cfg.MCP.Servers = map[string]MCPServerConfig{
		"valid":    {Command: "cmd", Timeout: "30s", CallTimeout: "2m"},
		"empty":    {Command: "cmd"},
		"invalid":  {Command: "cmd", Timeout: "abc"},
		"negative": {Command: "cmd", CallTimeout: "-5s"},
	}

	warnings := normalizeMCPTimeouts(cfg)
	if len(warnings) != 2 {
		t.Fatalf("warnings = %v, want exactly 2 (invalid.timeout, negative.call_timeout)", warnings)
	}
	joined := strings.Join(warnings, "\n")
	if !strings.Contains(joined, "mcp.servers.invalid.timeout") {
		t.Errorf("missing invalid.timeout warning: %v", warnings)
	}
	if !strings.Contains(joined, "mcp.servers.negative.call_timeout") {
		t.Errorf("missing negative.call_timeout warning: %v", warnings)
	}
	// Fail-soft: the raw value is left untouched for the adapter to resolve.
	if cfg.MCP.Servers["invalid"].Timeout != "abc" {
		t.Errorf("invalid.Timeout = %q, want it left as-is", cfg.MCP.Servers["invalid"].Timeout)
	}
}

func TestNormalizeMCPTimeouts_ValidHasNoWarnings(t *testing.T) {
	cfg := &Config{}
	cfg.MCP.Servers = map[string]MCPServerConfig{
		"valid": {Command: "cmd", Timeout: "30s", CallTimeout: "2m"},
		"empty": {Command: "cmd"},
	}
	if w := normalizeMCPTimeouts(cfg); len(w) != 0 {
		t.Errorf("warnings = %v, want none", w)
	}
}

// TestLoadWithResult_InvalidMCPTimeoutWarns pins the C2 fix: a hand-edited
// config with a typo'd MCP duration must surface the fail-soft warning through
// the load path (LoadErrors → the UI's configLoadErrors), which is the only
// place the warning can reach the user in production (the frontend
// config→builder rebuild paths call the adapter without a logger).
func TestLoadWithResult_InvalidMCPTimeoutWarns(t *testing.T) {
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
      timeout: "abc"
      call_timeout: "0s"
`
	result, err := LoadWithResult(writeTestConfig(t, content))
	if err != nil {
		t.Fatalf("LoadWithResult() failed: %v", err)
	}
	joined := strings.Join(result.LoadErrors, "\n")
	if !strings.Contains(joined, "mcp.servers.broken.timeout") {
		t.Errorf("LoadErrors must warn about mcp.servers.broken.timeout, got %v", result.LoadErrors)
	}
	if !strings.Contains(joined, "mcp.servers.broken.call_timeout") {
		t.Errorf("LoadErrors must warn about mcp.servers.broken.call_timeout, got %v", result.LoadErrors)
	}
	// The invalid value is preserved for the adapter to fail soft on.
	if got := result.Config.MCP.Servers["broken"].Timeout; got != "abc" {
		t.Errorf("broken.Timeout = %q, want it left as-is", got)
	}
}
