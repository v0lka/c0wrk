package skills

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseManifest_Valid(t *testing.T) {
	// Create temp directory and skill.json
	tempDir := t.TempDir()
	manifestPath := filepath.Join(tempDir, "skill.json")

	manifestContent := `{
		"name": "test_skill",
		"description": "A test skill",
		"version": "1.0.0",
		"language": "python",
		"entry_point": "main.py",
		"input_schema": {"type": "object"},
		"output_schema": {"type": "object"},
		"dependencies": ["requests"],
		"capabilities": ["network"]
	}`

	if err := os.WriteFile(manifestPath, []byte(manifestContent), 0644); err != nil {
		t.Fatalf("failed to write manifest: %v", err)
	}

	// Parse and verify
	manifest, err := ParseManifest(manifestPath)
	if err != nil {
		t.Fatalf("ParseManifest failed: %v", err)
	}

	if manifest.Name != "test_skill" {
		t.Errorf("Name = %q, want %q", manifest.Name, "test_skill")
	}
	if manifest.Description != "A test skill" {
		t.Errorf("Description = %q, want %q", manifest.Description, "A test skill")
	}
	if manifest.Version != "1.0.0" {
		t.Errorf("Version = %q, want %q", manifest.Version, "1.0.0")
	}
	if manifest.Language != "python" {
		t.Errorf("Language = %q, want %q", manifest.Language, "python")
	}
	if manifest.EntryPoint != "main.py" {
		t.Errorf("EntryPoint = %q, want %q", manifest.EntryPoint, "main.py")
	}
	if len(manifest.Dependencies) != 1 || manifest.Dependencies[0] != "requests" {
		t.Errorf("Dependencies = %v, want [requests]", manifest.Dependencies)
	}
	if len(manifest.Capabilities) != 1 || manifest.Capabilities[0] != "network" {
		t.Errorf("Capabilities = %v, want [network]", manifest.Capabilities)
	}
}

func TestParseManifest_InvalidJSON(t *testing.T) {
	tempDir := t.TempDir()
	manifestPath := filepath.Join(tempDir, "skill.json")

	// Write invalid JSON
	if err := os.WriteFile(manifestPath, []byte(`{not valid json`), 0644); err != nil {
		t.Fatalf("failed to write manifest: %v", err)
	}

	_, err := ParseManifest(manifestPath)
	if err == nil {
		t.Error("ParseManifest should fail for invalid JSON")
	}
}

func TestParseManifest_MissingRequired(t *testing.T) {
	tempDir := t.TempDir()
	manifestPath := filepath.Join(tempDir, "skill.json")

	// Write manifest missing required "name" field
	manifestContent := `{
		"description": "A test skill",
		"version": "1.0.0",
		"language": "python",
		"entry_point": "main.py"
	}`

	if err := os.WriteFile(manifestPath, []byte(manifestContent), 0644); err != nil {
		t.Fatalf("failed to write manifest: %v", err)
	}

	_, err := ParseManifest(manifestPath)
	if err == nil {
		t.Error("ParseManifest should fail when name is missing")
	}
}

func TestParseManifest_AllFields(t *testing.T) {
	tempDir := t.TempDir()
	manifestPath := filepath.Join(tempDir, "skill.json")

	// Write a full manifest with all fields
	manifestContent := `{
		"name": "full_skill",
		"description": "A skill with all fields",
		"version": "2.5.1",
		"language": "python",
		"entry_point": "run.py",
		"input_schema": {
			"type": "object",
			"properties": {
				"query": {"type": "string"}
			},
			"required": ["query"]
		},
		"output_schema": {
			"type": "object",
			"properties": {
				"result": {"type": "array"}
			}
		},
		"dependencies": ["requests", "beautifulsoup4", "lxml"],
		"capabilities": ["network", "filesystem"]
	}`

	if err := os.WriteFile(manifestPath, []byte(manifestContent), 0644); err != nil {
		t.Fatalf("failed to write manifest: %v", err)
	}

	manifest, err := ParseManifest(manifestPath)
	if err != nil {
		t.Fatalf("ParseManifest failed: %v", err)
	}

	// Verify all fields
	if manifest.Name != "full_skill" {
		t.Errorf("Name = %q, want %q", manifest.Name, "full_skill")
	}
	if manifest.Version != "2.5.1" {
		t.Errorf("Version = %q, want %q", manifest.Version, "2.5.1")
	}
	if manifest.EntryPoint != "run.py" {
		t.Errorf("EntryPoint = %q, want %q", manifest.EntryPoint, "run.py")
	}
	if len(manifest.Dependencies) != 3 {
		t.Errorf("Dependencies count = %d, want 3", len(manifest.Dependencies))
	}
	if len(manifest.Capabilities) != 2 {
		t.Errorf("Capabilities count = %d, want 2", len(manifest.Capabilities))
	}

	// Check input_schema was parsed
	if manifest.InputSchema == nil {
		t.Error("InputSchema should not be nil")
	}
	if manifest.OutputSchema == nil {
		t.Error("OutputSchema should not be nil")
	}
}

