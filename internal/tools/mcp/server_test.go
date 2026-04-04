package mcp

import (
	"context"
	"encoding/json"
	"testing"
)

func TestNewMCPServer_Initialization(t *testing.T) {
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
			s := NewMCPServer(tt.serverName)
			if s == nil {
				t.Fatal("NewMCPServer returned nil")
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

func TestMCPServer_CallTool_NilClient(t *testing.T) {
	s := NewMCPServer("test")

	_, err := s.CallTool(context.Background(), "some_tool", nil)
	if err == nil {
		t.Fatal("expected error when calling tool on disconnected server")
	}

	expected := "MCP server test is not connected"
	if err.Error() != expected {
		t.Errorf("error = %q, want %q", err.Error(), expected)
	}
}

func TestMCPServer_CallTool_NilClientWithArgs(t *testing.T) {
	s := NewMCPServer("my-server")

	args := map[string]interface{}{
		"path": "/tmp",
	}

	_, err := s.CallTool(context.Background(), "read_file", args)
	if err == nil {
		t.Fatal("expected error when calling tool on disconnected server")
	}

	if err.Error() != "MCP server my-server is not connected" {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestMCPServer_DiscoverTools_NilClient(t *testing.T) {
	s := NewMCPServer("test")

	err := s.DiscoverTools(context.Background())
	if err == nil {
		t.Fatal("expected error when discovering tools on disconnected server")
	}

	expected := "MCP server test is not connected"
	if err.Error() != expected {
		t.Errorf("error = %q, want %q", err.Error(), expected)
	}
}

func TestMCPServer_Close_NilClient(t *testing.T) {
	s := NewMCPServer("test")

	// Closing a server with no client should return nil
	err := s.Close()
	if err != nil {
		t.Errorf("Close() on nil client should return nil, got: %v", err)
	}
}

func TestMCPServer_Close_NilClient_ClearsState(t *testing.T) {
	s := NewMCPServer("test")
	// Manually set some tools
	s.tools = []MCPToolInfo{
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

func TestMCPServer_Close_MultipleTimes(t *testing.T) {
	s := NewMCPServer("test")

	// Multiple closes should be safe
	for i := 0; i < 3; i++ {
		err := s.Close()
		if err != nil {
			t.Errorf("Close() call %d error = %v", i, err)
		}
	}
}

func TestMCPServer_Tools_ReturnsCopy(t *testing.T) {
	s := NewMCPServer("test")
	s.tools = []MCPToolInfo{
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

func TestMCPServer_IsConnected(t *testing.T) {
	s := NewMCPServer("test")

	if s.IsConnected() {
		t.Error("new server should not be connected")
	}

	// We can't easily set a real client, but we verified the nil path
}

func TestMCPServer_Name(t *testing.T) {
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
			s := NewMCPServer(tt.expected)
			if got := s.Name(); got != tt.expected {
				t.Errorf("Name() = %q, want %q", got, tt.expected)
			}
		})
	}
}
