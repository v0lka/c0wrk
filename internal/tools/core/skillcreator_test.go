package core

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/user/agent/internal/tools"
)

// mockSkillBuilder is a mock implementation of SkillBuilder for testing.
type mockSkillBuilder struct {
	buildCalled   bool
	buildSkillDir string
	buildName     string
	buildVersion  string
	buildError    error
	buildImageTag string
}

func (m *mockSkillBuilder) Build(ctx context.Context, skillDir string, name string, version string) (string, error) {
	m.buildCalled = true
	m.buildSkillDir = skillDir
	m.buildName = name
	m.buildVersion = version
	if m.buildError != nil {
		return "", m.buildError
	}
	if m.buildImageTag != "" {
		return m.buildImageTag, nil
	}
	return "agent-skill-" + name + ":" + version, nil
}

func TestSkillCreatorTool_Descriptor(t *testing.T) {
	tool := NewSkillCreatorTool("/tmp/skills", nil, nil)

	if tool.Name() != "skill_creator" {
		t.Errorf("Name() = %q, want %q", tool.Name(), "skill_creator")
	}

	if tool.Description() == "" {
		t.Error("Description() should not be empty")
	}

	if !strings.Contains(tool.Description(), "Python skill") {
		t.Errorf("Description() = %q, should contain 'Python skill'", tool.Description())
	}

	schema := tool.InputSchema()
	if len(schema) == 0 {
		t.Error("InputSchema() should not be empty")
	}

	var schemaMap map[string]interface{}
	if err := json.Unmarshal(schema, &schemaMap); err != nil {
		t.Fatalf("InputSchema() is not valid JSON: %v", err)
	}

	if schemaMap["type"] != "object" {
		t.Errorf("InputSchema() type = %q, want %q", schemaMap["type"], "object")
	}

	props, ok := schemaMap["properties"].(map[string]interface{})
	if !ok {
		t.Fatal("InputSchema() should have properties")
	}

	requiredProps := []string{"name", "description", "code"}
	for _, prop := range requiredProps {
		if _, exists := props[prop]; !exists {
			t.Errorf("InputSchema() should have property %q", prop)
		}
	}

	required, ok := schemaMap["required"].([]interface{})
	if !ok {
		t.Fatal("InputSchema() should have required field")
	}

	requiredStrs := make([]string, len(required))
	for i, r := range required {
		requiredStrs[i] = r.(string)
	}

	for _, prop := range requiredProps {
		found := false
		for _, r := range requiredStrs {
			if r == prop {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("InputSchema() should require %q", prop)
		}
	}
}

func TestSkillCreatorTool_CreateSkill(t *testing.T) {
	tmpDir := t.TempDir()
	registry := tools.NewToolRegistry()
	tool := NewSkillCreatorTool(tmpDir, registry, nil)

	input := map[string]interface{}{
		"name":         "test_skill",
		"description":  "A test skill",
		"code":         "import json\nprint(json.dumps({'result': 'ok'}))",
		"dependencies": []string{"requests"},
		"capabilities": []string{"network"},
	}
	inputJSON, _ := json.Marshal(input)

	result, err := tool.Execute(context.Background(), inputJSON)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.IsError {
		t.Fatalf("Execute() returned error: %s", result.Content)
	}

	// Verify skill directory was created
	skillDir := filepath.Join(tmpDir, "test_skill")
	if _, err := os.Stat(skillDir); os.IsNotExist(err) {
		t.Error("Skill directory was not created")
	}

	// Verify files were created
	files := []string{"skill.json", "main.py", "requirements.txt"}
	for _, file := range files {
		path := filepath.Join(skillDir, file)
		if _, err := os.Stat(path); os.IsNotExist(err) {
			t.Errorf("File %s was not created", file)
		}
	}
}

func TestSkillCreatorTool_ManifestContent(t *testing.T) {
	tmpDir := t.TempDir()
	tool := NewSkillCreatorTool(tmpDir, nil, nil)

	input := map[string]interface{}{
		"name":         "manifest_test",
		"description":  "Testing manifest content",
		"code":         "print('hello')",
		"dependencies": []string{"requests", "numpy"},
		"capabilities": []string{"network", "filesystem"},
	}
	inputJSON, _ := json.Marshal(input)

	result, err := tool.Execute(context.Background(), inputJSON)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.IsError {
		t.Fatalf("Execute() returned error: %s", result.Content)
	}

	// Read and verify skill.json
	manifestPath := filepath.Join(tmpDir, "manifest_test", "skill.json")
	manifestData, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("Failed to read skill.json: %v", err)
	}

	var manifest skillManifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		t.Fatalf("Failed to parse skill.json: %v", err)
	}

	if manifest.Name != "manifest_test" {
		t.Errorf("Manifest name = %q, want %q", manifest.Name, "manifest_test")
	}
	if manifest.Description != "Testing manifest content" {
		t.Errorf("Manifest description = %q, want %q", manifest.Description, "Testing manifest content")
	}
	if manifest.Version != "1.0.0" {
		t.Errorf("Manifest version = %q, want %q", manifest.Version, "1.0.0")
	}
	if manifest.Language != "python" {
		t.Errorf("Manifest language = %q, want %q", manifest.Language, "python")
	}
	if manifest.EntryPoint != "main.py" {
		t.Errorf("Manifest entry_point = %q, want %q", manifest.EntryPoint, "main.py")
	}
	if len(manifest.Dependencies) != 2 {
		t.Errorf("Manifest dependencies count = %d, want %d", len(manifest.Dependencies), 2)
	}
	if len(manifest.Capabilities) != 2 {
		t.Errorf("Manifest capabilities count = %d, want %d", len(manifest.Capabilities), 2)
	}
}

