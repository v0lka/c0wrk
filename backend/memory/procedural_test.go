package memory

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestProceduralMemory_Scan(t *testing.T) {
	// Create temp directory with 2 tool directories
	tempDir := t.TempDir()

	// Create tool1
	tool1Dir := filepath.Join(tempDir, "tool1")
	if err := os.Mkdir(tool1Dir, 0o755); err != nil {
		t.Fatalf("failed to create tool1 dir: %v", err)
	}
	tool1Manifest := `{
		"name": "tool1",
		"description": "First test tool",
		"version": "1.0.0",
		"language": "python",
		"capabilities": ["network"]
	}`
	if err := os.WriteFile(filepath.Join(tool1Dir, "tool.json"), []byte(tool1Manifest), 0o644); err != nil {
		t.Fatalf("failed to write tool1 manifest: %v", err)
	}

	// Create tool2
	tool2Dir := filepath.Join(tempDir, "tool2")
	if err := os.Mkdir(tool2Dir, 0o755); err != nil {
		t.Fatalf("failed to create tool2 dir: %v", err)
	}
	tool2Manifest := `{
		"name": "tool2",
		"description": "Second test tool",
		"version": "2.0.0",
		"language": "python",
		"capabilities": ["filesystem"]
	}`
	if err := os.WriteFile(filepath.Join(tool2Dir, "tool.json"), []byte(tool2Manifest), 0o644); err != nil {
		t.Fatalf("failed to write tool2 manifest: %v", err)
	}

	// Scan and verify
	pm := NewProceduralMemory(tempDir)
	if err := pm.Scan(); err != nil {
		t.Fatalf("Scan failed: %v", err)
	}

	tools := pm.ListTools()
	if len(tools) != 2 {
		t.Errorf("ListTools() returned %d tools, want 2", len(tools))
	}

	// Verify tool1
	s1, ok := pm.GetTool("tool1")
	if !ok {
		t.Error("GetTool(tool1) not found")
	} else {
		if s1.Description != "First test tool" {
			t.Errorf("tool1.Description = %q, want %q", s1.Description, "First test tool")
		}
		if s1.Version != "1.0.0" {
			t.Errorf("tool1.Version = %q, want %q", s1.Version, "1.0.0")
		}
		if s1.Path != tool1Dir {
			t.Errorf("tool1.Path = %q, want %q", s1.Path, tool1Dir)
		}
	}

	// Verify tool2
	s2, ok := pm.GetTool("tool2")
	if !ok {
		t.Error("GetTool(tool2) not found")
	} else {
		if s2.Description != "Second test tool" {
			t.Errorf("tool2.Description = %q, want %q", s2.Description, "Second test tool")
		}
		if s2.Version != "2.0.0" {
			t.Errorf("tool2.Version = %q, want %q", s2.Version, "2.0.0")
		}
	}
}

func TestProceduralMemory_ScanEmpty(t *testing.T) {
	tempDir := t.TempDir()

	pm := NewProceduralMemory(tempDir)
	if err := pm.Scan(); err != nil {
		t.Fatalf("Scan failed on empty dir: %v", err)
	}

	tools := pm.ListTools()
	if len(tools) != 0 {
		t.Errorf("ListTools() returned %d tools, want 0", len(tools))
	}
}

func TestProceduralMemory_ScanNonExistent(t *testing.T) {
	pm := NewProceduralMemory("/nonexistent/path/to/tools")
	if err := pm.Scan(); err != nil {
		t.Fatalf("Scan should not fail for nonexistent directory: %v", err)
	}

	tools := pm.ListTools()
	if len(tools) != 0 {
		t.Errorf("ListTools() returned %d tools, want 0", len(tools))
	}
}

func TestProceduralMemory_GetTool(t *testing.T) {
	tempDir := t.TempDir()

	// Create a tool
	toolDir := filepath.Join(tempDir, "mytool")
	if err := os.Mkdir(toolDir, 0o755); err != nil {
		t.Fatalf("failed to create tool dir: %v", err)
	}
	manifest := `{
		"name": "mytool",
		"description": "My tool",
		"version": "3.0.0",
		"language": "python",
		"capabilities": ["network", "filesystem"]
	}`
	if err := os.WriteFile(filepath.Join(toolDir, "tool.json"), []byte(manifest), 0o644); err != nil {
		t.Fatalf("failed to write manifest: %v", err)
	}

	pm := NewProceduralMemory(tempDir)
	if err := pm.Scan(); err != nil {
		t.Fatalf("Scan failed: %v", err)
	}

	// Test GetTool for existing tool
	tool, ok := pm.GetTool("mytool")
	if !ok {
		t.Error("GetTool(mytool) not found")
	}
	if tool.Name != "mytool" {
		t.Errorf("tool.Name = %q, want %q", tool.Name, "mytool")
	}
	if len(tool.Capabilities) != 2 {
		t.Errorf("tool.Capabilities = %v, want 2 items", tool.Capabilities)
	}

	// Test GetTool for non-existing tool
	_, ok = pm.GetTool("nonexistent")
	if ok {
		t.Error("GetTool(nonexistent) should return false")
	}
}

