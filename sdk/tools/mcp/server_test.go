package mcp

import (
	"context"
	"encoding/json"
	"testing"
)

func TestNewServer_Initialization(t *testing.T) {
	tests := []struct {
		name       string
		serverName string
	}{
		{"simple name", "test-server"},
		{"empty name", ""},
		{"name with special chars", "server-with-dashes_and_underscores"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newServer(tt.serverName)
			if s == nil {
				t.Fatal("NewServer returned nil")
			}
			if s.Name() != tt.serverName {
				t.Errorf("Name() = %q, want %q", s.Name(), tt.serverName)
			}
			if s.IsConnected() {
				t.Error("new server should not be connected")
			}
			if len(s.Tools()) != 0 {
				t.Error("new server should have no tools")
			}
		})
	}
}

func TestServer_CallTool_NilClient(t *testing.T) {
	s := newServer("test")

	_, err := s.CallTool(context.Background(), "some_tool", nil)
	if err == nil {
		t.Fatal("expected error when calling tool on disconnected server")
	}

	expected := "mcp server test is not connected"
	if err.Error() != expected {
		t.Errorf("error = %q, want %q", err.Error(), expected)
	}
}

func TestServer_CallTool_NilClientWithArgs(t *testing.T) {
	s := newServer("my-server")

	args := map[string]any{
		"path": "/tmp",
	}

	_, err := s.CallTool(context.Background(), "read_file", args)
	if err == nil {
		t.Fatal("expected error when calling tool on disconnected server")
	}

	if err.Error() != "mcp server my-server is not connected" {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestServer_DiscoverTools_NilClient(t *testing.T) {
	s := newServer("test")

	err := s.DiscoverTools(context.Background())
	if err == nil {
		t.Fatal("expected error when discovering tools on disconnected server")
	}

	expected := "mcp server test is not connected"
	if err.Error() != expected {
		t.Errorf("error = %q, want %q", err.Error(), expected)
	}
}

func TestServer_Close_NilClient(t *testing.T) {
	s := newServer("test")

	// Closing a server with no client should return nil
	err := s.Close()
	if err != nil {
		t.Errorf("Close() on nil client should return nil, got: %v", err)
	}
}

func TestServer_Close_NilClient_ClearsState(t *testing.T) {
	s := newServer("test")
	// Manually set some tools
	s.tools = []ToolInfo{
		{Name: "tool1", Description: "desc", InputSchema: json.RawMessage(`{}`)},
	}

	err := s.Close()
	if err != nil {
		t.Errorf("Close() error = %v", err)
	}

	// When client is nil, Close returns early — tools are NOT cleared
	if len(s.tools) != 1 {
		t.Errorf("tools should not be cleared when client is nil, got len=%d", len(s.tools))
	}
	if s.IsConnected() {
		t.Error("should not be connected after Close")
	}
}

func TestServer_Close_MultipleTimes(t *testing.T) {
	s := newServer("test")

	// Multiple closes should be safe
	for i := 0; i < 3; i++ {
		err := s.Close()
		if err != nil {
			t.Errorf("Close() call %d error = %v", i, err)
		}
	}
}

func TestServer_Tools_ReturnsCopy(t *testing.T) {
	s := newServer("test")
	s.tools = []ToolInfo{
		{Name: "tool1", Description: "desc1", InputSchema: json.RawMessage(`{}`)},
		{Name: "tool2", Description: "desc2", InputSchema: json.RawMessage(`{}`)},
	}

	tools1 := s.Tools()
	tools2 := s.Tools()

	// Verify it returns correct count
	if len(tools1) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(tools1))
	}

	// Modify the returned slice and verify original is unaffected
	tools1[0].Name = "modified"
	if tools2[0].Name == "modified" {
		t.Error("Tools() should return a copy, not a reference")
	}

	// Original should be unaffected
	original := s.Tools()
	if original[0].Name != "tool1" {
		t.Error("original tools should not be modified")
	}
}