func TestSkillCreatorTool_MainPyContent(t *testing.T) {
	tmpDir := t.TempDir()
	tool := NewSkillCreatorTool(tmpDir, nil, nil)

	pythonCode := `import json
import sys

def main():
    data = json.loads(sys.stdin.read())
    result = {"status": "ok", "input": data}
    print(json.dumps(result))

if __name__ == "__main__":
    main()
`
	input := map[string]interface{}{
		"name":        "mainpy_test",
		"description": "Testing main.py content",
		"code":        pythonCode,
	}
	inputJSON, _ := json.Marshal(input)

	result, err := tool.Execute(context.Background(), inputJSON)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.IsError {
		t.Fatalf("Execute() returned error: %s", result.Content)
	}

	// Read and verify main.py
	mainPyPath := filepath.Join(tmpDir, "mainpy_test", "main.py")
	mainPyData, err := os.ReadFile(mainPyPath)
	if err != nil {
		t.Fatalf("Failed to read main.py: %v", err)
	}

	if string(mainPyData) != pythonCode {
		t.Errorf("main.py content mismatch\ngot:\n%s\nwant:\n%s", string(mainPyData), pythonCode)
	}
}

func TestSkillCreatorTool_RequirementsTxt(t *testing.T) {
	tmpDir := t.TempDir()
	tool := NewSkillCreatorTool(tmpDir, nil, nil)

	input := map[string]interface{}{
		"name":         "requirements_test",
		"description":  "Testing requirements.txt",
		"code":         "print('hello')",
		"dependencies": []string{"requests", "numpy", "pandas"},
	}
	inputJSON, _ := json.Marshal(input)

	result, err := tool.Execute(context.Background(), inputJSON)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.IsError {
		t.Fatalf("Execute() returned error: %s", result.Content)
	}

	// Read and verify requirements.txt
	requirementsPath := filepath.Join(tmpDir, "requirements_test", "requirements.txt")
	requirementsData, err := os.ReadFile(requirementsPath)
	if err != nil {
		t.Fatalf("Failed to read requirements.txt: %v", err)
	}

	expected := "requests\nnumpy\npandas\n"
	if string(requirementsData) != expected {
		t.Errorf("requirements.txt content = %q, want %q", string(requirementsData), expected)
	}
}

func TestSkillCreatorTool_RequirementsTxtEmpty(t *testing.T) {
	tmpDir := t.TempDir()
	tool := NewSkillCreatorTool(tmpDir, nil, nil)

	input := map[string]interface{}{
		"name":        "requirements_empty_test",
		"description": "Testing empty requirements.txt",
		"code":        "print('hello')",
	}
	inputJSON, _ := json.Marshal(input)

	result, err := tool.Execute(context.Background(), inputJSON)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.IsError {
		t.Fatalf("Execute() returned error: %s", result.Content)
	}

	// Read and verify requirements.txt is empty
	requirementsPath := filepath.Join(tmpDir, "requirements_empty_test", "requirements.txt")
	requirementsData, err := os.ReadFile(requirementsPath)
	if err != nil {
		t.Fatalf("Failed to read requirements.txt: %v", err)
	}

	if string(requirementsData) != "" {
		t.Errorf("requirements.txt content = %q, want empty", string(requirementsData))
	}
}

