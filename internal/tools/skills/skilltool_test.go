package skills

import (
	"encoding/json"
	"testing"

	tools "github.com/user/agent/internal/tools"
)

func TestSkillTool_Name(t *testing.T) {
	manifest := &SkillManifest{
		Name:        "test-skill",
		Description: "A test skill",
		Version:     "1.0.0",
		Language:    "python",
		EntryPoint:  "main.py",
	}
	builder := NewDockerBuilder()
	st := NewSkillTool(manifest, "/tmp/skill", builder)

	if got := st.Name(); got != "test-skill" {
		t.Errorf("Name() = %q, want %q", got, "test-skill")
	}
}

func TestSkillTool_Description(t *testing.T) {
	manifest := &SkillManifest{
		Name:        "test-skill",
		Description: "A test skill for testing",
		Version:     "1.0.0",
		Language:    "python",
		EntryPoint:  "main.py",
	}
	builder := NewDockerBuilder()
	st := NewSkillTool(manifest, "/tmp/skill", builder)

	if got := st.Description(); got != "A test skill for testing" {
		t.Errorf("Description() = %q, want %q", got, "A test skill for testing")
	}
}

func TestSkillTool_InputSchema(t *testing.T) {
	manifest := &SkillManifest{
		Name:        "test-skill",
		Description: "A test skill",
		Version:     "1.0.0",
		Language:    "python",
		EntryPoint:  "main.py",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"query": map[string]interface{}{
					"type":        "string",
					"description": "The query to process",
				},
			},
			"required": []string{"query"},
		},
	}
	builder := NewDockerBuilder()
	st := NewSkillTool(manifest, "/tmp/skill", builder)

	schema := st.InputSchema()
	if len(schema) == 0 {
		t.Fatal("InputSchema() returned empty schema")
	}

	// Parse schema back to verify structure
	var parsed map[string]interface{}
	if err := json.Unmarshal(schema, &parsed); err != nil {
		t.Fatalf("InputSchema() returned invalid JSON: %v", err)
	}

	if parsed["type"] != "object" {
		t.Errorf("InputSchema type = %v, want %q", parsed["type"], "object")
	}
}

func TestSkillTool_InputSchema_EmptyManifest(t *testing.T) {
	manifest := &SkillManifest{
		Name:        "test-skill",
		Description: "A test skill",
		Version:     "1.0.0",
		Language:    "python",
		EntryPoint:  "main.py",
		// InputSchema is nil
	}
	builder := NewDockerBuilder()
	st := NewSkillTool(manifest, "/tmp/skill", builder)

	schema := st.InputSchema()
	// Should return valid JSON even with nil schema
	var parsed interface{}
	if err := json.Unmarshal(schema, &parsed); err != nil {
		t.Fatalf("InputSchema() returned invalid JSON: %v", err)
	}
}

func TestSkillTool_DefaultPolicy(t *testing.T) {
	manifest := &SkillManifest{
		Name:        "test-skill",
		Description: "A test skill",
		Version:     "1.0.0",
		Language:    "python",
		EntryPoint:  "main.py",
	}
	builder := NewDockerBuilder()
	st := NewSkillTool(manifest, "/tmp/skill", builder)

	if st.DefaultPolicy() != tools.PolicyAlwaysAllow {
		t.Errorf("expected DefaultPolicy() to return PolicyAlwaysAllow, got %v", st.DefaultPolicy())
	}
}

func TestSkillTool_ImplementsToolInterface(t *testing.T) {
	// Compile-time check that SkillTool implements Tool interface
	var _ tools.Tool = (*SkillTool)(nil)

	// Runtime check
	manifest := &SkillManifest{
		Name:        "test-skill",
		Description: "A test skill",
		Version:     "1.0.0",
		Language:    "python",
		EntryPoint:  "main.py",
	}
	builder := NewDockerBuilder()
	st := NewSkillTool(manifest, "/tmp/skill", builder)
	if st == nil {
		t.Fatal("NewSkillTool returned nil")
	}

	// Verify the tool can be assigned to the interface
	var tool tools.Tool = st
	_ = tool // Use the variable to satisfy the compiler
}
