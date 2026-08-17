package config

import (
	"testing"
)

// TestLoadVerifyOnEdit verifies the executor.verify_on_edit config section:
// snake_case YAML keys map onto VerifyOnEditConfig fields, and the zero value
// (section absent) stays a disabled no-op.
func TestLoadVerifyOnEdit(t *testing.T) {
	content := `
llm:
  default_model: claude-3-haiku
  anthropic:
    api_key: "test-key"
    models:
      - claude-3-haiku
executor:
  verify_on_edit:
    enabled: true
    command: "go test ./... -count=1"
    timeout: 3m
    max_output_chars: 2500
`
	configPath := writeTestConfig(t, content)

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}

	v := cfg.Executor.VerifyOnEdit
	if !v.Enabled {
		t.Error("Expected verify_on_edit.enabled = true")
	}
	if v.Command != "go test ./... -count=1" {
		t.Errorf("Expected command 'go test ./... -count=1', got %q", v.Command)
	}
	if v.Timeout != "3m" {
		t.Errorf("Expected timeout '3m', got %q", v.Timeout)
	}
	if v.MaxOutputChars != 2500 {
		t.Errorf("Expected max_output_chars 2500, got %d", v.MaxOutputChars)
	}
}

// TestLoadVerifyOnEdit_DefaultOff verifies the section is optional and
// disabled by default.
func TestLoadVerifyOnEdit_DefaultOff(t *testing.T) {
	content := `
llm:
  default_model: claude-3-haiku
  anthropic:
    api_key: "test-key"
    models:
      - claude-3-haiku
`
	configPath := writeTestConfig(t, content)

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}

	v := cfg.Executor.VerifyOnEdit
	if v.Enabled {
		t.Error("verify_on_edit must default to disabled")
	}
	if v.Command != "" || v.Timeout != "" || v.MaxOutputChars != 0 {
		t.Errorf("verify_on_edit zero value expected, got %+v", v)
	}
}
