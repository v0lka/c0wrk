// SPDX-License-Identifier: Apache-2.0

package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// loadShellExecYAML is a small helper that loads a config from an in-memory YAML
// document through the real pipeline.
func loadShellExecYAML(t *testing.T, doc string) (cfg *Config, warnings []string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(doc), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	result, err := LoadWithResult(path)
	if err != nil {
		t.Fatalf("LoadWithResult: %v", err)
	}
	return result.Config, result.LoadErrors
}

// minimalConfigYAML is the smallest document the loader accepts (default_model
// + one provider with an enabled model).
func minimalConfigYAML(shellExec string) string {
	doc := `
llm:
  default_model: claude-3-haiku
  anthropic:
    api_key: "test-key"
    models:
      - claude-3-haiku
`
	if shellExec != "" {
		doc += "\n" + shellExec + "\n"
	}
	return doc
}

func TestShellExec_ValidOverrideLoads(t *testing.T) {
	cfg, warnings := loadShellExecYAML(t, minimalConfigYAML(`
shell_exec:
  bash_exec:
    command: ["/opt/homebrew/bin/zsh", "-c", "{command}"]
    shell: zsh
`))
	if len(warnings) != 0 {
		t.Fatalf("valid override must load without warnings, got %v", warnings)
	}
	tool := cfg.ShellExec.BashExec
	if !tool.OverrideActive() {
		t.Fatal("override must be active")
	}
	if tool.Shell != "zsh" {
		t.Errorf("shell = %q, want zsh", tool.Shell)
	}
	if tool.Command[0] != "/opt/homebrew/bin/zsh" {
		t.Errorf("binary = %q", tool.Command[0])
	}
	// The other platform's entry stays inert but loadable.
	if cfg.ShellExec.PoshExec.OverrideActive() {
		t.Error("posh override must be inactive")
	}
}

func TestShellExec_OmittedShellSeedsPlatformDefault(t *testing.T) {
	cfg, warnings := loadShellExecYAML(t, minimalConfigYAML(`
shell_exec:
  bash_exec:
    command: ["/opt/homebrew/bin/bash", "-c", "{command}"]
  posh_exec:
    command: ["pwsh.exe", "-NoProfile", "-Command", "{command}"]
`))
	if len(warnings) != 0 {
		t.Fatalf("omitted shell is seeded silently, got warnings %v", warnings)
	}
	if got := cfg.ShellExec.BashExec.Shell; got != DefaultShellKindBash {
		t.Errorf("bash_exec seeded shell = %q, want %q", got, DefaultShellKindBash)
	}
	if got := cfg.ShellExec.PoshExec.Shell; got != DefaultShellKindPosh {
		t.Errorf("posh_exec seeded shell = %q, want %q", got, DefaultShellKindPosh)
	}
}

func TestShellExec_InvalidKindFailsSoft(t *testing.T) {
	cfg, warnings := loadShellExecYAML(t, minimalConfigYAML(`
shell_exec:
  bash_exec:
    command: ["/usr/bin/fish", "-c", "{command}"]
    shell: fish
`))
	if len(warnings) != 1 {
		t.Fatalf("expected exactly one load warning, got %v", warnings)
	}
	if !strings.Contains(warnings[0], "bash_exec") || !strings.Contains(warnings[0], "fish") {
		t.Errorf("warning must name the tool and the reason, got %q", warnings[0])
	}
	if cfg.ShellExec.BashExec.OverrideActive() {
		t.Error("invalid override must be reset to the built-in default")
	}
	if cfg.ShellExec.BashExec.Shell != "" {
		t.Errorf("reset section must be zero, got shell %q", cfg.ShellExec.BashExec.Shell)
	}
}

func TestShellExec_InvalidTemplateFailsSoft(t *testing.T) {
	cases := map[string]string{
		"no placeholder":   `command: ["/bin/zsh", "-c"]`,
		"two placeholders": `command: ["/bin/zsh", "{command}", "{command}"]`,
		"embedded":         `command: ["/bin/zsh", "-c{command}"]`,
		"empty binary":     `command: ["", "{command}"]`,
		"binary is ph":     `command: ["{command}", "-c", "{command}"]`,
	}
	for name, shellExec := range cases {
		cfg, warnings := loadShellExecYAML(t, minimalConfigYAML("shell_exec:\n  bash_exec:\n    "+shellExec+"\n    shell: zsh"))
		if len(warnings) != 1 {
			t.Errorf("%s: expected exactly one warning, got %v", name, warnings)
			continue
		}
		if cfg.ShellExec.BashExec.OverrideActive() {
			t.Errorf("%s: invalid template must reset the override", name)
		}
	}
}

func TestShellExec_InactiveSectionAcceptsAnything(t *testing.T) {
	// A section with no command is inert: a stray (even invalid) shell value
	// with no override must not warn — there is nothing to apply it to.
	cfg, warnings := loadShellExecYAML(t, minimalConfigYAML(`
shell_exec:
  bash_exec:
    shell: fish
`))
	if len(warnings) != 0 {
		t.Fatalf("inactive section must not warn, got %v", warnings)
	}
	if cfg.ShellExec.BashExec.OverrideActive() {
		t.Error("section must stay inactive")
	}
}

func TestValidateShellExecCommand(t *testing.T) {
	valid := [][]string{
		{"bash", "-c", ShellCommandPlaceholder},
		{"/opt/homebrew/bin/zsh", "-l", "-c", ShellCommandPlaceholder},
		{"pwsh.exe", "-NoProfile", "-Command", ShellCommandPlaceholder},
		{"bash", ShellCommandPlaceholder},
	}
	for _, command := range valid {
		if err := ValidateShellExecCommand(command); err != nil {
			t.Errorf("ValidateShellExecCommand(%v) = %v, want nil", command, err)
		}
	}
	invalid := [][]string{
		{""},
		{ShellCommandPlaceholder},
		{"bash"},
		{"bash", "-c"},
		{"bash", "-c", ShellCommandPlaceholder, ShellCommandPlaceholder},
		{"bash", "-c" + ShellCommandPlaceholder},
	}
	for _, command := range invalid {
		if err := ValidateShellExecCommand(command); err == nil {
			t.Errorf("ValidateShellExecCommand(%v) = nil, want error", command)
		}
	}
	// Empty input = no override, always valid.
	if err := ValidateShellExecCommand(nil); err != nil {
		t.Errorf("nil command must be valid, got %v", err)
	}
}

func TestValidateShellKind(t *testing.T) {
	for _, kind := range []string{"bash", "sh", "zsh", "ksh", "dash", "powershell", "pwsh"} {
		if err := ValidateShellKind(kind); err != nil {
			t.Errorf("ValidateShellKind(%q) = %v, want nil", kind, err)
		}
	}
	for _, kind := range []string{"fish", "nushell", "cmd", "", "BASH"} {
		if err := ValidateShellKind(kind); err == nil {
			t.Errorf("ValidateShellKind(%q) = nil, want error", kind)
		}
	}
}
