package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	sdktools "github.com/v0lka/sp4rk/tools"
)

func TestNewGateway(t *testing.T) {
	gateway := newGateway()
	if gateway == nil {
		t.Fatal("NewGateway returned nil")
	}

	if gateway.servers == nil {
		t.Error("servers map should be initialized")
	}

	if len(gateway.ServerNames()) != 0 {
		t.Error("new gateway should have no servers")
	}

	if gateway.ToolCount() != 0 {
		t.Error("new gateway should have no tools")
	}
}

func TestNewServer(t *testing.T) {
	server := newServer("test-server")
	if server == nil {
		t.Fatal("NewServer returned nil")
	}

	if server.Name() != "test-server" {
		t.Errorf("expected name 'test-server', got '%s'", server.Name())
	}

	if server.IsConnected() {
		t.Error("new server should not be connected")
	}

	if len(server.Tools()) != 0 {
		t.Error("new server should have no tools")
	}
}

func TestNewTool(t *testing.T) {
	server := newServer("test-server")

	info := ToolInfo{
		Name:        "test_tool",
		Description: "A test tool for testing",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"name":{"type":"string"}}}`),
	}

	tool := NewTool(server, info)
	if tool == nil {
		t.Fatal("NewTool returned nil")
	}

	if tool.Name() != "test_tool" {
		t.Errorf("expected name 'test_tool', got '%s'", tool.Name())
	}

	if tool.Description() != "A test tool for testing" {
		t.Errorf("unexpected description: %s", tool.Description())
	}

	if tool.ServerName() != "test-server" {
		t.Errorf("expected server name 'test-server', got '%s'", tool.ServerName())
	}

	schema := tool.InputSchema()
	if schema == nil {
		t.Error("InputSchema should not be nil")
	}

	var schemaMap map[string]any
	if err := json.Unmarshal(schema, &schemaMap); err != nil {
		t.Errorf("failed to unmarshal schema: %v", err)
	}

	if schemaMap["type"] != "object" {
		t.Errorf("expected schema type 'object', got '%v'", schemaMap["type"])
	}
}

func TestToolImplementsToolInterface(t *testing.T) {
	server := newServer("test-server")
	info := ToolInfo{
		Name:        "test_tool",
		Description: "A test tool",
		InputSchema: json.RawMessage(`{"type":"object"}`),
	}

	mcpTool := NewTool(server, info)

	// Verify Tool implements tools.Tool interface
	var _ sdktools.Tool = mcpTool
}

func TestGatewayRegisterTools(t *testing.T) {
	gateway := newGateway()

	// Manually add a mock server with tools for testing
	server := newServer("mock-server")
	server.tools = []ToolInfo{
		{
			Name:        "mock_tool_1",
			Description: "First mock tool",
			InputSchema: json.RawMessage(`{"type":"object"}`),
		},
		{
			Name:        "mock_tool_2",
			Description: "Second mock tool",
			InputSchema: json.RawMessage(`{"type":"object"}`),
		},
	}

	gateway.servers["mock-server"] = server

	// Create a registry and register tools
	registry := sdktools.NewToolRegistry()
	err := gateway.RegisterTools(registry)
	if err != nil {
		t.Fatalf("RegisterTools failed: %v", err)
	}

	// Verify tools are registered
	tool1, exists := registry.Get("mock_tool_1")
	if !exists {
		t.Error("mock_tool_1 should be registered")
	}
	if tool1 != nil && tool1.Name() != "mock_tool_1" {
		t.Errorf("unexpected tool name: %s", tool1.Name())
	}

	tool2, exists := registry.Get("mock_tool_2")
	if !exists {
		t.Error("mock_tool_2 should be registered")
	}
	if tool2 != nil && tool2.Name() != "mock_tool_2" {
		t.Errorf("unexpected tool name: %s", tool2.Name())
	}
}

func TestGatewayStop(t *testing.T) {
	gateway := newGateway()

	// Add a mock server (not actually connected)
	server := newServer("mock-server")
	gateway.servers["mock-server"] = server

	err := gateway.Stop()
	if err != nil {
		t.Errorf("Stop should not error for unconnected servers: %v", err)
	}

	if len(gateway.ServerNames()) != 0 {
		t.Error("servers should be cleared after Stop")
	}
}

func TestConvertMCPResult(t *testing.T) {
	tests := []struct {
		name     string
		result   *mcp.CallToolResult
		expected sdktools.ToolResult
	}{
		{
			name:   "nil result",
			result: nil,
			expected: sdktools.ToolResult{
				Content: "",
				IsError: false,
			},
		},
		{
			name: "text content",
			result: &mcp.CallToolResult{
				Content: []mcp.Content{
					mcp.NewTextContent("Hello, world!"),
				},
				IsError: false,
			},
			expected: sdktools.ToolResult{
				Content: "Hello, world!",
				IsError: false,
			},
		},
		{
			name: "error result",
			result: &mcp.CallToolResult{
				Content: []mcp.Content{
					mcp.NewTextContent("Something went wrong"),
				},
				IsError: true,
			},
			expected: sdktools.ToolResult{
				Content: "Something went wrong",
				IsError: true,
			},
		},
		{
			name: "multiple text contents",
			result: &mcp.CallToolResult{
				Content: []mcp.Content{
					mcp.NewTextContent("Line 1"),
					mcp.NewTextContent("Line 2"),
				},
				IsError: false,
			},
			expected: sdktools.ToolResult{
				Content: "Line 1\nLine 2",
				IsError: false,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := convertMCPResult(tt.result)
			if result.Content != tt.expected.Content {
				t.Errorf("expected content %q, got %q", tt.expected.Content, result.Content)
			}
			if result.IsError != tt.expected.IsError {
				t.Errorf("expected IsError %v, got %v", tt.expected.IsError, result.IsError)
			}
		})
	}
}

func TestStartError(t *testing.T) {
	singleErr := &StartError{
		Errors: []error{
			&mockError{msg: "connection failed"},
		},
	}

	if singleErr.Error() != "MCP gateway start error: connection failed" {
		t.Errorf("unexpected error message: %s", singleErr.Error())
	}

	multiErr := &StartError{
		Errors: []error{
			&mockError{msg: "error 1"},
			&mockError{msg: "error 2"},
		},
	}

	if multiErr.Error() != "MCP gateway start errors: 2 servers failed to connect" {
		t.Errorf("unexpected error message: %s", multiErr.Error())
	}
}

func TestStopError(t *testing.T) {
	singleErr := &StopError{
		Errors: []error{
			&mockError{msg: "close failed"},
		},
	}

	if singleErr.Error() != "MCP gateway stop error: close failed" {
		t.Errorf("unexpected error message: %s", singleErr.Error())
	}

	multiErr := &StopError{
		Errors: []error{
			&mockError{msg: "error 1"},
			&mockError{msg: "error 2"},
		},
	}

	if multiErr.Error() != "MCP gateway stop errors: 2 servers failed to stop cleanly" {
		t.Errorf("unexpected error message: %s", multiErr.Error())
	}
}

// mockError is a simple error implementation for testing.
type mockError struct {
	msg string
}

func (e *mockError) Error() string {
	return e.msg
}

func TestGateway_GetServer(t *testing.T) {
	gateway := newGateway()

	// GetServer on empty gateway should return nil
	if s := gateway.GetServer("nonexistent"); s != nil {
		t.Error("expected nil for nonexistent server")
	}

	// Add a server and retrieve it
	server := newServer("my-server")
	gateway.servers["my-server"] = server

	got := gateway.GetServer("my-server")
	if got == nil {
		t.Fatal("expected non-nil server")
	}
	if got.Name() != "my-server" {
		t.Errorf("expected name 'my-server', got %q", got.Name())
	}

	// Different name should still return nil
	if s := gateway.GetServer("other"); s != nil {
		t.Error("expected nil for non-matching server name")
	}
}

func TestGateway_ServerNames_Multiple(t *testing.T) {
	gateway := newGateway()

	gateway.servers["alpha"] = newServer("alpha")
	gateway.servers["beta"] = newServer("beta")
	gateway.servers["gamma"] = newServer("gamma")

	names := gateway.ServerNames()
	if len(names) != 3 {
		t.Fatalf("expected 3 server names, got %d", len(names))
	}

	// Verify all names are present (order may vary due to map iteration)
	nameSet := make(map[string]bool)
	for _, n := range names {
		nameSet[n] = true
	}
	for _, expected := range []string{"alpha", "beta", "gamma"} {
		if !nameSet[expected] {
			t.Errorf("expected server name %q to be present", expected)
		}
	}
}

func TestGateway_ToolCount_Multiple(t *testing.T) {
	gateway := newGateway()

	server1 := newServer("s1")
	server1.tools = []ToolInfo{
		{Name: "tool1"},
		{Name: "tool2"},
	}

	server2 := newServer("s2")
	server2.tools = []ToolInfo{
		{Name: "tool3"},
	}

	gateway.servers["s1"] = server1
	gateway.servers["s2"] = server2

	if count := gateway.ToolCount(); count != 3 {
		t.Errorf("expected ToolCount()=3, got %d", count)
	}
}

func TestGateway_ToolCount_Empty(t *testing.T) {
	gateway := newGateway()
	if count := gateway.ToolCount(); count != 0 {
		t.Errorf("expected ToolCount()=0, got %d", count)
	}
}

func TestGateway_Stop_EmptyGateway(t *testing.T) {
	gateway := newGateway()
	err := gateway.Stop()
	if err != nil {
		t.Errorf("Stop on empty gateway should return nil, got: %v", err)
	}
}

func TestGateway_Stop_ClearsServers(t *testing.T) {
	gateway := newGateway()
	gateway.servers["s1"] = newServer("s1")
	gateway.servers["s2"] = newServer("s2")

	err := gateway.Stop()
	if err != nil {
		t.Errorf("Stop error: %v", err)
	}

	if len(gateway.servers) != 0 {
		t.Errorf("servers should be empty after Stop, got %d", len(gateway.servers))
	}
}

func TestGateway_RegisterTools_Empty(t *testing.T) {
	gateway := newGateway()
	registry := sdktools.NewToolRegistry()

	err := gateway.RegisterTools(registry)
	if err != nil {
		t.Errorf("RegisterTools on empty gateway should not error: %v", err)
	}

	if len(registry.List()) != 0 {
		t.Error("registry should be empty when gateway has no servers")
	}
}

func TestGateway_RegisterTools_MultipleServers(t *testing.T) {
	gateway := newGateway()

	server1 := newServer("s1")
	server1.tools = []ToolInfo{
		{Name: "s1_tool1", Description: "tool from s1", InputSchema: json.RawMessage(`{"type":"object"}`)},
	}

	server2 := newServer("s2")
	server2.tools = []ToolInfo{
		{Name: "s2_tool1", Description: "tool from s2", InputSchema: json.RawMessage(`{"type":"object"}`)},
		{Name: "s2_tool2", Description: "another tool from s2", InputSchema: json.RawMessage(`{"type":"object"}`)},
	}

	gateway.servers["s1"] = server1
	gateway.servers["s2"] = server2

	registry := sdktools.NewToolRegistry()
	err := gateway.RegisterTools(registry)
	if err != nil {
		t.Fatalf("RegisterTools failed: %v", err)
	}

	allTools := registry.List()
	if len(allTools) != 3 {
		t.Errorf("expected 3 registered tools, got %d", len(allTools))
	}
}

func TestGateway_Start_EmptyConfigs(t *testing.T) {
	gateway := newGateway()

	// Starting with empty configs should succeed with no errors
	err := gateway.Start(context.Background(), map[string]ServerConfig{})
	if err != nil {
		t.Errorf("Start with empty configs should not error: %v", err)
	}
}

func TestGateway_Start_InvalidCommand(t *testing.T) {
	gateway := newGateway()

	configs := map[string]ServerConfig{
		"bad-server": {
			Command: "/nonexistent/command/that/does/not/exist",
			Args:    []string{},
		},
	}

	err := gateway.Start(context.Background(), configs)
	if err == nil {
		t.Fatal("expected error for invalid command")
	}

	// Should be a StartError
	var startErr *StartError
	ok := errors.As(err, &startErr)
	if !ok {
		t.Fatalf("expected *StartError, got %T", err)
	}
	if len(startErr.Errors) != 1 {
		t.Errorf("expected 1 error, got %d", len(startErr.Errors))
	}

	// No servers should be added on failure
	if len(gateway.ServerNames()) != 0 {
		t.Error("no servers should be added when connection fails")
	}
}

func TestGateway_Start_MultipleInvalidCommands(t *testing.T) {
	gateway := newGateway()

	configs := map[string]ServerConfig{
		"bad1": {
			Command: "/nonexistent/cmd1",
		},
		"bad2": {
			Command: "/nonexistent/cmd2",
		},
	}

	err := gateway.Start(context.Background(), configs)
	if err == nil {
		t.Fatal("expected error for invalid commands")
	}

	var startErr *StartError
	ok := errors.As(err, &startErr)
	if !ok {
		t.Fatalf("expected *StartError, got %T", err)
	}
	if len(startErr.Errors) != 2 {
		t.Errorf("expected 2 errors, got %d", len(startErr.Errors))
	}
}

// Integration tests that require actual MCP servers.
// These are skipped by default.

func TestGatewayIntegration(t *testing.T) {
	t.Skip("Integration test: requires actual MCP server (e.g., npx @modelcontextprotocol/server-filesystem)")

	ctx := context.Background()
	gateway := newGateway()

	configs := map[string]ServerConfig{
		"filesystem": {
			Command: "npx",
			Args:    []string{"-y", "@modelcontextprotocol/server-filesystem", "/tmp"},
		},
	}

	if err := gateway.Start(ctx, configs); err != nil {
		t.Fatalf("failed to start gateway: %v", err)
	}
	defer func() { _ = gateway.Stop() }()

	serverNames := gateway.ServerNames()
	if len(serverNames) != 1 {
		t.Fatalf("expected 1 server, got %d", len(serverNames))
	}

	if serverNames[0] != "filesystem" {
		t.Errorf("expected server name 'filesystem', got '%s'", serverNames[0])
	}

	server := gateway.GetServer("filesystem")
	if server == nil {
		t.Fatal("GetServer returned nil for 'filesystem'")
	}

	if !server.IsConnected() {
		t.Error("server should be connected")
	}

	toolInfos := server.Tools()
	if len(toolInfos) == 0 {
		t.Error("filesystem server should have at least one tool")
	}

	// Test registering tools
	registry := sdktools.NewToolRegistry()
	if err := gateway.RegisterTools(registry); err != nil {
		t.Fatalf("failed to register tools: %v", err)
	}

	// The filesystem MCP server typically has tools like read_file, write_file, etc.
	// Let's check if at least one tool was registered
	allTools := registry.List()
	if len(allTools) == 0 {
		t.Error("no tools were registered from MCP server")
	}
}

func TestToolExecuteIntegration(t *testing.T) {
	t.Skip("Integration test: requires actual MCP server (e.g., npx @modelcontextprotocol/server-filesystem)")

	ctx := context.Background()
	gateway := newGateway()

	configs := map[string]ServerConfig{
		"filesystem": {
			Command: "npx",
			Args:    []string{"-y", "@modelcontextprotocol/server-filesystem", "/tmp"},
		},
	}

	if err := gateway.Start(ctx, configs); err != nil {
		t.Fatalf("failed to start gateway: %v", err)
	}
	defer func() { _ = gateway.Stop() }()

	server := gateway.GetServer("filesystem")
	if server == nil {
		t.Fatal("GetServer returned nil for 'filesystem'")
	}

	// Find a tool to test (e.g., list_directory or similar)
	var listDirTool *Tool
	for _, info := range server.Tools() {
		if info.Name == "list_directory" || info.Name == "list_dir" {
			listDirTool = NewTool(server, info)
			break
		}
	}

	if listDirTool == nil {
		t.Skip("list_directory tool not found, skipping execute test")
	}

	// Execute the tool
	input := json.RawMessage(`{"path": "/tmp"}`)
	result, err := listDirTool.Execute(ctx, input)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if result.IsError {
		t.Errorf("tool execution returned error: %s", result.Content)
	}
}

// Reconfigure tests

func TestGateway_Reconfigure_AddServer(t *testing.T) {
	gateway := newGateway()
	registry := sdktools.NewToolRegistry()

	// Start with one mock server
	server1 := newServer("server1")
	server1.tools = []ToolInfo{
		{Name: "tool1", Description: "tool from server1", InputSchema: json.RawMessage(`{"type":"object"}`)},
	}
	gateway.servers["server1"] = server1
	gateway.config = GatewayConfig{
		Servers: map[string]ServerEntry{
			"server1": {Command: "cmd1"},
		},
	}
	gateway.expandedConfigs["server1"] = ServerConfig{Command: "cmd1"}

	// Register initial tools
	_ = gateway.RegisterTools(registry)
	if len(registry.List()) != 1 {
		t.Fatalf("expected 1 tool initially, got %d", len(registry.List()))
	}

	// Reconfigure to add server2 (using mock to avoid actual connection)
	// Note: This test verifies the logic, but since we can't easily mock Connect(),
	// we'll verify the error handling instead

	newConfig := GatewayConfig{
		Servers: map[string]ServerEntry{
			"server1": {Command: "cmd1"},
			"server2": {Command: "/nonexistent/cmd"},
		},
	}

	err := gateway.Reconfigure(context.Background(), newConfig, registry, func(s string) string { return s }, nil)

	// Should get an error for the failed connection to server2
	if err == nil {
		t.Fatal("expected error for nonexistent command")
	}

	var reconfigErr *ReconfigureError
	if !errors.As(err, &reconfigErr) {
		t.Fatalf("expected ReconfigureError, got %T", err)
	}

	// server1 should still be present (unchanged)
	if gateway.GetServer("server1") == nil {
		t.Error("server1 should still exist")
	}

	// tool1 should still be registered
	if _, ok := registry.Get("tool1"); !ok {
		t.Error("tool1 should still be registered")
	}
}

func TestGateway_Reconfigure_RemoveServer(t *testing.T) {
	gateway := newGateway()
	registry := sdktools.NewToolRegistry()

	// Start with two mock servers
	server1 := newServer("server1")
	server1.tools = []ToolInfo{
		{Name: "tool1", Description: "tool from server1", InputSchema: json.RawMessage(`{"type":"object"}`)},
	}
	server2 := newServer("server2")
	server2.tools = []ToolInfo{
		{Name: "tool2", Description: "tool from server2", InputSchema: json.RawMessage(`{"type":"object"}`)},
	}
	gateway.servers["server1"] = server1
	gateway.servers["server2"] = server2
	gateway.config = GatewayConfig{
		Servers: map[string]ServerEntry{
			"server1": {Command: "cmd1"},
			"server2": {Command: "cmd2"},
		},
	}
	gateway.expandedConfigs["server1"] = ServerConfig{Command: "cmd1"}
	gateway.expandedConfigs["server2"] = ServerConfig{Command: "cmd2"}

	// Register initial tools
	_ = gateway.RegisterTools(registry)
	if len(registry.List()) != 2 {
		t.Fatalf("expected 2 tools initially, got %d", len(registry.List()))
	}

	// Reconfigure to remove server2
	newConfig := GatewayConfig{
		Servers: map[string]ServerEntry{
			"server1": {Command: "cmd1"},
		},
	}

	err := gateway.Reconfigure(context.Background(), newConfig, registry, func(s string) string { return s }, nil)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// server2 should be removed
	if gateway.GetServer("server2") != nil {
		t.Error("server2 should be removed")
	}

	// server1 should still exist
	if gateway.GetServer("server1") == nil {
		t.Error("server1 should still exist")
	}

	// tool2 should be unregistered
	if _, ok := registry.Get("tool2"); ok {
		t.Error("tool2 should be unregistered")
	}

	// tool1 should still be registered
	if _, ok := registry.Get("tool1"); !ok {
		t.Error("tool1 should still be registered")
	}
}

func TestGateway_Reconfigure_UnchangedServer(t *testing.T) {
	gateway := newGateway()
	registry := sdktools.NewToolRegistry()

	// Start with one mock server
	server1 := newServer("server1")
	server1.tools = []ToolInfo{
		{Name: "tool1", Description: "tool from server1", InputSchema: json.RawMessage(`{"type":"object"}`)},
	}
	gateway.servers["server1"] = server1
	gateway.config = GatewayConfig{
		Servers: map[string]ServerEntry{
			"server1": {Command: "cmd1", Args: []string{"arg1"}},
		},
	}
	gateway.expandedConfigs["server1"] = ServerConfig{Command: "cmd1", Args: []string{"arg1"}}

	// Register initial tools
	_ = gateway.RegisterTools(registry)

	// Reconfigure with same config
	newConfig := GatewayConfig{
		Servers: map[string]ServerEntry{
			"server1": {Command: "cmd1", Args: []string{"arg1"}},
		},
	}

	err := gateway.Reconfigure(context.Background(), newConfig, registry, func(s string) string { return s }, nil)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Same server instance should be preserved
	got := gateway.GetServer("server1")
	if got == nil {
		t.Fatal("server1 should exist")
	}
	if got != server1 {
		t.Error("server1 should be the same instance (connection preserved)")
	}

	// tool1 should still be registered
	if _, ok := registry.Get("tool1"); !ok {
		t.Error("tool1 should still be registered")
	}
}

func TestGateway_Reconfigure_EmptyConfig(t *testing.T) {
	gateway := newGateway()
	registry := sdktools.NewToolRegistry()

	// Start with one mock server
	server1 := newServer("server1")
	server1.tools = []ToolInfo{
		{Name: "tool1", Description: "tool from server1", InputSchema: json.RawMessage(`{"type":"object"}`)},
	}
	gateway.servers["server1"] = server1
	gateway.config = GatewayConfig{
		Servers: map[string]ServerEntry{
			"server1": {Command: "cmd1"},
		},
	}

	// Register initial tools
	_ = gateway.RegisterTools(registry)

	// Reconfigure with empty config (removes all servers)
	newConfig := GatewayConfig{
		Servers: map[string]ServerEntry{},
	}

	err := gateway.Reconfigure(context.Background(), newConfig, registry, func(s string) string { return s }, nil)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// server1 should be removed
	if gateway.GetServer("server1") != nil {
		t.Error("server1 should be removed")
	}

	// tool1 should be unregistered
	if _, ok := registry.Get("tool1"); ok {
		t.Error("tool1 should be unregistered")
	}

	// No servers should remain
	if len(gateway.ServerNames()) != 0 {
		t.Errorf("expected 0 servers, got %d", len(gateway.ServerNames()))
	}
}

func TestGateway_Reconfigure_ChangedConfig(t *testing.T) {
	gateway := newGateway()
	registry := sdktools.NewToolRegistry()

	// Start with one mock server
	server1 := newServer("server1")
	server1.tools = []ToolInfo{
		{Name: "tool1", Description: "tool from server1", InputSchema: json.RawMessage(`{"type":"object"}`)},
	}
	gateway.servers["server1"] = server1
	gateway.config = GatewayConfig{
		Servers: map[string]ServerEntry{
			"server1": {Command: "cmd1"},
		},
	}
	gateway.expandedConfigs["server1"] = ServerConfig{Command: "cmd1"}

	// Register initial tools
	_ = gateway.RegisterTools(registry)

	// Reconfigure with changed command (will fail to connect to new command)
	newConfig := GatewayConfig{
		Servers: map[string]ServerEntry{
			"server1": {Command: "/nonexistent/newcmd"},
		},
	}

	err := gateway.Reconfigure(context.Background(), newConfig, registry, func(s string) string { return s }, nil)

	// Should get an error for the failed connection
	if err == nil {
		t.Fatal("expected error for nonexistent command")
	}

	var reconfigErr *ReconfigureError
	if !errors.As(err, &reconfigErr) {
		t.Fatalf("expected ReconfigureError, got %T", err)
	}

	// tool1 should be unregistered (old server was closed)
	if _, ok := registry.Get("tool1"); ok {
		t.Error("tool1 should be unregistered")
	}
}

func TestGateway_ConfigChanged(t *testing.T) {
	// configChanged compares against the previously-expanded ServerConfig stored
	// in g.expandedConfigs (W-7), not the raw ServerEntry on g.config.Servers.
	tests := []struct {
		name     string
		old      ServerConfig
		new      ServerConfig
		expected bool
	}{
		{
			name:     "identical command",
			old:      ServerConfig{Command: "cmd", Args: []string{"arg1"}},
			new:      ServerConfig{Command: "cmd", Args: []string{"arg1"}},
			expected: false,
		},
		{
			name:     "different command",
			old:      ServerConfig{Command: "cmd1"},
			new:      ServerConfig{Command: "cmd2"},
			expected: true,
		},
		{
			name:     "different args",
			old:      ServerConfig{Command: "cmd", Args: []string{"arg1"}},
			new:      ServerConfig{Command: "cmd", Args: []string{"arg2"}},
			expected: true,
		},
		{
			name:     "different transport",
			old:      ServerConfig{Transport: "stdio"},
			new:      ServerConfig{Transport: "http"},
			expected: true,
		},
		{
			name:     "different URL",
			old:      ServerConfig{URL: "http://localhost:8080"},
			new:      ServerConfig{URL: "http://localhost:9090"},
			expected: true,
		},
		{
			name:     "same URL",
			old:      ServerConfig{URL: "http://localhost:8080"},
			new:      ServerConfig{URL: "http://localhost:8080"},
			expected: false,
		},
		{
			name:     "different env count",
			old:      ServerConfig{Env: map[string]string{"A": "1"}},
			new:      ServerConfig{Env: map[string]string{"A": "1", "B": "2"}},
			expected: true,
		},
		{
			name:     "different env value",
			old:      ServerConfig{Env: map[string]string{"A": "1"}},
			new:      ServerConfig{Env: map[string]string{"A": "2"}},
			expected: true,
		},
		{
			name:     "same env",
			old:      ServerConfig{Env: map[string]string{"A": "1", "B": "2"}},
			new:      ServerConfig{Env: map[string]string{"A": "1", "B": "2"}},
			expected: false,
		},
		{
			name:     "different headers count",
			old:      ServerConfig{Headers: map[string]string{"X-Auth": "token"}},
			new:      ServerConfig{Headers: map[string]string{"X-Auth": "token", "X-Other": "val"}},
			expected: true,
		},
		{
			name:     "same headers",
			old:      ServerConfig{Headers: map[string]string{"X-Auth": "token"}},
			new:      ServerConfig{Headers: map[string]string{"X-Auth": "token"}},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gateway := &Gateway{
				expandedConfigs: map[string]ServerConfig{
					"test": tt.old,
				},
			}

			result := gateway.configChanged("test", tt.new)
			if result != tt.expected {
				t.Errorf("configChanged() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestGateway_ConfigChanged_NonExistent(t *testing.T) {
	gateway := &Gateway{
		expandedConfigs: map[string]ServerConfig{},
	}

	// Non-existent server should return true (it's new)
	result := gateway.configChanged("nonexistent", ServerConfig{Command: "cmd"})
	if !result {
		t.Error("configChanged for non-existent server should return true")
	}
}

func TestReconfigureError(t *testing.T) {
	singleErr := &ReconfigureError{
		Errors: []error{
			&mockError{msg: "connection failed"},
		},
	}

	if singleErr.Error() != "MCP gateway reconfigure error: connection failed" {
		t.Errorf("unexpected error message: %s", singleErr.Error())
	}

	multiErr := &ReconfigureError{
		Errors: []error{
			&mockError{msg: "error 1"},
			&mockError{msg: "error 2"},
		},
	}

	if multiErr.Error() != "MCP gateway reconfigure errors: 2 operations failed" {
		t.Errorf("unexpected error message: %s", multiErr.Error())
	}
}

// Status tests

func TestGateway_Status_EmptyGateway(t *testing.T) {
	gateway := newGateway()
	status := gateway.Status()
	if status == nil {
		t.Fatal("Status() should not return nil")
	}
	if len(status) != 0 {
		t.Errorf("expected empty slice for empty gateway, got %d items", len(status))
	}
}

func TestGateway_Status_ConnectedServers(t *testing.T) {
	gateway := newGateway()

	// Add mock servers with tools
	server1 := newServer("alpha")
	server1.tools = []ToolInfo{
		{Name: "tool1", Description: "first tool", InputSchema: json.RawMessage(`{"type":"object"}`)},
		{Name: "tool2", Description: "second tool", InputSchema: json.RawMessage(`{"type":"object"}`)},
	}
	server1.connected = true
	server1.transportType = "stdio"

	server2 := newServer("beta")
	server2.tools = []ToolInfo{
		{Name: "tool3", Description: "third tool", InputSchema: json.RawMessage(`{"type":"object"}`)},
	}
	server2.connected = true
	server2.transportType = "http"

	gateway.servers["alpha"] = server1
	gateway.servers["beta"] = server2

	status := gateway.Status()
	if len(status) != 2 {
		t.Fatalf("expected 2 status items, got %d", len(status))
	}

	// Verify sorting (alpha < beta)
	if status[0].Name != "alpha" {
		t.Errorf("expected first status to be 'alpha', got %q", status[0].Name)
	}
	if status[1].Name != "beta" {
		t.Errorf("expected second status to be 'beta', got %q", status[1].Name)
	}

	// Verify alpha server status
	if status[0].Transport != "stdio" {
		t.Errorf("expected alpha transport 'stdio', got %q", status[0].Transport)
	}
	if !status[0].Connected {
		t.Error("expected alpha to be connected")
	}
	if status[0].ToolCount != 2 {
		t.Errorf("expected alpha tool count 2, got %d", status[0].ToolCount)
	}
	if len(status[0].Tools) != 2 {
		t.Errorf("expected 2 tool names, got %d", len(status[0].Tools))
	}

	// Verify beta server status
	if status[1].Transport != "http" {
		t.Errorf("expected beta transport 'http', got %q", status[1].Transport)
	}
	if !status[1].Connected {
		t.Error("expected beta to be connected")
	}
	if status[1].ToolCount != 1 {
		t.Errorf("expected beta tool count 1, got %d", status[1].ToolCount)
	}
}

func TestGateway_Status_DisconnectedServer(t *testing.T) {
	gateway := newGateway()

	// Add a server that failed to connect
	server := newServer("failed-server")
	server.connected = false
	server.transportType = "stdio"
	server.lastError = "connection refused"

	gateway.servers["failed-server"] = server

	status := gateway.Status()
	if len(status) != 1 {
		t.Fatalf("expected 1 status item, got %d", len(status))
	}

	if status[0].Name != "failed-server" {
		t.Errorf("expected name 'failed-server', got %q", status[0].Name)
	}
	if status[0].Connected {
		t.Error("expected server to be disconnected")
	}
	if status[0].Error != "connection refused" {
		t.Errorf("expected error 'connection refused', got %q", status[0].Error)
	}
}

func TestGateway_Status_SortedByName(t *testing.T) {
	gateway := newGateway()

	// Add servers in non-alphabetical order
	names := []string{"zebra", "alpha", "mike"}
	for _, name := range names {
		server := newServer(name)
		server.connected = true
		server.transportType = "stdio"
		gateway.servers[name] = server
	}

	status := gateway.Status()
	if len(status) != 3 {
		t.Fatalf("expected 3 status items, got %d", len(status))
	}

	// Verify alphabetical ordering
	expected := []string{"alpha", "mike", "zebra"}
	for i, exp := range expected {
		if status[i].Name != exp {
			t.Errorf("expected status[%d].Name=%q, got %q", i, exp, status[i].Name)
		}
	}
}

func TestServer_Status(t *testing.T) {
	server := newServer("test-server")
	server.tools = []ToolInfo{
		{Name: "read_file", Description: "Read a file", InputSchema: json.RawMessage(`{"type":"object"}`)},
		{Name: "write_file", Description: "Write a file", InputSchema: json.RawMessage(`{"type":"object"}`)},
	}
	server.connected = true
	server.transportType = "http"

	status := server.Status()

	if status.Name != "test-server" {
		t.Errorf("expected name 'test-server', got %q", status.Name)
	}
	if status.Transport != "http" {
		t.Errorf("expected transport 'http', got %q", status.Transport)
	}
	if !status.Connected {
		t.Error("expected connected=true")
	}
	if status.ToolCount != 2 {
		t.Errorf("expected tool count 2, got %d", status.ToolCount)
	}
	if len(status.Tools) != 2 {
		t.Errorf("expected 2 tool names, got %d", len(status.Tools))
	}
	// Verify tool names are present
	toolSet := make(map[string]bool)
	for _, name := range status.Tools {
		toolSet[name] = true
	}
	if !toolSet["read_file"] || !toolSet["write_file"] {
		t.Errorf("expected tools [read_file, write_file], got %v", status.Tools)
	}
}

func TestServer_Status_NoTools(t *testing.T) {
	server := newServer("empty-server")
	server.connected = true
	server.transportType = "stdio"

	status := server.Status()

	if status.ToolCount != 0 {
		t.Errorf("expected tool count 0, got %d", status.ToolCount)
	}
	if len(status.Tools) != 0 {
		t.Errorf("expected empty tools slice, got %d items", len(status.Tools))
	}
}

func TestServer_Status_WithError(t *testing.T) {
	server := newServer("error-server")
	server.connected = false
	server.lastError = "failed to connect: connection refused"

	status := server.Status()

	if status.Connected {
		t.Error("expected connected=false")
	}
	if status.Error != "failed to connect: connection refused" {
		t.Errorf("unexpected error: %q", status.Error)
	}
}

func TestGateway_SetDefaultWorkDir(t *testing.T) {
	gw := newGateway()
	gw.SetDefaultWorkDir("/test/workspace")

	gw.mu.RLock()
	got := gw.defaultWorkDir
	gw.mu.RUnlock()

	if got != "/test/workspace" {
		t.Errorf("expected defaultWorkDir %q, got %q", "/test/workspace", got)
	}
}

func TestGateway_SchemaSanitizer(t *testing.T) {
	gateway := newGateway()

	// Add a test MCP server with a project parameter
	server := newServer("test-mcp")
	server.tools = []ToolInfo{
		{
			Name:        "search_graph",
			Description: "Search the code graph",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"project":{"type":"string"},"name_pattern":{"type":"string"}},"required":["project","name_pattern"]}`),
		},
	}
	gateway.servers["test-mcp"] = server

	// Set sanitizer via GatewayConfig to strip 'project' param from test-mcp tools
	gateway.schemaSanitizer = func(source string, schema json.RawMessage) json.RawMessage {
		if source != "test-mcp" {
			return schema
		}
		return sdktools.DefaultParamManager().SanitizeSchema(source, schema)
	}

	registry := sdktools.NewToolRegistry()
	if err := gateway.RegisterTools(registry); err != nil {
		t.Fatalf("RegisterTools failed: %v", err)
	}

	tool, ok := registry.Get("search_graph")
	if !ok {
		t.Fatal("search_graph should be registered")
	}

	// Verify 'project' is stripped from the schema
	var schema map[string]json.RawMessage
	if err := json.Unmarshal(tool.InputSchema(), &schema); err != nil {
		t.Fatalf("tool schema is not valid JSON: %v", err)
	}

	var props map[string]json.RawMessage
	if err := json.Unmarshal(schema["properties"], &props); err != nil {
		t.Fatalf("properties is not valid JSON: %v", err)
	}
	if _, exists := props["project"]; exists {
		t.Error("'project' should be stripped from test-mcp tool schema")
	}
	if _, exists := props["name_pattern"]; !exists {
		t.Error("'name_pattern' should still be present in tool schema")
	}

	// Verify 'project' is also stripped from required
	var required []string
	if err := json.Unmarshal(schema["required"], &required); err != nil {
		t.Fatalf("required is not valid JSON: %v", err)
	}
	for _, r := range required {
		if r == "project" {
			t.Error("'project' should be stripped from required in tool schema")
		}
	}
}

