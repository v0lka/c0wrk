package core

import (
	"encoding/json"
	"testing"

	tools "github.com/user/agent/sdk/tools"
)

// mockToolDescriptor creates a ToolDescriptor for testing
func mockToolDescriptor(name string) tools.ToolDescriptor {
	return tools.ToolDescriptor{
		Name:        name,
		Description: "Test tool: " + name,
		InputSchema: json.RawMessage(`{"type":"object","properties":{"arg":{"type":"string"}}}`),
		Source:      "core",
	}
}

// TestRouterGetsNoTools verifies router profile returns empty tool list
func TestRouterGetsNoTools(t *testing.T) {
	allTools := []tools.ToolDescriptor{
		mockToolDescriptor("read_file"),
		mockToolDescriptor("list_directory"),
		mockToolDescriptor("search_files"),
		mockToolDescriptor("search_content"),
		mockToolDescriptor("ripgrep"),
		mockToolDescriptor("glob"),
		mockToolDescriptor("bash_exec"),
	}

	// Router profile is an empty slice
	profile := ToolProfiles["router"]
	if profile == nil {
		t.Fatal("router profile should not be nil")
	}

	filtered := FilterToolsByProfile(allTools, profile)

	if len(filtered) != 0 {
		t.Errorf("router should get no tools, got %d: %v", len(filtered), filtered)
	}
}

// TestPlannerGetsReadOnlyTools verifies planner profile returns only read-only tools
func TestPlannerGetsReadOnlyTools(t *testing.T) {
	allTools := []tools.ToolDescriptor{
		mockToolDescriptor("read_file"),
		mockToolDescriptor("list_directory"),
		mockToolDescriptor("search_files"),
		mockToolDescriptor("search_content"),
		mockToolDescriptor("ripgrep"),
		mockToolDescriptor("glob"),
		mockToolDescriptor("bash_exec"),
		mockToolDescriptor("web_search"),
	}

	profile := ToolProfiles["planner"]
	if profile == nil {
		t.Fatal("planner profile should not be nil")
	}

	filtered := FilterToolsByProfile(allTools, profile)

	// Should have exactly 6 tools
	if len(filtered) != 6 {
		t.Errorf("planner should get exactly 6 tools, got %d", len(filtered))
	}

	// Check that expected tools are present
	toolNames := make(map[string]bool)
	for _, tool := range filtered {
		toolNames[tool.Name] = true
	}

	expectedTools := []string{"read_file", "list_directory", "search_files", "search_content", "ripgrep", "glob"}
	for _, expected := range expectedTools {
		if !toolNames[expected] {
			t.Errorf("planner should have %s tool", expected)
		}
	}

	// Check that unexpected tools are NOT present
	unexpectedTools := []string{"bash_exec", "web_search"}
	for _, unexpected := range unexpectedTools {
		if toolNames[unexpected] {
			t.Errorf("planner should NOT have %s tool", unexpected)
		}
	}
}

// TestReflectorGetsReadOnlyPlusEvidence verifies reflector gets read-only tools + read_evidence
func TestReflectorGetsReadOnlyPlusEvidence(t *testing.T) {
	allTools := []tools.ToolDescriptor{
		mockToolDescriptor("read_file"),
		mockToolDescriptor("list_directory"),
		mockToolDescriptor("search_files"),
		mockToolDescriptor("search_content"),
		mockToolDescriptor("ripgrep"),
		mockToolDescriptor("glob"),
		mockToolDescriptor("read_evidence"),
		mockToolDescriptor("bash_exec"),
		mockToolDescriptor("web_search"),
	}

	profile := ToolProfiles["reflector"]
	if profile == nil {
		t.Fatal("reflector profile should not be nil")
	}

	filtered := FilterToolsByProfile(allTools, profile)

	// Should have exactly 7 tools
	if len(filtered) != 7 {
		t.Errorf("reflector should get exactly 7 tools, got %d", len(filtered))
	}

	// Check that expected tools are present
	toolNames := make(map[string]bool)
	for _, tool := range filtered {
		toolNames[tool.Name] = true
	}

	expectedTools := []string{"read_file", "list_directory", "search_files", "search_content", "ripgrep", "glob", "read_evidence"}
	for _, expected := range expectedTools {
		if !toolNames[expected] {
			t.Errorf("reflector should have %s tool", expected)
		}
	}

	// Check that unexpected tools are NOT present
	unexpectedTools := []string{"bash_exec", "web_search"}
	for _, unexpected := range unexpectedTools {
		if toolNames[unexpected] {
			t.Errorf("reflector should NOT have %s tool", unexpected)
		}
	}
}

