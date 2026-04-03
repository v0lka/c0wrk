package core

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/user/agent/internal/llm"
)

func TestConstitution_NewEmpty(t *testing.T) {
	// Create temp directory
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "constitution.json")

	// Create constitution with non-existent file
	c, err := NewConstitution(filePath)
	if err != nil {
		t.Fatalf("NewConstitution failed: %v", err)
	}

	// Should have empty principles
	if len(c.Principles()) != 0 {
		t.Errorf("Expected 0 principles, got %d", len(c.Principles()))
	}

	// Session count should be 0
	if c.SessionCount() != 0 {
		t.Errorf("Expected session count 0, got %d", c.SessionCount())
	}
}

func TestConstitution_LoadSave(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "constitution.json")

	// Create and add a principle
	c1, err := NewConstitution(filePath)
	if err != nil {
		t.Fatalf("NewConstitution failed: %v", err)
	}

	if err := c1.AddPrinciple("Always verify before acting"); err != nil {
		t.Fatalf("AddPrinciple failed: %v", err)
	}
	c1.IncrementSession()

	// Load from same file
	c2, err := NewConstitution(filePath)
	if err != nil {
		t.Fatalf("NewConstitution reload failed: %v", err)
	}

	// Verify principle was persisted
	principles := c2.Principles()
	if len(principles) != 1 {
		t.Fatalf("Expected 1 principle after reload, got %d", len(principles))
	}

	if principles[0].Principle != "Always verify before acting" {
		t.Errorf("Principle = %q, want %q", principles[0].Principle, "Always verify before acting")
	}

	if principles[0].Source != "user_defined" {
		t.Errorf("Source = %q, want %q", principles[0].Source, "user_defined")
	}

	// Verify session count was persisted
	if c2.SessionCount() != 1 {
		t.Errorf("Session count = %d, want 1", c2.SessionCount())
	}
}

func TestConstitution_AddPrinciple(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "constitution.json")

	c, err := NewConstitution(filePath)
	if err != nil {
		t.Fatalf("NewConstitution failed: %v", err)
	}

	// Add multiple principles
	if err := c.AddPrinciple("First principle"); err != nil {
		t.Fatalf("AddPrinciple 1 failed: %v", err)
	}
	if err := c.AddPrinciple("Second principle"); err != nil {
		t.Fatalf("AddPrinciple 2 failed: %v", err)
	}

	principles := c.Principles()
	if len(principles) != 2 {
		t.Fatalf("Expected 2 principles, got %d", len(principles))
	}

	// Verify IDs are unique
	if principles[0].ID == principles[1].ID {
		t.Errorf("Principle IDs should be unique: both are %q", principles[0].ID)
	}

	// Verify principles are in order
	if principles[0].Principle != "First principle" {
		t.Errorf("First principle = %q, want %q", principles[0].Principle, "First principle")
	}
	if principles[1].Principle != "Second principle" {
		t.Errorf("Second principle = %q, want %q", principles[1].Principle, "Second principle")
	}
}

func TestConstitution_ForPrompt(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "constitution.json")

	c, err := NewConstitution(filePath)
	if err != nil {
		t.Fatalf("NewConstitution failed: %v", err)
	}

	// Empty constitution should return empty string
	if c.ForPrompt() != "" {
		t.Errorf("Empty constitution ForPrompt should return empty string, got %q", c.ForPrompt())
	}

	// Add principles
	_ = c.AddPrinciple("Always test your code")
	_ = c.AddPrinciple("Document your assumptions")

	prompt := c.ForPrompt()

	// Should contain header
	if !strings.Contains(prompt, "Constitution Principles:") {
		t.Error("Prompt should contain 'Constitution Principles:' header")
	}

	// Should contain numbered principles
	if !strings.Contains(prompt, "1. Always test your code") {
		t.Error("Prompt should contain first principle with number")
	}
	if !strings.Contains(prompt, "2. Document your assumptions") {
		t.Error("Prompt should contain second principle with number")
	}
}

func TestConstitution_MetaReflect(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "constitution.json")

	c, err := NewConstitution(filePath)
	if err != nil {
		t.Fatalf("NewConstitution failed: %v", err)
	}

	// Create mock LLM
	mockLLM := &mockLLMCaller{
		responses: []*llm.ChatResponse{
			{
				Message: llm.Message{
					Role: "assistant",
					Content: `Based on the reflections, here are new principles:
1. Always check file existence before modifying
2. Run tests after each code change
3. Validate input parameters early`,
				},
				StopReason: "end_turn",
			},
		},
	}

	// Create reflections
	reflections := []StoredReflectionData{
		{
			TaskDescription: "Fix bug in user module",
			Summary:         "Failed to find file that was supposed to exist",
			Hypotheses:      []string{"File was deleted", "Wrong path"},
			SuggestedAction: "retry",
		},
		{
			TaskDescription: "Add new feature",
			Summary:         "Tests failed after changes",
			Hypotheses:      []string{"Didn't run tests", "Breaking change"},
			SuggestedAction: "replan",
		},
	}

	// Run meta-reflection
	if err := c.MetaReflect(context.Background(), reflections, mockLLM); err != nil {
		t.Fatalf("MetaReflect failed: %v", err)
	}

	// Should have new principles
	principles := c.Principles()
	if len(principles) < 2 {
		t.Errorf("Expected at least 2 principles from meta-reflection, got %d", len(principles))
	}

	// Verify source is meta_reflection
	for _, p := range principles {
		if p.Source != "meta_reflection" {
			t.Errorf("Principle source = %q, want %q", p.Source, "meta_reflection")
		}
	}
}

