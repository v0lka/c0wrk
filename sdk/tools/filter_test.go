package tools

import (
	"encoding/json"
	"testing"
)

// mockToolDescriptor creates a ToolDescriptor for testing
func mockToolDescriptor(name string) ToolDescriptor {
	return ToolDescriptor{
		Name:        name,
		Description: "Test tool: " + name,
		InputSchema: json.RawMessage(`{"type":"object","properties":{"arg":{"type":"string"}}}`),
		Source:      "core",
	}
}

// TestFilterNilAllowedReturnsAll verifies nil profile returns all tools
func TestFilterNilAllowedReturnsAll(t *testing.T) {
	allTools := []ToolDescriptor{
		mockToolDescriptor("read_file"),
		mockToolDescriptor("list_directory"),
		mockToolDescriptor("write_file"),
		mockToolDescriptor("edit_file"),
		mockToolDescriptor("ripgrep"),
		mockToolDescriptor("glob"),
		mockToolDescriptor("bash_exec"),
		mockToolDescriptor("web_search"),
	}

	// nil profile means all tools
	filtered := FilterToolsByProfile(allTools, nil)

	if len(filtered) != len(allTools) {
		t.Errorf("nil profile should get all %d tools, got %d", len(allTools), len(filtered))
	}

	// Verify all tools are present
	toolNames := make(map[string]bool)
	for _, tool := range filtered {
		toolNames[tool.Name] = true
	}

	for _, original := range allTools {
		if !toolNames[original.Name] {
			t.Errorf("should have %s tool", original.Name)
		}
	}
}

// TestFilterEmptyAllowedReturnsNone verifies empty allowed list returns no tools
func TestFilterEmptyAllowedReturnsNone(t *testing.T) {
	allTools := []ToolDescriptor{
		mockToolDescriptor("read_file"),
		mockToolDescriptor("ripgrep"),
	}

	// Empty slice means no tools
	filtered := FilterToolsByProfile(allTools, []string{})

	if len(filtered) != 0 {
		t.Errorf("empty allowed list should return no tools, got %d", len(filtered))
	}
}

// TestFilterWithSpecificNames verifies filtering with specific names
func TestFilterWithSpecificNames(t *testing.T) {
	allTools := []ToolDescriptor{
		mockToolDescriptor("read_file"),
		mockToolDescriptor("list_directory"),
		mockToolDescriptor("ripgrep"),
		mockToolDescriptor("glob"),
		mockToolDescriptor("bash_exec"),
		mockToolDescriptor("web_search"),
	}

	allowedNames := []string{"read_file", "list_directory", "ripgrep", "glob"}
	filtered := FilterToolsByProfile(allTools, allowedNames)

	if len(filtered) != 4 {
		t.Errorf("should get exactly 4 tools, got %d", len(filtered))
	}

	toolNames := make(map[string]bool)
	for _, tool := range filtered {
		toolNames[tool.Name] = true
	}

	for _, expected := range allowedNames {
		if !toolNames[expected] {
			t.Errorf("should have %s tool", expected)
		}
	}

	unexpectedTools := []string{"bash_exec", "web_search"}
	for _, unexpected := range unexpectedTools {
		if toolNames[unexpected] {
			t.Errorf("should NOT have %s tool", unexpected)
		}
	}
}

// TestFilterWithUnknownTool verifies filtering with a tool name not in the registry
// gracefully returns only matching tools
func TestFilterWithUnknownTool(t *testing.T) {
	allTools := []ToolDescriptor{
		mockToolDescriptor("read_file"),
		mockToolDescriptor("ripgrep"),
	}

	// Request tools including one that doesn't exist
	allowedNames := []string{"read_file", "unknown_tool", "ripgrep", "another_missing"}
	filtered := FilterToolsByProfile(allTools, allowedNames)

	// Should only have the 2 tools that exist
	if len(filtered) != 2 {
		t.Errorf("should get exactly 2 existing tools, got %d", len(filtered))
	}

	toolNames := make(map[string]bool)
	for _, tool := range filtered {
		toolNames[tool.Name] = true
	}

	if !toolNames["read_file"] {
		t.Error("should have read_file tool")
	}
	if !toolNames["ripgrep"] {
		t.Error("should have ripgrep tool")
	}
	if toolNames["unknown_tool"] {
		t.Error("should NOT have unknown_tool")
	}
}

// TestFilterPreservesToolDescriptorFields verifies that filtering preserves all fields
func TestFilterPreservesToolDescriptorFields(t *testing.T) {
	original := ToolDescriptor{
		Name:        "test_tool",
		Description: "Test description",
		InputSchema: json.RawMessage(`{"type":"object"}`),
		Source:      "mcp:test",
	}

	allTools := []ToolDescriptor{original}
	filtered := FilterToolsByProfile(allTools, []string{"test_tool"})

	if len(filtered) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(filtered))
	}

	if filtered[0].Name != original.Name {
		t.Errorf("Name mismatch: got %q, want %q", filtered[0].Name, original.Name)
	}
	if filtered[0].Description != original.Description {
		t.Errorf("Description mismatch: got %q, want %q", filtered[0].Description, original.Description)
	}
	if string(filtered[0].InputSchema) != string(original.InputSchema) {
		t.Errorf("InputSchema mismatch: got %q, want %q", filtered[0].InputSchema, original.InputSchema)
	}
	if filtered[0].Source != original.Source {
		t.Errorf("Source mismatch: got %q, want %q", filtered[0].Source, original.Source)
	}
}