// TestExecutorGetsAllTools verifies nil profile returns all tools
func TestExecutorGetsAllTools(t *testing.T) {
	allTools := []tools.ToolDescriptor{
		mockToolDescriptor("read_file"),
		mockToolDescriptor("list_directory"),
		mockToolDescriptor("search_files"),
		mockToolDescriptor("search_content"),
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
		t.Errorf("executor (nil profile) should get all %d tools, got %d", len(allTools), len(filtered))
	}

	// Verify all tools are present
	toolNames := make(map[string]bool)
	for _, tool := range filtered {
		toolNames[tool.Name] = true
	}

	for _, original := range allTools {
		if !toolNames[original.Name] {
			t.Errorf("executor should have %s tool", original.Name)
		}
	}
}

// TestFilterWithUnknownTool verifies filtering with a tool name not in the registry
// gracefully returns only matching tools
func TestFilterWithUnknownTool(t *testing.T) {
	allTools := []tools.ToolDescriptor{
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

// TestFilterWithEmptyAllowedList verifies empty allowed list returns no tools
func TestFilterWithEmptyAllowedList(t *testing.T) {
	allTools := []tools.ToolDescriptor{
		mockToolDescriptor("read_file"),
		mockToolDescriptor("ripgrep"),
	}

	// Empty slice means no tools
	filtered := FilterToolsByProfile(allTools, []string{})

	if len(filtered) != 0 {
		t.Errorf("empty allowed list should return no tools, got %d", len(filtered))
	}
}

// TestFilterPreservesToolDescriptorFields verifies that filtering preserves all fields
func TestFilterPreservesToolDescriptorFields(t *testing.T) {
	original := tools.ToolDescriptor{
		Name:        "test_tool",
		Description: "Test description",
		InputSchema: json.RawMessage(`{"type":"object"}`),
		Source:      "mcp:test",
	}

	allTools := []tools.ToolDescriptor{original}
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

// TestToolProfilesMapContent verifies the ToolProfiles map has expected content
func TestToolProfilesMapContent(t *testing.T) {
	tests := []struct {
		role         string
		expectedLen  int
		shouldExist  bool
		expectNil    bool
	}{
		{"router", 0, true, false},      // empty slice
		{"planner", 6, true, false},     // 6 tools
		{"reflector", 7, true, false},   // 7 tools
		{"evaluator", 0, false, false},  // should not exist
		{"executor", 0, false, true},    // should not exist (gets all tools via nil)
		{"unknown", 0, false, false},    // should not exist
	}

	for _, tt := range tests {
		t.Run(tt.role, func(t *testing.T) {
			profile, exists := ToolProfiles[tt.role]

			if tt.shouldExist && !exists {
				t.Errorf("expected %s to exist in ToolProfiles", tt.role)
			}
			if !tt.shouldExist && exists {
				t.Errorf("expected %s to NOT exist in ToolProfiles", tt.role)
			}

			if !exists {
				return
			}

			if tt.expectNil && profile != nil {
				t.Errorf("expected %s profile to be nil", tt.role)
			}
			if !tt.expectNil && profile == nil {
				t.Errorf("expected %s profile to be non-nil", tt.role)
			}

			if profile != nil && len(profile) != tt.expectedLen {
				t.Errorf("expected %s to have %d tools, got %d", tt.role, tt.expectedLen, len(profile))
			}
		})
	}
}

// TestPlannerProfileContent verifies the planner profile has the expected tools
func TestPlannerProfileContent(t *testing.T) {
	profile := ToolProfiles["planner"]
	if profile == nil {
		t.Fatal("planner profile should not be nil")
	}

	expectedTools := map[string]bool{
		"read_file":      true,
		"list_directory": true,
		"search_files":   true,
		"search_content": true,
		"ripgrep":        true,
		"glob":           true,
	}

	if len(profile) != len(expectedTools) {
		t.Errorf("planner should have %d tools, got %d", len(expectedTools), len(profile))
	}

	for _, tool := range profile {
		if !expectedTools[tool] {
			t.Errorf("unexpected tool in planner profile: %s", tool)
		}
	}
}

// TestReflectorProfileContent verifies the reflector profile has the expected tools
func TestReflectorProfileContent(t *testing.T) {
	profile := ToolProfiles["reflector"]
	if profile == nil {
		t.Fatal("reflector profile should not be nil")
	}

	expectedTools := map[string]bool{
		"read_file":      true,
		"list_directory": true,
		"search_files":   true,
		"search_content": true,
		"ripgrep":        true,
		"glob":           true,
		"read_evidence":  true,
	}

	if len(profile) != len(expectedTools) {
		t.Errorf("reflector should have %d tools, got %d", len(expectedTools), len(profile))
	}

	for _, tool := range profile {
		if !expectedTools[tool] {
			t.Errorf("unexpected tool in reflector profile: %s", tool)
		}
	}
}