func TestServer_IsConnected(t *testing.T) {
	s := newServer("test")

	if s.IsConnected() {
		t.Error("new server should not be connected")
	}

	// We can't easily set a real client, but we verified the nil path
}

func TestServer_Name(t *testing.T) {
	tests := []struct {
		name     string
		expected string
	}{
		{"simple", "simple"},
		{"empty", ""},
		{"with-special-chars", "with-special-chars"},
		{"unicode-日本語", "unicode-日本語"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newServer(tt.expected)
			if got := s.Name(); got != tt.expected {
				t.Errorf("Name() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestServer_Connect_UnsupportedTransport(t *testing.T) {
	s := newServer("test")

	cfg := ServerConfig{
		Transport: "unsupported",
	}

	err := s.Connect(context.Background(), cfg)
	if err == nil {
		t.Fatal("expected error for unsupported transport")
	}

	expected := `unsupported transport type "unsupported" for MCP server test`
	if err.Error() != expected {
		t.Errorf("error = %q, want %q", err.Error(), expected)
	}
}

func TestServer_Connect_HTTP_MissingURL(t *testing.T) {
	s := newServer("test")

	cfg := ServerConfig{
		Transport: "http",
		URL:       "",
	}

	err := s.Connect(context.Background(), cfg)
	if err == nil {
		t.Fatal("expected error for HTTP transport without URL")
	}

	expected := "http transport requires URL for MCP server test"
	if err.Error() != expected {
		t.Errorf("error = %q, want %q", err.Error(), expected)
	}
}

func TestServer_Connect_HTTP_InvalidURL(t *testing.T) {
	s := newServer("test")

	cfg := ServerConfig{
		Transport: "http",
		URL:       "http://localhost:99999/invalid", // Invalid port
	}

	err := s.Connect(context.Background(), cfg)
	if err == nil {
		t.Fatal("expected error for invalid HTTP URL")
	}

	// Should get an error related to HTTP connection failure
	// The error could be from Streamable HTTP or SSE fallback
	if !containsSubstring(err.Error(), "failed to start MCP client") && !containsSubstring(err.Error(), "failed to create HTTP MCP client") {
		t.Errorf("error should indicate HTTP connection failure, got: %v", err)
	}
}

func TestServerConfig_DefaultsToStdio(t *testing.T) {
	// Verify that empty transport defaults to stdio behavior
	cfg := ServerConfig{
		Transport: "",
		Command:   "/nonexistent/command",
	}

	s := newServer("test")
	err := s.Connect(context.Background(), cfg)

	// Should fail because command doesn't exist, not because of transport type
	if err == nil {
		t.Fatal("expected error for nonexistent command")
	}

	// Error should mention stdio client creation
	if !containsSubstring(err.Error(), "stdio") {
		t.Errorf("error should mention stdio, got: %v", err)
	}
}

func TestServerConfig_TransportStdioExplicit(t *testing.T) {
	// Verify explicit "stdio" transport works
	cfg := ServerConfig{
		Transport: "stdio",
		Command:   "/nonexistent/command",
	}

	s := newServer("test")
	err := s.Connect(context.Background(), cfg)

	// Should fail because command doesn't exist
	if err == nil {
		t.Fatal("expected error for nonexistent command")
	}

	// Error should mention stdio client creation
	if !containsSubstring(err.Error(), "stdio") {
		t.Errorf("error should mention stdio, got: %v", err)
	}
}

// containsSubstring checks if s contains substr (case-insensitive).
func containsSubstring(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || substr == "" ||
		(s != "" && substr != "" && containsFold(s, substr)))
}

func containsFold(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		match := true
		for j := 0; j < len(substr); j++ {
			if toLower(s[i+j]) != toLower(substr[j]) {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

func toLower(c byte) byte {
	if c >= 'A' && c <= 'Z' {
		return c + ('a' - 'A')
	}
	return c
}
