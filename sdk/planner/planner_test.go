package planner

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/v0lka/c0wrk/sdk/tools"
)

func TestParsePlanResponse_ExtractsSummary(t *testing.T) {
	p := &Planner{}
	input := `{"steps": [{"id": "step_1", "summary": "Setup auth module", "description": "What: Create authentication module\nHow: Use JWT tokens\nWhere: auth/\nAcceptance Criteria:\n- Module compiles", "depends_on": [], "parallelizable": false}]}`
	plan, err := p.parsePlanResponse(input, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(plan.Steps) != 1 {
		t.Fatalf("expected 1 step, got %d", len(plan.Steps))
	}
	if plan.Steps[0].Summary != "Setup auth module" {
		t.Errorf("expected summary 'Setup auth module', got %q", plan.Steps[0].Summary)
	}
}

func TestParsePlanResponse_EdgeCases(t *testing.T) {
	p := &Planner{}

	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{
			name:  "valid JSON with steps",
			input: `{"steps":[{"id":"step_1","description":"do thing"}]}`,
		},
		{
			name:  "JSON in markdown code block",
			input: "```json\n{\"steps\":[{\"id\":\"step_1\",\"description\":\"do thing\"}]}\n```",
		},
		{
			name:  "JSON in plain code block",
			input: "```\n{\"steps\":[{\"id\":\"step_1\",\"description\":\"do thing\"}]}\n```",
		},
		{
			name:    "no JSON in response",
			input:   "here is some text with no json",
			wantErr: true,
		},
		{
			name:    "empty string",
			input:   "",
			wantErr: true,
		},
		{
			name:    "invalid JSON",
			input:   `{invalid json}`,
			wantErr: true,
		},
		{
			name:  "JSON with surrounding text",
			input: `Here is the plan: {"steps":[{"id":"step_1","description":"test"}]} end`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan, err := p.parsePlanResponse(tt.input, nil)
			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if plan == nil {
				t.Fatal("expected non-nil plan")
			}
			if len(plan.Steps) == 0 {
				t.Error("expected at least one step in plan")
			}
		})
	}
}

// mockToolRegistry implements ToolRegistry for testing.
type mockToolRegistry struct {
	tools []tools.ToolDescriptor
}

func (m *mockToolRegistry) List() []tools.ToolDescriptor { return m.tools }
func (m *mockToolRegistry) Execute(_ context.Context, _ string, _ json.RawMessage) (tools.ToolResult, error) {
	return tools.ToolResult{}, nil
}
func (m *mockToolRegistry) GetToolSource(_ string) string { return "" }
func (m *mockToolRegistry) IsToolUntrusted(_ string) bool { return false }

func TestGetPlannerTools(t *testing.T) {
	t.Run("mixed_registry_fs_only", func(t *testing.T) {
		reg := &mockToolRegistry{tools: []tools.ToolDescriptor{
			{Name: "get_architecture", SourceCategory: tools.SourceCategoryMCP},
			{Name: "query_codebase", SourceCategory: tools.SourceCategoryMCP},
			{Name: "read_file"},
			{Name: "list_directory"},
			{Name: "glob"},
			{Name: "ripgrep"},
			{Name: "write_file"}, // should NOT be included
		}}

		p := &Planner{Cfg: Config{
			ToolRegistry:     reg,
			PlannerToolNames: map[string]bool{"read_file": true, "list_directory": true, "glob": true, "ripgrep": true},
		}}
		result := p.getPlannerTools()

		names := map[string]bool{}
		for _, td := range result {
			names[td.Name] = true
		}

		expectedFS := []string{"read_file", "list_directory", "glob", "ripgrep"}
		for _, e := range expectedFS {
			if !names[e] {
				t.Errorf("expected tool %q in result", e)
			}
		}
		if names["write_file"] {
			t.Error("write_file should NOT be included in planner tools")
		}
		if names["get_architecture"] {
			t.Error("MCP tool get_architecture should NOT be included in planner tools")
		}
		if names["query_codebase"] {
			t.Error("MCP tool query_codebase should NOT be included in planner tools")
		}
	})

	t.Run("fs_only", func(t *testing.T) {
		reg := &mockToolRegistry{tools: []tools.ToolDescriptor{
			{Name: "read_file"},
			{Name: "glob"},
		}}

		p := &Planner{Cfg: Config{
			ToolRegistry:     reg,
			PlannerToolNames: map[string]bool{"read_file": true, "glob": true},
		}}
		result := p.getPlannerTools()

		names := map[string]bool{}
		for _, td := range result {
			names[td.Name] = true
		}

		if !names["read_file"] || !names["glob"] {
			t.Errorf("expected FS tools in result, got %v", names)
		}
	})

	t.Run("nil_registry", func(t *testing.T) {
		p := &Planner{}
		result := p.getPlannerTools()
		if result != nil {
			t.Errorf("expected nil for nil registry, got %v", result)
		}
	})
}
