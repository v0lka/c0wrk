package core

import (
	"strings"
	"testing"

	"github.com/v0lka/c0wrk/sdk/agent"
	tools "github.com/v0lka/c0wrk/sdk/tools"
)

func TestBuildGroupedToolList_AllTiers(t *testing.T) {
	descriptors := []tools.ToolDescriptor{
		{Name: "read_file", Description: "reads a file", Source: "core"},
		{Name: "mcp_tool", Description: "an mcp tool", Source: "mcp", SourceCategory: tools.SourceCategoryMCP},
		{Name: "bash_exec", Description: "run shell commands", Source: "core"},
	}

	result := agent.BuildGroupedToolList(descriptors)

	// Check tier headers
	if !strings.Contains(result, "TIER 1") {
		t.Error("expected TIER 1 header for built-in tools")
	}
	if !strings.Contains(result, "TIER 2") {
		t.Error("expected TIER 2 header for MCP tools")
	}
	if !strings.Contains(result, "TIER 3") {
		t.Error("expected TIER 3 header for fallback tools")
	}

	// Check tool entries
	if !strings.Contains(result, "- read_file: reads a file") {
		t.Error("expected read_file in output")
	}
	if !strings.Contains(result, "- mcp_tool: an mcp tool") {
		t.Error("expected mcp_tool in output")
	}
	if !strings.Contains(result, "- bash_exec: run shell commands") {
		t.Error("expected bash_exec in output")
	}
}

func TestBuildGroupedToolList_EmptyInput(t *testing.T) {
	result := agent.BuildGroupedToolList(nil)
	if result != "" {
		t.Errorf("expected empty string for nil input, got %q", result)
	}

	result = agent.BuildGroupedToolList([]tools.ToolDescriptor{})
	if result != "" {
		t.Errorf("expected empty string for empty input, got %q", result)
	}
}

func TestBuildGroupedToolList_OnlyOneTier(t *testing.T) {
	tests := []struct {
		name        string
		descriptors []tools.ToolDescriptor
		wantTier    string
		absentTiers []string
	}{
		{
			name: "only builtin",
			descriptors: []tools.ToolDescriptor{
				{Name: "read_file", Description: "read", Source: "core"},
			},
			wantTier:    "TIER 1",
			absentTiers: []string{"TIER 2", "TIER 3"},
		},
		{
			name: "only mcp",
			descriptors: []tools.ToolDescriptor{
				{Name: "mcp_tool", Description: "mcp desc", Source: "mcp", SourceCategory: tools.SourceCategoryMCP},
			},
			wantTier:    "TIER 2",
			absentTiers: []string{"TIER 1", "TIER 3"},
		},
		{
			name: "only fallback",
			descriptors: []tools.ToolDescriptor{
				{Name: "bash_exec", Description: "bash", Source: "core"},
			},
			wantTier:    "TIER 3",
			absentTiers: []string{"TIER 1", "TIER 2"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := agent.BuildGroupedToolList(tt.descriptors)
			if !strings.Contains(result, tt.wantTier) {
				t.Errorf("expected %s in output", tt.wantTier)
			}
			for _, absent := range tt.absentTiers {
				if strings.Contains(result, absent) {
					t.Errorf("did not expect %s in output", absent)
				}
			}
		})
	}
}

func TestBuildGroupedToolList_FallbackToolDetection(t *testing.T) {
	// bash_exec should be classified as fallback (Tier 3), not built-in
	descriptors := []tools.ToolDescriptor{
		{Name: "bash_exec", Description: "run shell commands", Source: "core"},
	}
	result := agent.BuildGroupedToolList(descriptors)

	if !strings.Contains(result, "Fallback") {
		t.Error("bash_exec should be in Fallback tier")
	}
	if strings.Contains(result, "Built-in") {
		t.Error("bash_exec should NOT be in Built-in tier")
	}
}
