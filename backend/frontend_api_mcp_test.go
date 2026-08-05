package backend

import (
	"testing"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/core/tools"
)

// --- UpdateMCPServers ---

func TestUpdateMCPServers_PersistsAndReconfigures(t *testing.T) {
	f, mock, _ := newTestAPI(t)
	// Initialize MCP config section.
	f.config.MCP = config.MCPConfig{
		Servers: make(map[string]config.MCPServerConfig),
	}

	err := f.UpdateMCPServers(map[string]config.MCPServerConfig{
		"test-server": {Command: "cmd", Args: []string{"--arg"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mock.reconfigureMCPCalls != 1 {
		t.Errorf("ReconfigureMCP called %d times, want 1", mock.reconfigureMCPCalls)
	}
	if _, ok := f.config.MCP.Servers["test-server"]; !ok {
		t.Error("test-server not persisted in config")
	}
}

func TestUpdateMCPServers_InvalidStdioNoCommand(t *testing.T) {
	f, mock, _ := newTestAPI(t)
	f.config.MCP = config.MCPConfig{
		Servers: make(map[string]config.MCPServerConfig),
	}

	err := f.UpdateMCPServers(map[string]config.MCPServerConfig{
		"bad": {Transport: "stdio", Command: ""},
	})
	if err == nil {
		t.Fatal("expected validation error for empty command")
	}
	if mock.reconfigureMCPCalls != 0 {
		t.Error("ReconfigureMCP should not be called on invalid input")
	}
}

func TestUpdateMCPServers_InvalidHTTPNoURL(t *testing.T) {
	f, _, _ := newTestAPI(t)
	f.config.MCP = config.MCPConfig{Servers: make(map[string]config.MCPServerConfig)}

	err := f.UpdateMCPServers(map[string]config.MCPServerConfig{
		"bad-http": {Transport: "http", URL: ""},
	})
	if err == nil {
		t.Fatal("expected validation error for empty URL on http transport")
	}
}

func TestUpdateMCPServers_UnsupportedTransport(t *testing.T) {
	f, _, _ := newTestAPI(t)
	f.config.MCP = config.MCPConfig{Servers: make(map[string]config.MCPServerConfig)}

	err := f.UpdateMCPServers(map[string]config.MCPServerConfig{
		"bad": {Transport: "grpc", Command: "cmd"},
	})
	if err == nil {
		t.Fatal("expected error for unsupported transport")
	}
}

func TestUpdateMCPServers_DeepCopy(t *testing.T) {
	f, _, _ := newTestAPI(t)
	f.config.MCP = config.MCPConfig{Servers: make(map[string]config.MCPServerConfig)}

	args := []string{"--port", "9000"}
	env := map[string]string{"KEY": "VAL"}
	err := f.UpdateMCPServers(map[string]config.MCPServerConfig{
		"srv": {Command: "cmd", Args: args, Env: env},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Mutate the caller's slice + map after the call.
	args[0] = "MUTATED"
	env["KEY"] = "MUTATED"

	stored := f.config.MCP.Servers["srv"]
	if stored.Args[0] == "MUTATED" {
		t.Error("stored Args[0] was mutated — deep copy missing")
	}
	if stored.Env["KEY"] == "MUTATED" {
		t.Error("stored Env['KEY'] was mutated — deep copy missing")
	}
}

// --- GetMCPServers ---

func TestGetMCPServers_DeepCopy(t *testing.T) {
	f, _, _ := newTestAPI(t)
	f.config.MCP = config.MCPConfig{
		Servers: map[string]config.MCPServerConfig{
			"srv": {Command: "cmd", Args: []string{"a"}, Env: map[string]string{"K": "V"}},
		},
	}

	got := f.GetMCPServers()
	got["srv"] = config.MCPServerConfig{Command: "HACK"} // mutate returned map

	// Internal config must NOT be affected.
	if f.config.MCP.Servers["srv"].Command != "cmd" {
		t.Error("GetMCPServers returned a live reference instead of a deep copy")
	}
}

// --- GetMCPStatus ---

// TestGetMCPStatus_NoApp verifies the nil-app early return.
//
// The remaining branches of GetMCPStatus (the non-blocking Starting-placeholder
// while MCPStartupDone()==false, the startup-error placeholder, and the live
// gateway status) are exercised indirectly through the core builder
// tests in core/builder_mcp_test.go (TestMCPStartupDone, TestMCPGatewayNoWait),
// which verify the exact builder methods Application.GetMCPStatus delegates to.
// Constructing an Application with MCPStartupDone()==false from this package is
// not feasible without a racy NewOrchestratorBuilder call: the mcpDone channel
// and gateway field are private to core, so the placeholder state cannot be set
// deterministically from the backend package.
func TestGetMCPStatus_NoApp(t *testing.T) {
	f := &FrontendAPI{} // f.app == nil
	got := f.GetMCPStatus()
	if got == nil {
		t.Fatal("expected non-nil empty slice")
	}
	if len(got) != 0 {
		t.Errorf("expected empty slice, got %d", len(got))
	}
}

// --- GetToolList ---

func TestGetToolList_NoApp(t *testing.T) {
	f := &FrontendAPI{} // f.app == nil
	got := f.GetToolList()
	if got == nil {
		t.Fatal("expected non-nil empty slice")
	}
	if len(got) != 0 {
		t.Errorf("expected empty slice when app is nil, got %d", len(got))
	}
}

func TestGetToolList_FiltersInternal(t *testing.T) {
	// This test verifies IsInternalTool filtering logic at the FrontendAPI level.
	// Internal tools (finish, tool_result_read, etc.) must never appear in the
	// returned list. We test the filter predicate directly since constructing a
	// full Application with registered tools requires heavyweight builder infra.
	internalTools := []string{"finish", "tool_result_read", "update_checklist", "read_step_output", "read_final_result"}
	for _, tool := range internalTools {
		if !tools.IsInternalTool(tool) {
			t.Errorf("IsInternalTool(%q) = false, want true", tool)
		}
	}
	externalTools := []string{"bash_exec", "read_file", "write_file", "web_search"}
	for _, tool := range externalTools {
		if tools.IsInternalTool(tool) {
			t.Errorf("IsInternalTool(%q) = true, want false", tool)
		}
	}
}

// --- resolveToolPolicy ---

func TestResolveToolPolicy_Override(t *testing.T) {
	f, _, _ := newTestAPI(t)
	f.config.Security.ToolPolicies = map[string]config.ToolPolicyConfig{
		"bash_exec": {Policy: "always_deny"},
	}
	f.config.Security.DefaultPolicy = "always_allow"

	if got := f.resolveToolPolicy("bash_exec"); got != "always_deny" {
		t.Errorf("per-tool override not used: got %q", got)
	}
}

func TestResolveToolPolicy_FallsBackToDefault(t *testing.T) {
	f, _, _ := newTestAPI(t)
	f.config.Security.ToolPolicies = nil
	f.config.Security.DefaultPolicy = "always_allow"

	if got := f.resolveToolPolicy("bash_exec"); got != "always_allow" {
		t.Errorf("fallback to default not used: got %q", got)
	}
}

func TestResolveToolPolicy_UltimateDefault(t *testing.T) {
	f, _, _ := newTestAPI(t)
	f.config.Security.ToolPolicies = nil
	f.config.Security.DefaultPolicy = ""

	if got := f.resolveToolPolicy("bash_exec"); got != "user_confirm" {
		t.Errorf("ultimate default not applied: got %q", got)
	}
}

// --- validateMCPServerConfig ---

func TestValidateMCPServerConfig(t *testing.T) {
	tests := []struct {
		name    string
		cfg     config.MCPServerConfig
		wantErr bool
	}{
		{name: "stdio valid", cfg: config.MCPServerConfig{Command: "cmd"}, wantErr: false},
		{name: "stdio no command", cfg: config.MCPServerConfig{Transport: "stdio"}, wantErr: true},
		{name: "http valid", cfg: config.MCPServerConfig{Transport: "http", URL: "http://x"}, wantErr: false},
		{name: "http no url", cfg: config.MCPServerConfig{Transport: "http"}, wantErr: true},
		{name: "unknown transport", cfg: config.MCPServerConfig{Transport: "grpc"}, wantErr: true},
		{name: "default transport needs command", cfg: config.MCPServerConfig{}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateMCPServerConfig("test", tt.cfg)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateMCPServerConfig = error %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