func TestValidate_MissingFields(t *testing.T) {
	tests := []struct {
		name     string
		manifest SkillManifest
		wantErr  string
	}{
		{
			name:     "missing name",
			manifest: SkillManifest{Description: "d", Version: "v", Language: "l", EntryPoint: "e"},
			wantErr:  "name is required",
		},
		{
			name:     "missing description",
			manifest: SkillManifest{Name: "n", Version: "v", Language: "l", EntryPoint: "e"},
			wantErr:  "description is required",
		},
		{
			name:     "missing version",
			manifest: SkillManifest{Name: "n", Description: "d", Language: "l", EntryPoint: "e"},
			wantErr:  "version is required",
		},
		{
			name:     "missing language",
			manifest: SkillManifest{Name: "n", Description: "d", Version: "v", EntryPoint: "e"},
			wantErr:  "language is required",
		},
		{
			name:     "missing entry_point",
			manifest: SkillManifest{Name: "n", Description: "d", Version: "v", Language: "l"},
			wantErr:  "entry_point is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.manifest.Validate()
			if err == nil {
				t.Errorf("Validate() should fail for %s", tt.name)
				return
			}
			if err.Error() != tt.wantErr {
				t.Errorf("Validate() error = %q, want %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestValidate_Valid(t *testing.T) {
	manifest := SkillManifest{
		Name:        "test",
		Description: "test desc",
		Version:     "1.0.0",
		Language:    "python",
		EntryPoint:  "main.py",
	}

	if err := manifest.Validate(); err != nil {
		t.Errorf("Validate() should pass for valid manifest: %v", err)
	}
}

func TestParseManifest_WithMetadataFields(t *testing.T) {
	tempDir := t.TempDir()
	manifestPath := filepath.Join(tempDir, "skill.json")

	// Write a manifest with created_at and created_by fields
	manifestContent := `{
		"name": "metadata_skill",
		"description": "A skill with metadata",
		"version": "1.0.0",
		"language": "python",
		"entry_point": "main.py",
		"created_at": "2025-03-25T10:00:00Z",
		"created_by": "test_user"
	}`

	if err := os.WriteFile(manifestPath, []byte(manifestContent), 0644); err != nil {
		t.Fatalf("failed to write manifest: %v", err)
	}

	manifest, err := ParseManifest(manifestPath)
	if err != nil {
		t.Fatalf("ParseManifest failed: %v", err)
	}

	if manifest.CreatedAt != "2025-03-25T10:00:00Z" {
		t.Errorf("CreatedAt = %q, want %q", manifest.CreatedAt, "2025-03-25T10:00:00Z")
	}
	if manifest.CreatedBy != "test_user" {
		t.Errorf("CreatedBy = %q, want %q", manifest.CreatedBy, "test_user")
	}
}

func TestParseManifest_WithoutMetadataFields(t *testing.T) {
	// Verify manifests without the new optional fields still work
	tempDir := t.TempDir()
	manifestPath := filepath.Join(tempDir, "skill.json")

	// Write a manifest without created_at and created_by fields
	manifestContent := `{
		"name": "simple_skill",
		"description": "A simple skill without metadata",
		"version": "1.0.0",
		"language": "python",
		"entry_point": "main.py"
	}`

	if err := os.WriteFile(manifestPath, []byte(manifestContent), 0644); err != nil {
		t.Fatalf("failed to write manifest: %v", err)
	}

	manifest, err := ParseManifest(manifestPath)
	if err != nil {
		t.Fatalf("ParseManifest failed: %v", err)
	}

	if manifest.CreatedAt != "" {
		t.Errorf("CreatedAt should be empty, got %q", manifest.CreatedAt)
	}
	if manifest.CreatedBy != "" {
		t.Errorf("CreatedBy should be empty, got %q", manifest.CreatedBy)
	}
}
