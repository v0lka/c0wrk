package mcp

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/user/agent/internal/config"
	"github.com/user/agent/internal/tools"
)

func TestNewMCPGateway(t *testing.T) {
	gateway := NewMCPGateway()
	if gateway == nil {
		t.Fatal("NewMCPGateway returned nil")
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

func TestNewMCPServer(t *testing.T) {
	server := NewMCPServer("test-server")
	if server == nil {
		t.Fatal("NewMCPServer returned nil")
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

func TestNewMCPTool(t *testing.T) {
	server := NewMCPServer("test-server")

	info := MCPToolInfo{
		Name:        "test_tool",
		Description: "A test tool for testing",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"name":{"type":"string"}}}`),
	}

	tool := NewMCPTool(server, info)
	if tool == nil {
		t.Fatal("NewMCPTool returned nil")
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

	var schemaMap map[string]interface{}
	if err := json.Unmarshal(schema, &schemaMap); err != nil {
		t.Errorf("failed to unmarshal schema: %v", err)
	}

	if schemaMap["type"] != "object" {
		t.Errorf("expected schema type 'object', got '%v'", schemaMap["type"])
	}
}

func TestMCPToolImplementsToolInterface(t *testing.T) {
	server := NewMCPServer("test-server")
	info := MCPToolInfo{
		Name:        "test_tool",
		Description: "A test tool",
		InputSchema: json.RawMessage(`{"type":"object"}`),
	}

	mcpTool := NewMCPTool(server, info)

	// Verify MCPTool implements tools.Tool interface
	var _ tools.Tool = mcpTool
}

func TestMCPGatewayRegisterTools(t *testing.T) {
	gateway := NewMCPGateway()

	// Manually add a mock server with tools for testing
	server := NewMCPServer("mock-server")
	server.tools = []MCPToolInfo{
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
	registry := tools.NewToolRegistry()
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

func TestMCPGatewayStop(t *testing.T) {
	gateway := NewMCPGateway()

	// Add a mock server (not actually connected)
	server := NewMCPServer("mock-server")
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
		expected tools.ToolResult
	}{
		{
			name:   "nil result",
			result: nil,
			expected: tools.ToolResult{
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
			expected: tools.ToolResult{
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
			expected: tools.ToolResult{
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
			expected: tools.ToolResult{
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

func TestMCPStartError(t *testing.T) {
	singleErr := &MCPStartError{
		Errors: []error{
			&mockError{msg: "connection failed"},
		},
	}

	if singleErr.Error() != "MCP gateway start error: connection failed" {
		t.Errorf("unexpected error message: %s", singleErr.Error())
	}

	multiErr := &MCPStartError{
		Errors: []error{
			&mockError{msg: "error 1"},
			&mockError{msg: "error 2"},
		},
	}

	if multiErr.Error() != "MCP gateway start errors: 2 servers failed to connect" {
		t.Errorf("unexpected error message: %s", multiErr.Error())
	}
}

func TestMCPStopError(t *testing.T) {
	singleErr := &MCPStopError{
		Errors: []error{
			&mockError{msg: "close failed"},
		},
	}

	if singleErr.Error() != "MCP gateway stop error: close failed" {
		t.Errorf("unexpected error message: %s", singleErr.Error())
	}

	multiErr := &MCPStopError{
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

// Integration tests that require actual MCP servers.
// These are skipped by default.

func TestMCPGatewayIntegration(t *testing.T) {
	t.Skip("Integration test: requires actual MCP server (e.g., npx @modelcontextprotocol/server-filesystem)")

	ctx := context.Background()
	gateway := NewMCPGateway()

	configs := map[string]config.MCPServerConfig{
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
	registry := tools.NewToolRegistry()
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

func TestMCPToolExecuteIntegration(t *testing.T) {
	t.Skip("Integration test: requires actual MCP server (e.g., npx @modelcontextprotocol/server-filesystem)")

	ctx := context.Background()
	gateway := NewMCPGateway()

	configs := map[string]config.MCPServerConfig{
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
	var listDirTool *MCPTool
	for _, info := range server.Tools() {
		if info.Name == "list_directory" || info.Name == "list_dir" {
			listDirTool = NewMCPTool(server, info)
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
