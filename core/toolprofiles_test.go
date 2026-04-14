package core

import (
	"testing"
)

// TestToolProfilesMapContent verifies the ToolProfiles map has expected content
func TestToolProfilesMapContent(t *testing.T) {
	tests := []struct {
		role        string
		expectedLen int
		shouldExist bool
		expectNil   bool
	}{
		{"router", 0, true, false},     // empty slice
		{"planner", 6, true, false},    // 6 tools
		{"reflector", 7, true, false},  // 7 tools
		{"evaluator", 0, false, false}, // should not exist
		{"executor", 0, false, true},   // should not exist (gets all tools via nil)
		{"unknown", 0, false, false},   // should not exist
	}

	for _, tt := range tests {
		t.Run(tt.role, func(t *testing.T) {
			profile, exists := ToolProfiles[tt.role]

			if tt.shouldExist && !exists {
				t.Errorf("expected %s to exist in ToolProfiles", tt.role)
			}
			if !tt.shouldExist && exists {
				t.Errorf("expected %s to NOT exist in ToolProfiles", tt.role)
			}

			if !exists {
				return
			}

			if tt.expectNil && profile != nil {
				t.Errorf("expected %s profile to be nil", tt.role)
			}
			if !tt.expectNil && profile == nil {
				t.Errorf("expected %s profile to be non-nil", tt.role)
			}

			if profile != nil && len(profile) != tt.expectedLen {
				t.Errorf("expected %s to have %d tools, got %d", tt.role, tt.expectedLen, len(profile))
			}
		})
	}
}

// TestPlannerProfileContent verifies the planner profile has the expected tools
func TestPlannerProfileContent(t *testing.T) {
	profile := ToolProfiles["planner"]
	if profile == nil {
		t.Fatal("planner profile should not be nil")
	}

	expectedTools := map[string]bool{
		"read_file":      true,
		"list_directory": true,
		"search_files":   true,
		"search_content": true,
		"ripgrep":        true,
		"glob":           true,
	}

	if len(profile) != len(expectedTools) {
		t.Errorf("planner should have %d tools, got %d", len(expectedTools), len(profile))
	}

	for _, tool := range profile {
		if !expectedTools[tool] {
			t.Errorf("unexpected tool in planner profile: %s", tool)
		}
	}
}

// TestReflectorProfileContent verifies the reflector profile has the expected tools
func TestReflectorProfileContent(t *testing.T) {
	profile := ToolProfiles["reflector"]
	if profile == nil {
		t.Fatal("reflector profile should not be nil")
	}

	expectedTools := map[string]bool{
		"read_file":      true,
		"list_directory": true,
		"search_files":   true,
		"search_content": true,
		"ripgrep":        true,
		"glob":           true,
		"read_evidence":  true,
	}

	if len(profile) != len(expectedTools) {
		t.Errorf("reflector should have %d tools, got %d", len(expectedTools), len(profile))
	}

	for _, tool := range profile {
		if !expectedTools[tool] {
			t.Errorf("unexpected tool in reflector profile: %s", tool)
		}
	}
}
