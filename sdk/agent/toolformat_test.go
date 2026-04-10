package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/user/agent/sdk/tools"
)

func TestBuildGroupedToolList_AllTiers(t *testing.T) {
	descriptors := []tools.ToolDescriptor{
		{Name: "read_file", Description: "reads a file", Source: "core"},
		{Name: "mcp_search", Description: "MCP search", Source: "mcp"},
		{Name: "bash_exec", Description: "run bash", Source: "core"},
	}

	result := BuildGroupedToolList(descriptors)

	// Check all tier labels are present
	if !strings.Contains(result, "TIER 1") {
		t.Error("expected TIER 1 label for built-in tools")
	}
	if !strings.Contains(result, "TIER 2") {
		t.Error("expected TIER 2 label for MCP tools")
	}
	if !strings.Contains(result, "TIER 3") {
		t.Error("expected TIER 3 label for fallback tools")
	}

	// Check tool names are present
	if !strings.Contains(result, "read_file") {
		t.Error("expected read_file in output")
	}
	if !strings.Contains(result, "mcp_search") {
		t.Error("expected mcp_search in output")
	}
	if !strings.Contains(result, "bash_exec") {
		t.Error("expected bash_exec in output")
	}
}

func TestBuildGroupedToolList_Empty(t *testing.T) {
	result := BuildGroupedToolList(nil)
	if result != "" {
		t.Errorf("expected empty string for nil input, got %q", result)
	}

	result = BuildGroupedToolList([]tools.ToolDescriptor{})
	if result != "" {
		t.Errorf("expected empty string for empty input, got %q", result)
	}
}

func TestBuildGroupedToolList_SingleTier(t *testing.T) {
	descriptors := []tools.ToolDescriptor{
		{Name: "my_tool", Description: "does stuff", Source: "core"},
	}

	result := BuildGroupedToolList(descriptors)
	if !strings.Contains(result, "TIER 1") {
		t.Error("expected TIER 1 label")
	}
	// Other tiers should not appear
	if strings.Contains(result, "TIER 2") {
		t.Error("TIER 2 should not appear with no MCP tools")
	}
}

func TestBuildGroupedToolList_WithSchema(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}}}`)
	descriptors := []tools.ToolDescriptor{
		{Name: "read", Description: "read file", InputSchema: schema, Source: "core"},
	}
	result := BuildGroupedToolList(descriptors)
	if !strings.Contains(result, "- read: read file") {
		t.Errorf("expected formatted tool entry, got:\n%s", result)
	}
}