func TestProceduralMemory_Register(t *testing.T) {
	pm := NewProceduralMemory("")

	// Register a new tool
	info := &ExternalToolInfo{
		Name:         "registered_tool",
		Description:  "A registered tool",
		Version:      "1.0.0",
		Path:         "/path/to/tool",
		Language:     "python",
		Capabilities: []string{"network"},
	}
	pm.Register(info)

	// Verify it's retrievable
	tool, ok := pm.GetTool("registered_tool")
	if !ok {
		t.Error("GetTool(registered_tool) not found after Register")
	}
	if tool.Description != "A registered tool" {
		t.Errorf("tool.Description = %q, want %q", tool.Description, "A registered tool")
	}
	if tool.Path != "/path/to/tool" {
		t.Errorf("tool.Path = %q, want %q", tool.Path, "/path/to/tool")
	}

	// Verify ListTools includes it
	tools := pm.ListTools()
	if len(tools) != 1 {
		t.Errorf("ListTools() returned %d tools, want 1", len(tools))
	}

	// Register should update existing
	info2 := &ExternalToolInfo{
		Name:        "registered_tool",
		Description: "Updated description",
		Version:     "2.0.0",
		Path:        "/new/path",
		Language:    "python",
	}
	pm.Register(info2)

	tool, ok = pm.GetTool("registered_tool")
	if !ok {
		t.Error("GetTool(registered_tool) not found after update")
	}
	if tool.Description != "Updated description" {
		t.Errorf("tool.Description = %q, want %q", tool.Description, "Updated description")
	}
	if tool.Version != "2.0.0" {
		t.Errorf("tool.Version = %q, want %q", tool.Version, "2.0.0")
	}
}

func TestProceduralMemory_ScanIgnoresBadManifests(t *testing.T) {
	tempDir := t.TempDir()

	// Create good tool
	goodDir := filepath.Join(tempDir, "good_tool")
	if err := os.Mkdir(goodDir, 0o755); err != nil {
		t.Fatalf("failed to create good_tool dir: %v", err)
	}
	goodManifest := `{
		"name": "good_tool",
		"description": "A good tool",
		"version": "1.0.0",
		"language": "python"
	}`
	if err := os.WriteFile(filepath.Join(goodDir, "tool.json"), []byte(goodManifest), 0o644); err != nil {
		t.Fatalf("failed to write good manifest: %v", err)
	}

	// Create bad tool with invalid JSON
	badDir := filepath.Join(tempDir, "bad_tool")
	if err := os.Mkdir(badDir, 0o755); err != nil {
		t.Fatalf("failed to create bad_tool dir: %v", err)
	}
	badManifest := `{invalid json here`
	if err := os.WriteFile(filepath.Join(badDir, "tool.json"), []byte(badManifest), 0o644); err != nil {
		t.Fatalf("failed to write bad manifest: %v", err)
	}

	// Create tool with missing name
	noNameDir := filepath.Join(tempDir, "noname_tool")
	if err := os.Mkdir(noNameDir, 0o755); err != nil {
		t.Fatalf("failed to create noname_tool dir: %v", err)
	}
	noNameManifest := `{
		"description": "No name tool",
		"version": "1.0.0"
	}`
	if err := os.WriteFile(filepath.Join(noNameDir, "tool.json"), []byte(noNameManifest), 0o644); err != nil {
		t.Fatalf("failed to write noname manifest: %v", err)
	}

	// Scan should succeed and only load the good tool
	pm := NewProceduralMemory(tempDir)
	if err := pm.Scan(); err != nil {
		t.Fatalf("Scan failed: %v", err)
	}

	tools := pm.ListTools()
	if len(tools) != 1 {
		t.Errorf("ListTools() returned %d tools, want 1 (only good_tool)", len(tools))
	}

	_, ok := pm.GetTool("good_tool")
	if !ok {
		t.Error("GetTool(good_tool) not found")
	}
}

func TestProceduralMemory_IncrementUsage(t *testing.T) {
	pm := NewProceduralMemory("")

	// Register a tool
	info := &ExternalToolInfo{
		Name:        "test_tool",
		Description: "Test tool for increment usage",
		Version:     "1.0.0",
	}
	pm.Register(info)

	// Verify initial state
	tool, ok := pm.GetTool("test_tool")
	if !ok {
		t.Fatal("GetTool(test_tool) not found")
	}
	if tool.UsageCount != 0 {
		t.Errorf("initial UsageCount = %d, want 0", tool.UsageCount)
	}
	if tool.LastUsed != nil {
		t.Errorf("initial LastUsed = %v, want nil", tool.LastUsed)
	}

	// Increment usage
	before := time.Now()
	pm.IncrementUsage("test_tool")
	after := time.Now()

	// Verify incremented state
	tool, _ = pm.GetTool("test_tool")
	if tool.UsageCount != 1 {
		t.Errorf("UsageCount after first increment = %d, want 1", tool.UsageCount)
	}
	if tool.LastUsed == nil {
		t.Error("LastUsed is nil after increment, want non-nil")
	} else if tool.LastUsed.Before(before) || tool.LastUsed.After(after) {
		t.Errorf("LastUsed = %v, want between %v and %v", tool.LastUsed, before, after)
	}

	// Increment again
	pm.IncrementUsage("test_tool")
	tool, _ = pm.GetTool("test_tool")
	if tool.UsageCount != 2 {
		t.Errorf("UsageCount after second increment = %d, want 2", tool.UsageCount)
	}
}

func TestProceduralMemory_IncrementUsage_NonExistent(t *testing.T) {
	pm := NewProceduralMemory("")

	// Should not panic when incrementing non-existent tool
	pm.IncrementUsage("nonexistent_tool")

	// Verify no tool was created
	_, ok := pm.GetTool("nonexistent_tool")
	if ok {
		t.Error("GetTool(nonexistent_tool) should return false")
	}
}
