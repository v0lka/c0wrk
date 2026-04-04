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

func TestToolCreatorTool_Descriptor(t *testing.T) {
	tool := NewToolCreatorTool("/tmp/tools", nil)

	if tool.Name() != "tool_creator" {
		t.Errorf("Name() = %q, want %q", tool.Name(), "tool_creator")
	}

	if tool.Description() == "" {
		t.Error("Description() should not be empty")
	}

	if !strings.Contains(tool.Description(), "external tool") {
		t.Errorf("Description() = %q, should contain 'external tool'", tool.Description())
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

	// Verify language field exists
	if _, exists := props["language"]; !exists {
		t.Error("InputSchema() should have property 'language'")
	}

	required, ok := schemaMap["required"].([]interface{})
	if !ok {
		t.Fatal("InputSchema() should have required field")
	}

	requiredStrs := make([]string, len(required))
	for i, r := range required {
		s, ok := r.(string)
		if !ok {
			t.Fatal("expected string in required array")
		}
		requiredStrs[i] = s
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

func TestToolCreatorTool_CreateTool(t *testing.T) {
	tmpDir := t.TempDir()
	registry := tools.NewToolRegistry()
	tool := NewToolCreatorTool(tmpDir, registry)

	input := map[string]interface{}{
		"name":        "test_tool",
		"description": "A test tool",
		"code":        "import json\nprint(json.dumps({'result': 'ok'}))",
	}
	inputJSON, _ := json.Marshal(input)

	result, err := tool.Execute(context.Background(), inputJSON)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.IsError {
		t.Fatalf("Execute() returned error: %s", result.Content)
	}

	// Verify tool directory was created
	toolDir := filepath.Join(tmpDir, "test_tool")
	if _, err := os.Stat(toolDir); os.IsNotExist(err) {
		t.Error("Tool directory was not created")
	}

	// Verify tool.json and main.py were created
	files := []string{"tool.json", "main.py"}
	for _, file := range files {
		path := filepath.Join(toolDir, file)
		if _, err := os.Stat(path); os.IsNotExist(err) {
			t.Errorf("File %s was not created", file)
		}
	}

	// Verify no requirements.txt was created
	reqPath := filepath.Join(toolDir, "requirements.txt")
	if _, err := os.Stat(reqPath); !os.IsNotExist(err) {
		t.Error("requirements.txt should not be created")
	}
}

func TestToolCreatorTool_CreateBashTool(t *testing.T) {
	tmpDir := t.TempDir()
	tool := NewToolCreatorTool(tmpDir, nil)

	bashCode := `#!/bin/bash
echo "Hello from bash"
`
	input := map[string]interface{}{
		"name":        "bash_tool",
		"description": "A bash tool",
		"code":        bashCode,
		"language":    "bash",
	}
	inputJSON, _ := json.Marshal(input)

	result, err := tool.Execute(context.Background(), inputJSON)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.IsError {
		t.Fatalf("Execute() returned error: %s", result.Content)
	}

	// Verify main.sh was written
	toolDir := filepath.Join(tmpDir, "bash_tool")
	mainShPath := filepath.Join(toolDir, "main.sh")
	mainShData, err := os.ReadFile(mainShPath)
	if err != nil {
		t.Fatalf("Failed to read main.sh: %v", err)
	}

	if string(mainShData) != bashCode {
		t.Errorf("main.sh content mismatch\ngot:\n%s\nwant:\n%s", string(mainShData), bashCode)
	}

	// Verify tool.json has correct language and entry_point
	manifestPath := filepath.Join(toolDir, "tool.json")
	manifestData, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("Failed to read tool.json: %v", err)
	}

	var manifest toolCreatorManifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		t.Fatalf("Failed to parse tool.json: %v", err)
	}

	if manifest.Language != "bash" {
		t.Errorf("Manifest language = %q, want %q", manifest.Language, "bash")
	}
	if manifest.EntryPoint != "main.sh" {
		t.Errorf("Manifest entry_point = %q, want %q", manifest.EntryPoint, "main.sh")
	}
}

func TestToolCreatorTool_ManifestContent(t *testing.T) {
	tmpDir := t.TempDir()
	tool := NewToolCreatorTool(tmpDir, nil)

	input := map[string]interface{}{
		"name":        "manifest_test",
		"description": "Testing manifest content",
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

	// Read and verify tool.json
	manifestPath := filepath.Join(tmpDir, "manifest_test", "tool.json")
	manifestData, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("Failed to read tool.json: %v", err)
	}

	var manifest toolCreatorManifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		t.Fatalf("Failed to parse tool.json: %v", err)
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
	if manifest.CreatedBy != "agent" {
		t.Errorf("Manifest created_by = %q, want %q", manifest.CreatedBy, "agent")
	}
	if manifest.CreatedAt == "" {
		t.Error("Manifest created_at should not be empty")
	}
}

func TestToolCreatorTool_MainPyContent(t *testing.T) {
	tmpDir := t.TempDir()
	tool := NewToolCreatorTool(tmpDir, nil)

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

func TestToolCreatorTool_AuditLog(t *testing.T) {
	tmpDir := t.TempDir()
	tool := NewToolCreatorTool(tmpDir, nil)

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
		t.Errorf("Audit log should contain tool name 'audit_test'")
	}
	if !strings.Contains(content, "created") {
		t.Errorf("Audit log should contain action 'created'")
	}
}

func TestToolCreatorTool_DuplicateName(t *testing.T) {
	tmpDir := t.TempDir()
	tool := NewToolCreatorTool(tmpDir, nil)

	input := map[string]interface{}{
		"name":        "duplicate_test",
		"description": "First tool",
		"code":        "print('first')",
	}
	inputJSON, _ := json.Marshal(input)

	// Create first tool
	result, err := tool.Execute(context.Background(), inputJSON)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.IsError {
		t.Fatalf("First Execute() returned error: %s", result.Content)
	}

	// Try to create duplicate
	input["description"] = "Second tool"
	input["code"] = "print('second')"
	inputJSON, _ = json.Marshal(input)

	result, err = tool.Execute(context.Background(), inputJSON)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !result.IsError {
		t.Error("Execute() should return error for duplicate tool name")
	}
	if !strings.Contains(result.Content, "already exists") {
		t.Errorf("Error message should mention 'already exists', got: %s", result.Content)
	}
}

func TestToolCreatorTool_InvalidName(t *testing.T) {
	tmpDir := t.TempDir()
	tool := NewToolCreatorTool(tmpDir, nil)

	invalidNames := []string{
		"123tool",    // starts with number
		"tool-name",  // contains hyphen
		"tool.name",  // contains dot
		"tool name",  // contains space
		"tool@name",  // contains special char
		"",           // empty
		"_underscore", // starts with underscore
	}

	for _, name := range invalidNames {
		input := map[string]interface{}{
			"name":        name,
			"description": "Test tool",
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

func TestToolCreatorTool_ValidNames(t *testing.T) {
	tmpDir := t.TempDir()
	tool := NewToolCreatorTool(tmpDir, nil)

	validNames := []string{
		"mytool",
		"tool_name",
		"toolName",
		"Tool123",
		"my_test_tool",
		"a",
		"A1",
	}

	for _, name := range validNames {
		input := map[string]interface{}{
			"name":        name,
			"description": "Test tool",
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

func TestToolCreatorTool_MissingParams(t *testing.T) {
	tmpDir := t.TempDir()
	tool := NewToolCreatorTool(tmpDir, nil)

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

func TestToolCreatorTool_DefaultPolicy(t *testing.T) {
	tool := NewToolCreatorTool("/tmp/tools", nil)
	if tool.DefaultPolicy() != tools.PolicyAlwaysAllow {
		t.Errorf("expected DefaultPolicy() to return PolicyAlwaysAllow, got %v", tool.DefaultPolicy())
	}
}

func TestToolCreatorTool_InvalidJSON(t *testing.T) {
	tmpDir := t.TempDir()
	tool := NewToolCreatorTool(tmpDir, nil)

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