func TestConstitution_MetaReflect_EmptyReflections(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "constitution.json")

	c, err := NewConstitution(filePath)
	if err != nil {
		t.Fatalf("NewConstitution failed: %v", err)
	}

	mockLLM := &mockLLMCaller{}

	// Empty reflections should not call LLM
	if err := c.MetaReflect(context.Background(), []StoredReflectionData{}, mockLLM); err != nil {
		t.Fatalf("MetaReflect failed: %v", err)
	}

	if len(mockLLM.calls) != 0 {
		t.Error("LLM should not be called for empty reflections")
	}
}

func TestConstitution_SessionTracking(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "constitution.json")

	c, err := NewConstitution(filePath)
	if err != nil {
		t.Fatalf("NewConstitution failed: %v", err)
	}

	// Initial count should be 0
	if c.SessionCount() != 0 {
		t.Errorf("Initial session count = %d, want 0", c.SessionCount())
	}

	// Increment and verify
	c.IncrementSession()
	if c.SessionCount() != 1 {
		t.Errorf("After increment, session count = %d, want 1", c.SessionCount())
	}

	c.IncrementSession()
	c.IncrementSession()
	if c.SessionCount() != 3 {
		t.Errorf("After 3 increments, session count = %d, want 3", c.SessionCount())
	}
}

func TestConstitution_ShouldMetaReflect(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "constitution.json")

	c, err := NewConstitution(filePath)
	if err != nil {
		t.Fatalf("NewConstitution failed: %v", err)
	}

	// At session 0, should not trigger
	if c.ShouldMetaReflect(10) {
		t.Error("Should not trigger meta-reflect at session 0")
	}

	// Increment to 10
	for i := 0; i < 10; i++ {
		c.IncrementSession()
	}

	// At session 10 with interval 10, should trigger
	if !c.ShouldMetaReflect(10) {
		t.Error("Should trigger meta-reflect at session 10 with interval 10")
	}

	// At session 10 with interval 5, should trigger
	if !c.ShouldMetaReflect(5) {
		t.Error("Should trigger meta-reflect at session 10 with interval 5")
	}

	// At session 10 with interval 7, should not trigger
	if c.ShouldMetaReflect(7) {
		t.Error("Should not trigger meta-reflect at session 10 with interval 7")
	}

	// Increment to 11
	c.IncrementSession()

	// At session 11 with interval 10, should not trigger
	if c.ShouldMetaReflect(10) {
		t.Error("Should not trigger meta-reflect at session 11 with interval 10")
	}

	// Interval 0 should never trigger
	if c.ShouldMetaReflect(0) {
		t.Error("Interval 0 should never trigger meta-reflect")
	}
}

func TestConstitution_DirectoryCreation(t *testing.T) {
	tmpDir := t.TempDir()
	// Use nested directory that doesn't exist
	filePath := filepath.Join(tmpDir, "nested", "deep", "constitution.json")

	c, err := NewConstitution(filePath)
	if err != nil {
		t.Fatalf("NewConstitution failed: %v", err)
	}

	// Adding a principle should create directories
	if err := c.AddPrinciple("Test principle"); err != nil {
		t.Fatalf("AddPrinciple failed: %v", err)
	}

	// Verify file was created
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		t.Error("Constitution file was not created")
	}
}

func TestConstitution_DuplicatePrevention(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "constitution.json")

	c, err := NewConstitution(filePath)
	if err != nil {
		t.Fatalf("NewConstitution failed: %v", err)
	}

	// Add an initial principle
	_ = c.AddPrinciple("Always verify file existence before modifying")

	// Mock LLM that returns similar principle
	mockLLM := &mockLLMCaller{
		responses: []*llm.ChatResponse{
			{
				Message: llm.Message{
					Role:    "assistant",
					Content: "1. Always verify file existence before modifying files",
				},
				StopReason: "end_turn",
			},
		},
	}

	reflections := []StoredReflectionData{
		{
			TaskDescription: "Test task",
			Summary:         "Test summary",
		},
	}

	// Run meta-reflection
	_ = c.MetaReflect(context.Background(), reflections, mockLLM)

	// Should still have only 1 principle (duplicate not added)
	principles := c.Principles()
	if len(principles) != 1 {
		t.Errorf("Expected 1 principle (duplicate should be rejected), got %d", len(principles))
	}
}

func TestConstitution_PrinciplesCopy(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "constitution.json")

	c, err := NewConstitution(filePath)
	if err != nil {
		t.Fatalf("NewConstitution failed: %v", err)
	}

	_ = c.AddPrinciple("Original principle")

	// Get principles
	principles1 := c.Principles()

	// Modify the returned slice
	principles1[0].Principle = "Modified"

	// Get principles again
	principles2 := c.Principles()

	// Original should be unchanged
	if principles2[0].Principle != "Original principle" {
		t.Error("Principles() should return a copy, not the internal slice")
	}
}
