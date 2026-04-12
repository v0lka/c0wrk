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

// mockFileOpsDescriptor creates a file_ops descriptor with the full schema
func mockFileOpsDescriptor() tools.ToolDescriptor {
	schema := `{
		"type": "object",
		"properties": {
			"action": {
				"type": "string",
				"enum": ["read_file", "write_file", "edit_file", "list_directory", "search_files", "search_content", "create_directory", "delete_directory", "delete_file"]
			},
			"path": {"type": "string"}
		},
		"required": ["action", "path"]
	}`
	return tools.ToolDescriptor{
		Name:        "file_ops",
		Description: "File operations tool",
		InputSchema: json.RawMessage(schema),
		Source:      "core",
	}
}

// TestRouterGetsNoTools verifies router profile returns empty tool list
func TestRouterGetsNoTools(t *testing.T) {
	allTools := []tools.ToolDescriptor{
		mockToolDescriptor("file_ops"),
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

// TestPlannerGetsReadOnlyTools verifies planner profile returns only file_ops, ripgrep, glob
// and that file_ops has write operations stripped
func TestPlannerGetsReadOnlyTools(t *testing.T) {
	allTools := []tools.ToolDescriptor{
		mockFileOpsDescriptor(),
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

	// Should have exactly 3 tools
	if len(filtered) != 3 {
		t.Errorf("planner should get exactly 3 tools, got %d", len(filtered))
	}

	// Check that expected tools are present
	toolNames := make(map[string]bool)
	for _, tool := range filtered {
		toolNames[tool.Name] = true
	}

	expectedTools := []string{"file_ops", "ripgrep", "glob"}
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

	// Verify file_ops has write operations stripped
	var fileOpsTool *tools.ToolDescriptor
	for i := range filtered {
		if filtered[i].Name == "file_ops" {
			fileOpsTool = &filtered[i]
			break
		}
	}

	if fileOpsTool == nil {
		t.Fatal("file_ops tool not found in filtered tools")
	}

	// Parse the schema and check enum values
	var schema map[string]interface{}
	if err := json.Unmarshal(fileOpsTool.InputSchema, &schema); err != nil {
		t.Fatalf("failed to unmarshal schema: %v", err)
	}

	properties, ok := schema["properties"].(map[string]interface{})
	if !ok {
		t.Fatal("schema missing 'properties' map")
	}
	actionProp, ok := properties["action"].(map[string]interface{})
	if !ok {
		t.Fatal("schema missing 'action' property")
	}
	enum, ok := actionProp["enum"].([]interface{})
	if !ok {
		t.Fatal("action property missing 'enum'")
	}

	// Should only have read-only actions
	allowedActions := map[string]bool{
		"read_file":      true,
		"list_directory": true,
		"search_files":   true,
		"search_content": true,
	}

	for _, action := range enum {
		actionStr, ok := action.(string)
		if !ok {
			t.Fatalf("enum value is not a string: %v", action)
		}
		if !allowedActions[actionStr] {
			t.Errorf("file_ops enum should not contain write action %s", actionStr)
		}
	}

	// Should have exactly 4 read-only actions
	if len(enum) != 4 {
		t.Errorf("file_ops should have exactly 4 read-only actions, got %d: %v", len(enum), enum)
	}
}

// TestReflectorGetsReadOnlyPlusEvidence verifies reflector gets file_ops, ripgrep, glob, read_evidence
func TestReflectorGetsReadOnlyPlusEvidence(t *testing.T) {
	allTools := []tools.ToolDescriptor{
		mockFileOpsDescriptor(),
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

	// Should have exactly 4 tools
	if len(filtered) != 4 {
		t.Errorf("reflector should get exactly 4 tools, got %d", len(filtered))
	}

	// Check that expected tools are present
	toolNames := make(map[string]bool)
	for _, tool := range filtered {
		toolNames[tool.Name] = true
	}

	expectedTools := []string{"file_ops", "ripgrep", "glob", "read_evidence"}
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
		mockToolDescriptor("file_ops"),
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
		mockToolDescriptor("file_ops"),
		mockToolDescriptor("ripgrep"),
	}

	// Request tools including one that doesn't exist
	allowedNames := []string{"file_ops", "unknown_tool", "ripgrep", "another_missing"}
	filtered := FilterToolsByProfile(allTools, allowedNames)

	// Should only have the 2 tools that exist
	if len(filtered) != 2 {
		t.Errorf("should get exactly 2 existing tools, got %d", len(filtered))
	}

	toolNames := make(map[string]bool)
	for _, tool := range filtered {
		toolNames[tool.Name] = true
	}

	if !toolNames["file_ops"] {
		t.Error("should have file_ops tool")
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
		mockToolDescriptor("file_ops"),
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

// TestFilterFileOpsDescriptorWithInvalidSchema verifies graceful handling of invalid schema
func TestFilterFileOpsDescriptorWithInvalidSchema(t *testing.T) {
	// Create a file_ops tool with invalid schema
	invalidFileOps := tools.ToolDescriptor{
		Name:        "file_ops",
		Description: "File operations",
		InputSchema: json.RawMessage(`{invalid json`),
		Source:      "core",
	}

	allTools := []tools.ToolDescriptor{invalidFileOps}
	filtered := FilterToolsByProfile(allTools, []string{"file_ops"})

	if len(filtered) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(filtered))
	}

	// Should return original (unmodified) when schema can't be parsed
	if string(filtered[0].InputSchema) != string(invalidFileOps.InputSchema) {
		t.Error("should return original schema when parsing fails")
	}
}

// TestFilterFileOpsDescriptorWithMissingProperties verifies graceful handling of missing properties
func TestFilterFileOpsDescriptorWithMissingProperties(t *testing.T) {
	// Schema without properties.action
	schema := `{"type":"object","properties":{"path":{"type":"string"}}}`
	fileOps := tools.ToolDescriptor{
		Name:        "file_ops",
		Description: "File operations",
		InputSchema: json.RawMessage(schema),
		Source:      "core",
	}

	allTools := []tools.ToolDescriptor{fileOps}
	filtered := FilterToolsByProfile(allTools, []string{"file_ops"})

	if len(filtered) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(filtered))
	}

	// Should return original when properties.action is missing
	if string(filtered[0].InputSchema) != string(fileOps.InputSchema) {
		t.Error("should return original schema when properties.action is missing")
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
		{"planner", 3, true, false},     // 3 tools
		{"reflector", 4, true, false},   // 4 tools
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
		"file_ops": true,
		"ripgrep":  true,
		"glob":     true,
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
		"file_ops":      true,
		"ripgrep":       true,
		"glob":          true,
		"read_evidence": true,
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
