package memory

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestProceduralMemory_Scan(t *testing.T) {
	// Create temp directory with 2 skill directories
	tempDir := t.TempDir()

	// Create skill1
	skill1Dir := filepath.Join(tempDir, "skill1")
	if err := os.Mkdir(skill1Dir, 0755); err != nil {
		t.Fatalf("failed to create skill1 dir: %v", err)
	}
	skill1Manifest := `{
		"name": "skill1",
		"description": "First test skill",
		"version": "1.0.0",
		"language": "python",
		"capabilities": ["network"]
	}`
	if err := os.WriteFile(filepath.Join(skill1Dir, "skill.json"), []byte(skill1Manifest), 0644); err != nil {
		t.Fatalf("failed to write skill1 manifest: %v", err)
	}

	// Create skill2
	skill2Dir := filepath.Join(tempDir, "skill2")
	if err := os.Mkdir(skill2Dir, 0755); err != nil {
		t.Fatalf("failed to create skill2 dir: %v", err)
	}
	skill2Manifest := `{
		"name": "skill2",
		"description": "Second test skill",
		"version": "2.0.0",
		"language": "python",
		"capabilities": ["filesystem"]
	}`
	if err := os.WriteFile(filepath.Join(skill2Dir, "skill.json"), []byte(skill2Manifest), 0644); err != nil {
		t.Fatalf("failed to write skill2 manifest: %v", err)
	}

	// Scan and verify
	pm := NewProceduralMemory(tempDir)
	if err := pm.Scan(); err != nil {
		t.Fatalf("Scan failed: %v", err)
	}

	skills := pm.ListSkills()
	if len(skills) != 2 {
		t.Errorf("ListSkills() returned %d skills, want 2", len(skills))
	}

	// Verify skill1
	s1, ok := pm.GetSkill("skill1")
	if !ok {
		t.Error("GetSkill(skill1) not found")
	} else {
		if s1.Description != "First test skill" {
			t.Errorf("skill1.Description = %q, want %q", s1.Description, "First test skill")
		}
		if s1.Version != "1.0.0" {
			t.Errorf("skill1.Version = %q, want %q", s1.Version, "1.0.0")
		}
		if s1.Path != skill1Dir {
			t.Errorf("skill1.Path = %q, want %q", s1.Path, skill1Dir)
		}
	}

	// Verify skill2
	s2, ok := pm.GetSkill("skill2")
	if !ok {
		t.Error("GetSkill(skill2) not found")
	} else {
		if s2.Description != "Second test skill" {
			t.Errorf("skill2.Description = %q, want %q", s2.Description, "Second test skill")
		}
		if s2.Version != "2.0.0" {
			t.Errorf("skill2.Version = %q, want %q", s2.Version, "2.0.0")
		}
	}
}

func TestProceduralMemory_ScanEmpty(t *testing.T) {
	tempDir := t.TempDir()

	pm := NewProceduralMemory(tempDir)
	if err := pm.Scan(); err != nil {
		t.Fatalf("Scan failed on empty dir: %v", err)
	}

	skills := pm.ListSkills()
	if len(skills) != 0 {
		t.Errorf("ListSkills() returned %d skills, want 0", len(skills))
	}
}

func TestProceduralMemory_ScanNonExistent(t *testing.T) {
	pm := NewProceduralMemory("/nonexistent/path/to/skills")
	if err := pm.Scan(); err != nil {
		t.Fatalf("Scan should not fail for nonexistent directory: %v", err)
	}

	skills := pm.ListSkills()
	if len(skills) != 0 {
		t.Errorf("ListSkills() returned %d skills, want 0", len(skills))
	}
}

func TestProceduralMemory_GetSkill(t *testing.T) {
	tempDir := t.TempDir()

	// Create a skill
	skillDir := filepath.Join(tempDir, "myskill")
	if err := os.Mkdir(skillDir, 0755); err != nil {
		t.Fatalf("failed to create skill dir: %v", err)
	}
	manifest := `{
		"name": "myskill",
		"description": "My skill",
		"version": "3.0.0",
		"language": "python",
		"capabilities": ["network", "filesystem"]
	}`
	if err := os.WriteFile(filepath.Join(skillDir, "skill.json"), []byte(manifest), 0644); err != nil {
		t.Fatalf("failed to write manifest: %v", err)
	}

	pm := NewProceduralMemory(tempDir)
	if err := pm.Scan(); err != nil {
		t.Fatalf("Scan failed: %v", err)
	}

	// Test GetSkill for existing skill
	skill, ok := pm.GetSkill("myskill")
	if !ok {
		t.Error("GetSkill(myskill) not found")
	}
	if skill.Name != "myskill" {
		t.Errorf("skill.Name = %q, want %q", skill.Name, "myskill")
	}
	if len(skill.Capabilities) != 2 {
		t.Errorf("skill.Capabilities = %v, want 2 items", skill.Capabilities)
	}

	// Test GetSkill for non-existing skill
	_, ok = pm.GetSkill("nonexistent")
	if ok {
		t.Error("GetSkill(nonexistent) should return false")
	}
}

