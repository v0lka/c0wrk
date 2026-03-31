package mcp

import (
	"testing"

	"github.com/user/agent/internal/tools"
)

func TestMCPTool_DefaultPolicy(t *testing.T) {
	// Create a minimal MCPTool for testing
	server := &MCPServer{} // We need a minimal server, but DefaultPolicy doesn't use it
	tool := NewMCPTool(server, MCPToolInfo{
		Name:        "test_tool",
		Description: "A test MCP tool",
		InputSchema: []byte(`{"type": "object"}`),
	})

	if tool.DefaultPolicy() != tools.PolicyAuto {
		t.Errorf("expected DefaultPolicy() to return PolicyAuto, got %v", tool.DefaultPolicy())
	}
}

func TestMCPTool_ImplementsToolInterface(t *testing.T) {
	// Compile-time check that MCPTool implements Tool interface
	var _ tools.Tool = (*MCPTool)(nil)
}
