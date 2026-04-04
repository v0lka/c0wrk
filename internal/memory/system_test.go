package memory

import (
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestNewMemorySystem_AllComponents(t *testing.T) {
	// Create temp directory for test files
	tmpDir := t.TempDir()

	// Create tools directory with a test tool
	toolsDir := filepath.Join(tmpDir, "tools")
	if err := os.MkdirAll(toolsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Create a test tool directory with tool.json
	testToolDir := filepath.Join(toolsDir, "test_tool")
	if err := os.MkdirAll(testToolDir, 0o755); err != nil {
		t.Fatal(err)
	}
	toolJSON := `{
		"name": "test_tool",
		"description": "A test tool",
		"version": "1.0.0"
	}`
	if err := os.WriteFile(filepath.Join(testToolDir, "tool.json"), []byte(toolJSON), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := MemorySystemConfig{
		DBPath:   filepath.Join(tmpDir, "memory.db"),
		ToolsDir: toolsDir,
		Embedder: newMockEmbedder(),
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
	if ms.Reflexion == nil {
		t.Error("Reflexion memory should not be nil")
	}

	// Verify procedural memory scanned the tool
	tools := ms.Procedural.ListTools()
	if len(tools) != 1 {
		t.Errorf("Expected 1 tool, got %d", len(tools))
	}
	if len(tools) > 0 && tools[0].Name != "test_tool" {
		t.Errorf("Expected tool name 'test_tool', got '%s'", tools[0].Name)
	}
}

func TestNewMemorySystem_Partial(t *testing.T) {
	// Create temp directory for test files
	tmpDir := t.TempDir()

	// Only provide DBPath
	cfg := MemorySystemConfig{
		DBPath: filepath.Join(tmpDir, "memory.db"),
	}

	ms, err := NewMemorySystem(cfg)
	if err != nil {
		t.Fatalf("NewMemorySystem failed: %v", err)
	}
	defer func() { _ = ms.Close() }()

	// Verify episodic and reflexion are initialized (they only need DBPath)
	// Semantic should be nil (needs Embedder)
	// Procedural should be nil (needs ToolsDir)
	if ms.Episodic == nil {
		t.Error("Episodic memory should not be nil")
	}
	if ms.Reflexion == nil {
		t.Error("Reflexion memory should not be nil")
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
	if ms.Reflexion != nil {
		t.Error("Reflexion memory should be nil")
	}
}

func TestMemorySystem_Close(t *testing.T) {
	// Create temp directory for test files
	tmpDir := t.TempDir()

	cfg := MemorySystemConfig{
		DBPath:   filepath.Join(tmpDir, "memory.db"),
		Embedder: newMockEmbedder(),
	}

	ms, err := NewMemorySystem(cfg)
	if err != nil {
		t.Fatalf("NewMemorySystem failed: %v", err)
	}

	// Close should not error
	if err := ms.Close(); err != nil {
		t.Errorf("Close failed: %v", err)
	}

	// Closing again should be safe (database already closed)
	// Note: This may return an error depending on SQLite behavior, but shouldn't panic
	_ = ms.Close()
}

func TestNewMemorySystem_SemanticWithoutEmbedder(t *testing.T) {
	// Create temp directory for test files
	tmpDir := t.TempDir()

	// Provide DBPath but no Embedder
	cfg := MemorySystemConfig{
		DBPath:   filepath.Join(tmpDir, "memory.db"),
		Embedder: nil, // no embedder
	}

	ms, err := NewMemorySystem(cfg)
	if err != nil {
		t.Fatalf("NewMemorySystem failed: %v", err)
	}
	defer func() { _ = ms.Close() }()

	// Episodic and Reflexion should be initialized (only need DBPath)
	if ms.Episodic == nil {
		t.Error("Episodic memory should not be nil")
	}
	if ms.Reflexion == nil {
		t.Error("Reflexion memory should not be nil")
	}
	// Semantic should be nil because embedder is required
	if ms.Semantic != nil {
		t.Error("Semantic memory should be nil without embedder")
	}
}

func TestNewMemorySystem_ProceduralNonExistentDir(t *testing.T) {
	// Create temp directory for test files
	tmpDir := t.TempDir()

	// Provide a non-existent tools directory
	cfg := MemorySystemConfig{
		ToolsDir: filepath.Join(tmpDir, "nonexistent_tools"),
	}

	ms, err := NewMemorySystem(cfg)
	if err != nil {
		t.Fatalf("NewMemorySystem failed: %v", err)
	}
	defer func() { _ = ms.Close() }()

	// Procedural should still be created, just with no tools
	if ms.Procedural == nil {
		t.Error("Procedural memory should not be nil")
	}
	tools := ms.Procedural.ListTools()
	if len(tools) != 0 {
		t.Errorf("Expected 0 tools, got %d", len(tools))
	}
}