func TestSkillCreatorTool_AuditLog(t *testing.T) {
	tmpDir := t.TempDir()
	tool := NewSkillCreatorTool(tmpDir, nil, nil)

	input := map[string]interface{}{
		"name":        "audit_test",
		"description": "Testing audit log",
		"code":        "print('hello')",
	}
	inputJSON, _ := json.Marshal(input)

	result, err := tool.Execute(context.Background(), inputJSON)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.IsError {
		t.Fatalf("Execute() returned error: %s", result.Content)
	}

	// Verify audit log was created and contains entry
	auditLogPath := filepath.Join(tmpDir, "audit.log")
	auditLogData, err := os.ReadFile(auditLogPath)
	if err != nil {
		t.Fatalf("Failed to read audit.log: %v", err)
	}

	content := string(auditLogData)
	if !strings.Contains(content, "audit_test") {
		t.Errorf("Audit log should contain skill name 'audit_test'")
	}
	if !strings.Contains(content, "created") {
		t.Errorf("Audit log should contain action 'created'")
	}
}

func TestSkillCreatorTool_DuplicateName(t *testing.T) {
	tmpDir := t.TempDir()
	tool := NewSkillCreatorTool(tmpDir, nil, nil)

	input := map[string]interface{}{
		"name":        "duplicate_test",
		"description": "First skill",
		"code":        "print('first')",
	}
	inputJSON, _ := json.Marshal(input)

	// Create first skill
	result, err := tool.Execute(context.Background(), inputJSON)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.IsError {
		t.Fatalf("First Execute() returned error: %s", result.Content)
	}

	// Try to create duplicate
	input["description"] = "Second skill"
	input["code"] = "print('second')"
	inputJSON, _ = json.Marshal(input)

	result, err = tool.Execute(context.Background(), inputJSON)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !result.IsError {
		t.Error("Execute() should return error for duplicate skill name")
	}
	if !strings.Contains(result.Content, "already exists") {
		t.Errorf("Error message should mention 'already exists', got: %s", result.Content)
	}
}

func TestSkillCreatorTool_InvalidName(t *testing.T) {
	tmpDir := t.TempDir()
	tool := NewSkillCreatorTool(tmpDir, nil, nil)

	invalidNames := []string{
		"123skill",      // starts with number
		"skill-name",    // contains hyphen
		"skill.name",    // contains dot
		"skill name",    // contains space
		"skill@name",    // contains special char
		"",              // empty
		"_underscore",   // starts with underscore
	}

	for _, name := range invalidNames {
		input := map[string]interface{}{
			"name":        name,
			"description": "Test skill",
			"code":        "print('hello')",
		}
		inputJSON, _ := json.Marshal(input)

		result, err := tool.Execute(context.Background(), inputJSON)
		if err != nil {
			t.Fatalf("Execute() error = %v for name %q", err, name)
		}
		if !result.IsError {
			t.Errorf("Execute() should return error for invalid name %q", name)
		}
	}
}

func TestSkillCreatorTool_ValidNames(t *testing.T) {
	tmpDir := t.TempDir()
	tool := NewSkillCreatorTool(tmpDir, nil, nil)

	validNames := []string{
		"skill",
		"skill_name",
		"skillName",
		"Skill123",
		"my_test_skill",
		"a",
		"A1",
	}

	for _, name := range validNames {
		input := map[string]interface{}{
			"name":        name,
			"description": "Test skill",
			"code":        "print('hello')",
		}
		inputJSON, _ := json.Marshal(input)

		result, err := tool.Execute(context.Background(), inputJSON)
		if err != nil {
			t.Fatalf("Execute() error = %v for name %q", err, name)
		}
		if result.IsError {
			t.Errorf("Execute() should not return error for valid name %q: %s", name, result.Content)
		}
	}
}

func TestSkillCreatorTool_MissingParams(t *testing.T) {
	tmpDir := t.TempDir()
	tool := NewSkillCreatorTool(tmpDir, nil, nil)

	tests := []struct {
		name        string
		input       map[string]interface{}
		wantContain string
	}{
		{
			name:        "missing name",
			input:       map[string]interface{}{"description": "test", "code": "print('hello')"},
			wantContain: "name",
		},
		{
			name:        "missing description",
			input:       map[string]interface{}{"name": "test", "code": "print('hello')"},
			wantContain: "description",
		},
		{
			name:        "missing code",
			input:       map[string]interface{}{"name": "test", "description": "test"},
			wantContain: "code",
		},
		{
			name:        "empty name",
			input:       map[string]interface{}{"name": "", "description": "test", "code": "print('hello')"},
			wantContain: "name",
		},
		{
			name:        "empty description",
			input:       map[string]interface{}{"name": "test_empty_desc", "description": "", "code": "print('hello')"},
			wantContain: "description",
		},
		{
			name:        "empty code",
			input:       map[string]interface{}{"name": "test_empty_code", "description": "test", "code": ""},
			wantContain: "code",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inputJSON, _ := json.Marshal(tt.input)
			result, err := tool.Execute(context.Background(), inputJSON)
			if err != nil {
				t.Fatalf("Execute() error = %v", err)
			}
			if !result.IsError {
				t.Error("Execute() should return error for missing required param")
			}
			if !strings.Contains(result.Content, tt.wantContain) {
				t.Errorf("Error message should mention %q, got: %s", tt.wantContain, result.Content)
			}
		})
	}
}