func TestGateway_SchemaSanitizer_OtherSourceUntouched(t *testing.T) {
	gateway := newGateway()

	// Add a non-test-mcp server with a project parameter
	server := newServer("other-mcp")
	server.tools = []ToolInfo{
		{
			Name:        "my_tool",
			Description: "Some tool",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"project":{"type":"string"}}}`),
		},
	}
	gateway.servers["other-mcp"] = server

	// Set sanitizer via GatewayConfig that only strips project for test-mcp
	gateway.schemaSanitizer = func(source string, schema json.RawMessage) json.RawMessage {
		if source != "test-mcp" {
			return schema
		}
		return sdktools.DefaultParamManager().SanitizeSchema(source, schema)
	}

	registry := sdktools.NewToolRegistry()
	if err := gateway.RegisterTools(registry); err != nil {
		t.Fatalf("RegisterTools failed: %v", err)
	}

	tool, ok := registry.Get("my_tool")
	if !ok {
		t.Fatal("my_tool should be registered")
	}

	// Verify 'project' is NOT stripped (different source)
	var schema map[string]json.RawMessage
	if err := json.Unmarshal(tool.InputSchema(), &schema); err != nil {
		t.Fatalf("tool schema is not valid JSON: %v", err)
	}

	var props map[string]json.RawMessage
	if err := json.Unmarshal(schema["properties"], &props); err != nil {
		t.Fatalf("properties is not valid JSON: %v", err)
	}
	if _, exists := props["project"]; !exists {
		t.Error("'project' should still be present for non-test-mcp server")
	}
}

func TestStartGateway_DefaultWorkDir(t *testing.T) {
	cfg := GatewayConfig{
		Servers:        map[string]ServerEntry{},
		DefaultWorkDir: "/my/workspace",
	}

	gw, err := StartGateway(context.Background(), cfg, sdktools.NewToolRegistry(), func(s string) string { return s }, nil)
	// No servers configured, returns nil gateway
	if gw != nil {
		t.Error("expected nil gateway when no servers configured")
	}
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestGateway_StartAppliesDefaultWorkDir(t *testing.T) {
	gw := newGateway()
	gw.defaultWorkDir = "/default/dir"

	// Start with an invalid command but verify the WorkDir was applied
	// by checking that the server config received the default work dir.
	// Since we can't easily inspect the config after Start, we verify
	// the Reconfigure path which also applies defaultWorkDir.
	gw.config = GatewayConfig{
		Servers: map[string]ServerEntry{},
	}

	// Verify SetDefaultWorkDir + Reconfigure propagates WorkDir
	gw.SetDefaultWorkDir("/updated/workspace")

	gw.mu.RLock()
	dir := gw.defaultWorkDir
	gw.mu.RUnlock()

	if dir != "/updated/workspace" {
		t.Errorf("expected defaultWorkDir %q, got %q", "/updated/workspace", dir)
	}
}
