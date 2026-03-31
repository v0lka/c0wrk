package memory

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNewMemorySystem_AllComponents(t *testing.T) {
	// Create temp directory for test files
	tmpDir := t.TempDir()

	// Create skills directory with a test skill
	skillsDir := filepath.Join(tmpDir, "skills")
	if err := os.MkdirAll(skillsDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create a test skill directory with skill.json
	testSkillDir := filepath.Join(skillsDir, "test_skill")
	if err := os.MkdirAll(testSkillDir, 0755); err != nil {
		t.Fatal(err)
	}
	skillJSON := `{
		"name": "test_skill",
		"description": "A test skill",
		"version": "1.0.0"
	}`
	if err := os.WriteFile(filepath.Join(testSkillDir, "skill.json"), []byte(skillJSON), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := MemorySystemConfig{
		EpisodicDBPath: filepath.Join(tmpDir, "episodic.db"),
		SemanticDBPath: filepath.Join(tmpDir, "semantic.db"),
		SkillsDir:      skillsDir,
		Embedder:       newMockEmbedder(),
	}

	ms, err := NewMemorySystem(cfg)
	if err != nil {
		t.Fatalf("NewMemorySystem failed: %v", err)
	}
	defer func() { _ = ms.Close() }()

	// Verify all components are initialized
	if ms.Episodic == nil {
		t.Error("Episodic memory should not be nil")
	}
	if ms.Semantic == nil {
		t.Error("Semantic memory should not be nil")
	}
	if ms.Procedural == nil {
		t.Error("Procedural memory should not be nil")
	}

	// Verify procedural memory scanned the skill
	skills := ms.Procedural.ListSkills()
	if len(skills) != 1 {
		t.Errorf("Expected 1 skill, got %d", len(skills))
	}
	if len(skills) > 0 && skills[0].Name != "test_skill" {
		t.Errorf("Expected skill name 'test_skill', got '%s'", skills[0].Name)
	}
}

func TestNewMemorySystem_Partial(t *testing.T) {
	// Create temp directory for test files
	tmpDir := t.TempDir()

	// Only provide EpisodicDBPath
	cfg := MemorySystemConfig{
		EpisodicDBPath: filepath.Join(tmpDir, "episodic.db"),
	}

	ms, err := NewMemorySystem(cfg)
	if err != nil {
		t.Fatalf("NewMemorySystem failed: %v", err)
	}
	defer func() { _ = ms.Close() }()

	// Verify only episodic is initialized
	if ms.Episodic == nil {
		t.Error("Episodic memory should not be nil")
	}
	if ms.Semantic != nil {
		t.Error("Semantic memory should be nil")
	}
	if ms.Procedural != nil {
		t.Error("Procedural memory should be nil")
	}
}

func TestNewMemorySystem_Empty(t *testing.T) {
	// Empty config
	cfg := MemorySystemConfig{}

	ms, err := NewMemorySystem(cfg)
	if err != nil {
		t.Fatalf("NewMemorySystem failed: %v", err)
	}
	defer func() { _ = ms.Close() }()

	// All should be nil
	if ms.Episodic != nil {
		t.Error("Episodic memory should be nil")
	}
	if ms.Semantic != nil {
		t.Error("Semantic memory should be nil")
	}
	if ms.Procedural != nil {
		t.Error("Procedural memory should be nil")
	}
}

func TestMemorySystem_Close(t *testing.T) {
	// Create temp directory for test files
	tmpDir := t.TempDir()

	cfg := MemorySystemConfig{
		EpisodicDBPath: filepath.Join(tmpDir, "episodic.db"),
		SemanticDBPath: filepath.Join(tmpDir, "semantic.db"),
		Embedder:       newMockEmbedder(),
	}

	ms, err := NewMemorySystem(cfg)
	if err != nil {
		t.Fatalf("NewMemorySystem failed: %v", err)
	}

	// Close should not error
	if err := ms.Close(); err != nil {
		t.Errorf("Close failed: %v", err)
	}

	// Closing again should be safe (databases already closed)
	// Note: This may return an error depending on SQLite behavior, but shouldn't panic
	_ = ms.Close()
}

func TestNewMemorySystem_SemanticWithoutEmbedder(t *testing.T) {
	// Create temp directory for test files
	tmpDir := t.TempDir()

	// Provide SemanticDBPath but no Embedder
	cfg := MemorySystemConfig{
		SemanticDBPath: filepath.Join(tmpDir, "semantic.db"),
		Embedder:       nil, // no embedder
	}

	ms, err := NewMemorySystem(cfg)
	if err != nil {
		t.Fatalf("NewMemorySystem failed: %v", err)
	}
	defer func() { _ = ms.Close() }()

	// Semantic should be nil because embedder is required
	if ms.Semantic != nil {
		t.Error("Semantic memory should be nil without embedder")
	}
}

func TestNewMemorySystem_ProceduralNonExistentDir(t *testing.T) {
	// Create temp directory for test files
	tmpDir := t.TempDir()

	// Provide a non-existent skills directory
	cfg := MemorySystemConfig{
		SkillsDir: filepath.Join(tmpDir, "nonexistent_skills"),
	}

	ms, err := NewMemorySystem(cfg)
	if err != nil {
		t.Fatalf("NewMemorySystem failed: %v", err)
	}
	defer func() { _ = ms.Close() }()

	// Procedural should still be created, just with no skills
	if ms.Procedural == nil {
		t.Error("Procedural memory should not be nil")
	}
	skills := ms.Procedural.ListSkills()
	if len(skills) != 0 {
		t.Errorf("Expected 0 skills, got %d", len(skills))
	}
}