func TestSkillCreatorTool_WithMockBuilder(t *testing.T) {
	tmpDir := t.TempDir()
	mockBuilder := &mockSkillBuilder{
		buildImageTag: "agent-skill-mock_skill:1.0.0",
	}
	tool := NewSkillCreatorTool(tmpDir, nil, mockBuilder)

	input := map[string]interface{}{
		"name":        "mock_skill",
		"description": "Testing with mock builder",
		"code":        "print('hello')",
	}
	inputJSON, _ := json.Marshal(input)

	result, err := tool.Execute(context.Background(), inputJSON)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.IsError {
		t.Fatalf("Execute() returned error: %s", result.Content)
	}

	// Verify builder was called
	if !mockBuilder.buildCalled {
		t.Error("Builder.Build() was not called")
	}

	expectedDir := filepath.Join(tmpDir, "mock_skill")
	if mockBuilder.buildSkillDir != expectedDir {
		t.Errorf("Builder received skillDir = %q, want %q", mockBuilder.buildSkillDir, expectedDir)
	}
	if mockBuilder.buildName != "mock_skill" {
		t.Errorf("Builder received name = %q, want %q", mockBuilder.buildName, "mock_skill")
	}
	if mockBuilder.buildVersion != "1.0.0" {
		t.Errorf("Builder received version = %q, want %q", mockBuilder.buildVersion, "1.0.0")
	}

	// Verify result contains image tag
	if !strings.Contains(result.Content, "agent-skill-mock_skill:1.0.0") {
		t.Errorf("Result should contain image tag, got: %s", result.Content)
	}
}

func TestSkillCreatorTool_BuilderError(t *testing.T) {
	tmpDir := t.TempDir()
	mockBuilder := &mockSkillBuilder{
		buildError: os.ErrNotExist, // Simulate build error
	}
	tool := NewSkillCreatorTool(tmpDir, nil, mockBuilder)

	input := map[string]interface{}{
		"name":        "builder_error_test",
		"description": "Testing builder error",
		"code":        "print('hello')",
	}
	inputJSON, _ := json.Marshal(input)

	result, err := tool.Execute(context.Background(), inputJSON)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	// Should succeed even if builder fails (files are created)
	if result.IsError {
		t.Fatalf("Execute() should not return error even if builder fails: %s", result.Content)
	}

	// Verify warning is included
	if !strings.Contains(result.Content, "Warning") && !strings.Contains(result.Content, "failed") {
		t.Errorf("Result should contain warning about build failure, got: %s", result.Content)
	}

	// Verify files were still created
	skillDir := filepath.Join(tmpDir, "builder_error_test")
	files := []string{"skill.json", "main.py", "requirements.txt"}
	for _, file := range files {
		path := filepath.Join(skillDir, file)
		if _, err := os.Stat(path); os.IsNotExist(err) {
			t.Errorf("File %s should still be created even if builder fails", file)
		}
	}
}

func TestSkillCreatorTool_DefaultPolicy(t *testing.T) {
	tool := NewSkillCreatorTool("/tmp/skills", nil, nil)
	if tool.DefaultPolicy() != tools.PolicyAlwaysAllow {
		t.Errorf("expected DefaultPolicy() to return PolicyAlwaysAllow, got %v", tool.DefaultPolicy())
	}
}

func TestSkillCreatorTool_InvalidJSON(t *testing.T) {
	tmpDir := t.TempDir()
	tool := NewSkillCreatorTool(tmpDir, nil, nil)

	result, err := tool.Execute(context.Background(), json.RawMessage(`{invalid json}`))
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !result.IsError {
		t.Error("Execute() should return error for invalid JSON")
	}
	if !strings.Contains(result.Content, "parse") {
		t.Errorf("Error message should mention parsing, got: %s", result.Content)
	}
}
