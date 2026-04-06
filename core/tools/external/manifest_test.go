package external

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseManifest_ValidPython(t *testing.T) {
	dir := t.TempDir()
	content := `{
		"name": "my_tool",
		"description": "A test tool",
		"version": "1.0.0",
		"language": "python",
		"entry_point": "main.py",
		"input_schema": {"type": "object", "properties": {"query": {"type": "string"}}},
		"default_policy": "always_allow",
		"created_at": "2025-01-01",
		"created_by": "test"
	}`
	path := filepath.Join(dir, "tool.json")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	m, err := ParseManifest(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.Name != "my_tool" {
		t.Errorf("Name = %q, want %q", m.Name, "my_tool")
	}
	if m.Description != "A test tool" {
		t.Errorf("Description = %q, want %q", m.Description, "A test tool")
	}
	if m.Version != "1.0.0" {
		t.Errorf("Version = %q, want %q", m.Version, "1.0.0")
	}
	if m.Language != "python" {
		t.Errorf("Language = %q, want %q", m.Language, "python")
	}
	if m.EntryPoint != "main.py" {
		t.Errorf("EntryPoint = %q, want %q", m.EntryPoint, "main.py")
	}
	if m.DefaultPolicy != "always_allow" {
		t.Errorf("DefaultPolicy = %q, want %q", m.DefaultPolicy, "always_allow")
	}
	if err := m.Validate(); err != nil {
		t.Errorf("Validate() unexpected error: %v", err)
	}
}

func TestParseManifest_ValidBash(t *testing.T) {
	dir := t.TempDir()
	content := `{
		"name": "bash_tool",
		"description": "A bash tool",
		"version": "0.1.0",
		"language": "bash",
		"entry_point": "run.sh",
		"input_schema": {}
	}`
	path := filepath.Join(dir, "tool.json")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	m, err := ParseManifest(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.Language != "bash" {
		t.Errorf("Language = %q, want %q", m.Language, "bash")
	}
	if err := m.Validate(); err != nil {
		t.Errorf("Validate() unexpected error: %v", err)
	}
}

func TestValidate_MissingName(t *testing.T) {
	m := &ToolManifest{Description: "d", Version: "1", Language: "python", EntryPoint: "m.py"}
	if err := m.Validate(); err == nil {
		t.Error("expected error for missing name")
	}
}

func TestValidate_MissingDescription(t *testing.T) {
	m := &ToolManifest{Name: "n", Version: "1", Language: "python", EntryPoint: "m.py"}
	if err := m.Validate(); err == nil {
		t.Error("expected error for missing description")
	}
}

func TestValidate_MissingVersion(t *testing.T) {
	m := &ToolManifest{Name: "n", Description: "d", Language: "python", EntryPoint: "m.py"}
	if err := m.Validate(); err == nil {
		t.Error("expected error for missing version")
	}
}

func TestValidate_MissingLanguage(t *testing.T) {
	m := &ToolManifest{Name: "n", Description: "d", Version: "1", EntryPoint: "m.py"}
	if err := m.Validate(); err == nil {
		t.Error("expected error for missing language")
	}
}

func TestValidate_MissingEntryPoint(t *testing.T) {
	m := &ToolManifest{Name: "n", Description: "d", Version: "1", Language: "python"}
	if err := m.Validate(); err == nil {
		t.Error("expected error for missing entry_point")
	}
}

func TestValidate_InvalidLanguage(t *testing.T) {
	m := &ToolManifest{Name: "n", Description: "d", Version: "1", Language: "ruby", EntryPoint: "m.rb"}
	if err := m.Validate(); err == nil {
		t.Error("expected error for invalid language")
	}
}

func TestParseManifest_NonExistentFile(t *testing.T) {
	_, err := ParseManifest("/nonexistent/path/tool.json")
	if err == nil {
		t.Error("expected error for non-existent file")
	}
}

func TestParseManifest_InvalidJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tool.json")
	if err := os.WriteFile(path, []byte(`{not valid json`), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := ParseManifest(path)
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}
