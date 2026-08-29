package backend

import (
	"testing"

	"github.com/v0lka/c0wrk/backend/config"
	sdktools "github.com/v0lka/sp4rk/tools"
	"github.com/v0lka/sp4rk/tools/mcp"
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

// --- GetMCPStatus merge (configured servers always visible) ---

// TestMergeConfiguredMCPServers verifies the pure core of the
// "configured servers are always visible" contract: every configured name
// missing from the live gateway status is appended as a disconnected entry
// with a defaulted transport, and the result stays sorted by name.
func TestMergeConfiguredMCPServers(t *testing.T) {
	status := []mcp.ServerStatus{
		{Name: "live", Transport: "stdio", Connected: true, Tools: []string{}},
		{Name: "dead", Transport: "http", Connected: false, Tools: []string{}, Error: "conn refused"},
	}
	configured := map[string]config.MCPServerConfig{
		"live":    {Transport: "stdio", Command: "cmd"},
		"dead":    {Transport: "http", URL: "http://x"},
		"unknown": {Command: "cmd"}, // transport empty → defaults to stdio
		"remote":  {Transport: "http", URL: "http://y"},
	}

	got := mergeConfiguredMCPServers(status, configured)

	if len(got) != 4 {
		t.Fatalf("expected 4 entries, got %d: %+v", len(got), got)
	}
	// Sorted by name: dead, live, remote, unknown.
	wantOrder := []string{"dead", "live", "remote", "unknown"}
	for i, want := range wantOrder {
		if got[i].Name != want {
			t.Errorf("got[%d].Name = %q, want %q", i, got[i].Name, want)
		}
	}
	// Existing entries must pass through untouched.
	if !got[1].Connected || got[1].Name != "live" {
		t.Errorf("live server status mutated: %+v", got[1])
	}
	// Missing configured servers are synthesized as disconnected.
	unknown := got[3]
	if unknown.Connected || unknown.Error != "unavailable" || unknown.Transport != "stdio" {
		t.Errorf("unknown = %+v, want disconnected stdio entry with error", unknown)
	}
	remote := got[2]
	if remote.Connected || remote.Transport != "http" {
		t.Errorf("remote = %+v, want disconnected http entry", remote)
	}
}

// TestMergeConfiguredMCPServers_EmptyConfig verifies that an empty (or nil)
// configuration leaves the gateway status untouched.
func TestMergeConfiguredMCPServers_EmptyConfig(t *testing.T) {
	status := []mcp.ServerStatus{{Name: "live", Connected: true}}
	if got := mergeConfiguredMCPServers(status, nil); len(got) != 1 {
		t.Errorf("expected untouched status for nil config, got %+v", got)
	}
	if got := mergeConfiguredMCPServers(status, map[string]config.MCPServerConfig{}); len(got) != 1 {
		t.Errorf("expected untouched status for empty config, got %+v", got)
	}
}

// TestIsMCPStartupPlaceholder pins the placeholder detection that suppresses
// the config merge while the gateway is still starting.
func TestIsMCPStartupPlaceholder(t *testing.T) {
	starting := []mcp.ServerStatus{{Name: "_gateway", Starting: true}}
	if !isMCPStartupPlaceholder(starting) {
		t.Error("starting placeholder not detected")
	}
	failed := []mcp.ServerStatus{{Name: "_gateway", Error: "boom"}}
	if isMCPStartupPlaceholder(failed) {
		t.Error("startup-error placeholder must not be treated as starting (config merge must apply)")
	}
	live := []mcp.ServerStatus{{Name: "srv", Connected: true}}
	if isMCPStartupPlaceholder(live) {
		t.Error("live status must not be treated as placeholder")
	}
	if isMCPStartupPlaceholder(nil) {
		t.Error("nil status must not be treated as placeholder")
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

func TestGetToolList_BuildToolInfos(t *testing.T) {
	// buildToolInfos is the pure core of GetToolList (the full method needs a
	// live Application with heavyweight builder infra). It must skip
	// system-group tools BY GROUP, label every tool with its group, and
	// resolve the effective policy from the live registry map.
	descriptors := []sdktools.ToolDescriptor{
		{Name: "bash_exec", Description: "shell", Source: "core", Group: sdktools.GroupExecute},
		{Name: "read_file", Description: "read", Source: "core", Group: sdktools.GroupLocalRead},
		{Name: "finish", Description: "internal", Source: "core", Group: sdktools.GroupSystem},
		{Name: "mcp_query", Description: "mcp tool", Source: "mcp:test-server", Group: sdktools.GroupLocalMCP},
		{Name: "web_fetch", Description: "web", Source: "core", Group: sdktools.GroupRemoteRead},
	}
	policies := map[sdktools.ToolGroup]sdktools.ToolPolicy{
		sdktools.GroupExecute:   sdktools.PolicyAlwaysDeny,
		sdktools.GroupLocalRead: sdktools.PolicyAlwaysAllow,
		sdktools.GroupLocalMCP:  sdktools.PolicyUserConfirm,
		// GroupRemoteRead deliberately absent → fail-safe user_confirm.
	}

	got := buildToolInfos(descriptors, policies)

	byName := make(map[string]ToolInfo, len(got))
	for _, info := range got {
		byName[info.Name] = info
	}
	if _, ok := byName["finish"]; ok {
		t.Error("system-group tool 'finish' must be filtered out by group")
	}
	if len(got) != 4 {
		t.Fatalf("expected 4 tools after filtering, got %d: %+v", len(got), got)
	}

	exec := byName["bash_exec"]
	if exec.Group != "execute" || exec.Policy != "deny" {
		t.Errorf("bash_exec = group %q policy %q, want execute/deny", exec.Group, exec.Policy)
	}
	read := byName["read_file"]
	if read.Group != "local_read" || read.Policy != "allow" {
		t.Errorf("read_file = group %q policy %q, want local_read/allow", read.Group, read.Policy)
	}
	mcpTool := byName["mcp_query"]
	if mcpTool.Group != "local_mcp" || mcpTool.Policy != "user_confirm" {
		t.Errorf("mcp_query = group %q policy %q, want local_mcp/user_confirm", mcpTool.Group, mcpTool.Policy)
	}
	// A group without a registry entry fails safe to user_confirm.
	web := byName["web_fetch"]
	if web.Policy != "user_confirm" {
		t.Errorf("web_fetch policy = %q, want fail-safe user_confirm", web.Policy)
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
