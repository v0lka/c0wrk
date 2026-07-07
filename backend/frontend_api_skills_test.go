package backend

import (
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/v0lka/sp4rk/skills"
)

// writeSkillMD writes a minimal valid SKILL.md into dir.
func writeSkillMD(t *testing.T, dir, name, desc string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll %s: %v", dir, err)
	}
	content := "---\nname: " + name + "\ndescription: \"" + desc + "\"\n---\n\nBody.\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile SKILL.md: %v", err)
	}
}

// TestStartSkillsWatchers_SkipsMissingDirs verifies that non-existent skill
// directories are silently skipped without errors.
func TestStartSkillsWatchers_SkipsMissingDirs(t *testing.T) {
	f := &FrontendAPI{
		emitEvent: func(string, ...any) {},
	}
	f.startSkillsWatchers([]string{"/nonexistent/skills/dir"})
	if len(f.skillWatchers) != 0 {
		t.Errorf("expected 0 watchers for non-existent dir, got %d", len(f.skillWatchers))
	}
}

// TestStartSkillsWatchers_InvalidatesCacheOnChange verifies that a file change
// in a watched skill directory invalidates the skill cache and emits
// skills:changed so the frontend autocomplete refreshes.
func TestStartSkillsWatchers_InvalidatesCacheOnChange(t *testing.T) {
	skillDir := t.TempDir()
	// Create a skill subdirectory with a SKILL.md so the watcher has
	// something to monitor inside the skill dir.
	skillSubdir := filepath.Join(skillDir, "my-skill")
	writeSkillMD(t, skillSubdir, "my-skill", "A test skill.")

	var emitted atomic.Int32
	f := &FrontendAPI{
		emitEvent: func(name string, _ ...any) {
			if name == EventSkillsChanged {
				emitted.Add(1)
			}
		},
	}
	t.Cleanup(f.closeSkillsWatchers)

	f.startSkillsWatchers([]string{skillDir})
	if len(f.skillWatchers) != 1 {
		t.Fatalf("expected 1 skill watcher, got %d", len(f.skillWatchers))
	}

	// Populate the skill cache via ListSkills with a mock builder.
	mock := &mockBuilder{
		getSkillDescriptorsRes: []skills.SkillDescriptor{
			{Name: "my-skill", Description: "A test skill."},
		},
	}
	f.builderOverride = mock
	result := f.ListSkills()
	if len(result) != 1 || result[0].Name != "my-skill" {
		t.Fatalf("ListSkills initial: got %v", result)
	}
	if mock.getSkillDescriptorsCalls != 1 {
		t.Fatalf("expected 1 GetSkillDescriptors call, got %d", mock.getSkillDescriptorsCalls)
	}

	// Modify a file inside the skill directory to trigger the watcher.
	skillMD := filepath.Join(skillSubdir, "SKILL.md")
	if err := os.WriteFile(skillMD, []byte("---\nname: my-skill\ndescription: \"Updated.\"\n---\n\nBody.\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// Wait for the debounced skills:changed emission (fsnotify latency on
	// macOS + 200ms debounce in the watcher).
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if emitted.Load() >= 1 {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if emitted.Load() < 1 {
		t.Fatal("expected skills:changed emission after skill file modification")
	}

	// The cache must have been invalidated — the next ListSkills call
	// should re-invoke GetSkillDescriptors.
	mock.getSkillDescriptorsRes = []skills.SkillDescriptor{
		{Name: "my-skill", Description: "Updated."},
	}
	result = f.ListSkills()
	if len(result) != 1 || result[0].Description != "Updated." {
		t.Fatalf("ListSkills after change: got %v", result)
	}
	if mock.getSkillDescriptorsCalls != 2 {
		t.Fatalf("expected 2 GetSkillDescriptors calls (cache invalidated), got %d", mock.getSkillDescriptorsCalls)
	}
}

// TestCloseSkillsWatchers_Idempotent verifies that closeSkillsWatchers can be
// called multiple times safely.
func TestCloseSkillsWatchers_Idempotent(t *testing.T) {
	skillDir := t.TempDir()
	f := &FrontendAPI{
		emitEvent: func(string, ...any) {},
	}
	f.startSkillsWatchers([]string{skillDir})
	f.closeSkillsWatchers()
	f.closeSkillsWatchers() // must not panic
	if len(f.skillWatchers) != 0 {
		t.Errorf("expected 0 watchers after close, got %d", len(f.skillWatchers))
	}
}
