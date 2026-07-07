package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/v0lka/sp4rk/tools"
)

func TestBuildGroupedToolList_AllTiers(t *testing.T) {
	descriptors := []tools.ToolDescriptor{
		{Name: "read_file", Description: "reads a file", Source: "core"},
		{Name: "mcp_search", Description: "MCP search", Source: "mcp", SourceCategory: tools.SourceCategoryMCP},
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

func TestBuildGroupedToolList_MCPServerNames(t *testing.T) {
	descriptors := []tools.ToolDescriptor{
		{Name: "read_file", Description: "reads a file", Source: "core"},
		{Name: "mem_search", Description: "memory search", Source: "mcp:test-server", SourceCategory: tools.SourceCategoryMCP},
		{Name: "deploy", Description: "deploy tool", Source: "my-server", SourceCategory: tools.SourceCategoryMCP},
		{Name: "bash_exec", Description: "run bash", Source: "core"},
	}

	result := BuildGroupedToolList(descriptors)

	// Both tools with non-core sources should land in TIER 2
	if !strings.Contains(result, "TIER 2") {
		t.Error("expected TIER 2 label for external tools")
	}
	if !strings.Contains(result, "mem_search") {
		t.Error("expected mem_search in TIER 2 output")
	}
	if !strings.Contains(result, "deploy") {
		t.Error("expected deploy in TIER 2 output")
	}

	// Verify tier ordering: TIER 1 before TIER 2 before TIER 3
	t1 := strings.Index(result, "TIER 1")
	t2 := strings.Index(result, "TIER 2")
	t3 := strings.Index(result, "TIER 3")
	if t1 >= t2 || t2 >= t3 {
		t.Errorf("expected TIER 1 < TIER 2 < TIER 3 positions, got %d, %d, %d", t1, t2, t3)
	}

	// read_file should be in TIER 1, not TIER 2
	readIdx := strings.Index(result, "read_file")
	if readIdx < t1 || readIdx > t2 {
		t.Error("expected read_file in TIER 1 section")
	}

	// mem_search and deploy should be in TIER 2 section
	memIdx := strings.Index(result, "mem_search")
	deployIdx := strings.Index(result, "deploy")
	if memIdx < t2 || memIdx > t3 {
		t.Error("expected mem_search in TIER 2 section")
	}
	if deployIdx < t2 || deployIdx > t3 {
		t.Error("expected deploy in TIER 2 section")
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
