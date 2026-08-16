package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// TestExampleConfigReflectsGroupsSchema loads the shipped config.example.yaml
// through the real pipeline and asserts it uses the groups schema cleanly:
// no load warnings and an execute blacklist identical to the compiled-in
// default union. This keeps the example from drifting away from the code when
// defaults change.
func TestExampleConfigReflectsGroupsSchema(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")

	data, err := os.ReadFile("../../config.example.yaml")
	if err != nil {
		t.Fatalf("read config.example.yaml: %v", err)
	}
	if err := os.WriteFile(configPath, data, 0o644); err != nil {
		t.Fatalf("write temp config: %v", err)
	}

	result, err := LoadWithResult(configPath)
	if err != nil {
		t.Fatalf("LoadWithResult(config.example.yaml): %v", err)
	}
	if len(result.LoadErrors) != 0 {
		t.Errorf("config.example.yaml must load without warnings, got %v", result.LoadErrors)
	}

	// Documented defaults must match the compiled-in defaults.
	for group, want := range defaultToolGroupPolicies {
		got, ok := result.Config.Security.Groups[group]
		if !ok {
			t.Errorf("config.example.yaml is missing group %q", group)
			continue
		}
		if got.Policy != want {
			t.Errorf("example group %q policy = %q, want default %q", group, got.Policy, want)
		}
	}
	if !reflect.DeepEqual(result.Config.Security.Groups[ToolGroupExecute].Blacklist, DefaultExecuteGroupBlacklist()) {
		t.Error("example execute blacklist differs from the compiled-in default union")
	}

	// No backup side effects for a groups-based example.
	backups, _ := filepath.Glob(configPath + ".bak-*")
	if len(backups) != 0 {
		t.Errorf("loading the example must not rewrite/backup, got %v", backups)
	}
}