func TestProceduralMemory_Register(t *testing.T) {
	pm := NewProceduralMemory("")

	// Register a new skill
	info := &SkillInfo{
		Name:         "registered_skill",
		Description:  "A registered skill",
		Version:      "1.0.0",
		Path:         "/path/to/skill",
		Language:     "python",
		Capabilities: []string{"network"},
	}
	pm.Register(info)

	// Verify it's retrievable
	skill, ok := pm.GetSkill("registered_skill")
	if !ok {
		t.Error("GetSkill(registered_skill) not found after Register")
	}
	if skill.Description != "A registered skill" {
		t.Errorf("skill.Description = %q, want %q", skill.Description, "A registered skill")
	}
	if skill.Path != "/path/to/skill" {
		t.Errorf("skill.Path = %q, want %q", skill.Path, "/path/to/skill")
	}

	// Verify ListSkills includes it
	skills := pm.ListSkills()
	if len(skills) != 1 {
		t.Errorf("ListSkills() returned %d skills, want 1", len(skills))
	}

	// Register should update existing
	info2 := &SkillInfo{
		Name:        "registered_skill",
		Description: "Updated description",
		Version:     "2.0.0",
		Path:        "/new/path",
		Language:    "python",
	}
	pm.Register(info2)

	skill, ok = pm.GetSkill("registered_skill")
	if !ok {
		t.Error("GetSkill(registered_skill) not found after update")
	}
	if skill.Description != "Updated description" {
		t.Errorf("skill.Description = %q, want %q", skill.Description, "Updated description")
	}
	if skill.Version != "2.0.0" {
		t.Errorf("skill.Version = %q, want %q", skill.Version, "2.0.0")
	}
}

func TestProceduralMemory_ScanIgnoresBadManifests(t *testing.T) {
	tempDir := t.TempDir()

	// Create good skill
	goodDir := filepath.Join(tempDir, "good_skill")
	if err := os.Mkdir(goodDir, 0755); err != nil {
		t.Fatalf("failed to create good_skill dir: %v", err)
	}
	goodManifest := `{
		"name": "good_skill",
		"description": "A good skill",
		"version": "1.0.0",
		"language": "python"
	}`
	if err := os.WriteFile(filepath.Join(goodDir, "skill.json"), []byte(goodManifest), 0644); err != nil {
		t.Fatalf("failed to write good manifest: %v", err)
	}

	// Create bad skill with invalid JSON
	badDir := filepath.Join(tempDir, "bad_skill")
	if err := os.Mkdir(badDir, 0755); err != nil {
		t.Fatalf("failed to create bad_skill dir: %v", err)
	}
	badManifest := `{invalid json here`
	if err := os.WriteFile(filepath.Join(badDir, "skill.json"), []byte(badManifest), 0644); err != nil {
		t.Fatalf("failed to write bad manifest: %v", err)
	}

	// Create skill with missing name
	noNameDir := filepath.Join(tempDir, "noname_skill")
	if err := os.Mkdir(noNameDir, 0755); err != nil {
		t.Fatalf("failed to create noname_skill dir: %v", err)
	}
	noNameManifest := `{
		"description": "No name skill",
		"version": "1.0.0"
	}`
	if err := os.WriteFile(filepath.Join(noNameDir, "skill.json"), []byte(noNameManifest), 0644); err != nil {
		t.Fatalf("failed to write noname manifest: %v", err)
	}

	// Scan should succeed and only load the good skill
	pm := NewProceduralMemory(tempDir)
	if err := pm.Scan(); err != nil {
		t.Fatalf("Scan failed: %v", err)
	}

	skills := pm.ListSkills()
	if len(skills) != 1 {
		t.Errorf("ListSkills() returned %d skills, want 1 (only good_skill)", len(skills))
	}

	_, ok := pm.GetSkill("good_skill")
	if !ok {
		t.Error("GetSkill(good_skill) not found")
	}
}

func TestProceduralMemory_IncrementUsage(t *testing.T) {
	pm := NewProceduralMemory("")

	// Register a skill
	info := &SkillInfo{
		Name:        "test_skill",
		Description: "Test skill for increment usage",
		Version:     "1.0.0",
	}
	pm.Register(info)

	// Verify initial state
	skill, ok := pm.GetSkill("test_skill")
	if !ok {
		t.Fatal("GetSkill(test_skill) not found")
	}
	if skill.UsageCount != 0 {
		t.Errorf("initial UsageCount = %d, want 0", skill.UsageCount)
	}
	if skill.LastUsed != nil {
		t.Errorf("initial LastUsed = %v, want nil", skill.LastUsed)
	}

	// Increment usage
	before := time.Now()
	pm.IncrementUsage("test_skill")
	after := time.Now()

	// Verify incremented state
	skill, _ = pm.GetSkill("test_skill")
	if skill.UsageCount != 1 {
		t.Errorf("UsageCount after first increment = %d, want 1", skill.UsageCount)
	}
	if skill.LastUsed == nil {
		t.Error("LastUsed is nil after increment, want non-nil")
	} else if skill.LastUsed.Before(before) || skill.LastUsed.After(after) {
		t.Errorf("LastUsed = %v, want between %v and %v", skill.LastUsed, before, after)
	}

	// Increment again
	pm.IncrementUsage("test_skill")
	skill, _ = pm.GetSkill("test_skill")
	if skill.UsageCount != 2 {
		t.Errorf("UsageCount after second increment = %d, want 2", skill.UsageCount)
	}
}

func TestProceduralMemory_IncrementUsage_NonExistent(t *testing.T) {
	pm := NewProceduralMemory("")

	// Should not panic when incrementing non-existent skill
	pm.IncrementUsage("nonexistent_skill")

	// Verify no skill was created
	_, ok := pm.GetSkill("nonexistent_skill")
	if ok {
		t.Error("GetSkill(nonexistent_skill) should return false")
	}
}
