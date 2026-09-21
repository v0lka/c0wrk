// SPDX-License-Identifier: Apache-2.0

package backend

import (
	"errors"
	"slices"
	"testing"

	"github.com/v0lka/c0wrk/backend/config"
)

// TestGetShellExecSettings_DefaultsAndRoundTrip verifies the get side: an
// uninitialized config renders the built-in defaults (no override), and a
// stored section round-trips with deep-copied command slices.
func TestGetShellExecSettings_DefaultsAndRoundTrip(t *testing.T) {
	f, _, _ := newTestAPI(t)

	defaults := f.GetShellExecSettings()
	if len(defaults.BashExec.Command) != 0 || defaults.BashExec.Shell != "" {
		t.Errorf("default settings must carry no override, got %+v", defaults.BashExec)
	}

	f.config.ShellExec.BashExec = config.ShellExecToolConfig{
		Command: []string{"/opt/homebrew/bin/zsh", "-c", config.ShellCommandPlaceholder},
		Shell:   config.ShellKindZsh,
	}
	got := f.GetShellExecSettings()
	if !slices.Equal(got.BashExec.Command, []string{"/opt/homebrew/bin/zsh", "-c", config.ShellCommandPlaceholder}) {
		t.Errorf("command not returned: %v", got.BashExec.Command)
	}
	if got.BashExec.Shell != config.ShellKindZsh {
		t.Errorf("shell = %q, want zsh", got.BashExec.Shell)
	}

	// Mutating the returned slice must not touch the live config.
	got.BashExec.Command[0] = "/tampered"
	if f.config.ShellExec.BashExec.Command[0] != "/opt/homebrew/bin/zsh" {
		t.Error("the response must deep-copy the command slice")
	}
}

// TestUpdateShellExecSettings_AppliesAndPersists verifies the happy path: a
// changed payload updates the stored section, re-registers the shell tool via
// the builder exactly once, and persists.
func TestUpdateShellExecSettings_AppliesAndPersists(t *testing.T) {
	f, mock, cfgPath := newTestAPI(t)

	payload := ShellExecSettingsResponse{
		BashExec: ShellExecToolSettings{
			Command: []string{"/opt/homebrew/bin/zsh", "-c", config.ShellCommandPlaceholder},
			Shell:   "zsh",
		},
	}
	if err := f.UpdateShellExecSettings(payload); err != nil {
		t.Fatalf("UpdateShellExecSettings: %v", err)
	}
	if mock.updateShellBlocklistCalls != 1 {
		t.Errorf("UpdateShellBlocklist called %d times, want 1", mock.updateShellBlocklistCalls)
	}
	if !f.config.ShellExec.BashExec.OverrideActive() || f.config.ShellExec.BashExec.Shell != "zsh" {
		t.Errorf("config not updated: %+v", f.config.ShellExec.BashExec)
	}
	if !persistedConfigHasShellExec(t, cfgPath) {
		t.Error("the override was not persisted")
	}
}

// TestUpdateShellExecSettings_NoChangeSkipsReRegistration mirrors the
// blocklist save path: a payload equal to the stored section must not
// re-register anything.
func TestUpdateShellExecSettings_NoChangeSkipsReRegistration(t *testing.T) {
	f, mock, _ := newTestAPI(t)
	f.config.ShellExec.BashExec = config.ShellExecToolConfig{
		Command: []string{"/bin/zsh", "-c", config.ShellCommandPlaceholder},
		Shell:   "zsh",
	}
	payload := ShellExecSettingsResponse{
		BashExec: ShellExecToolSettings{
			Command: []string{"/bin/zsh", "-c", config.ShellCommandPlaceholder},
			Shell:   "zsh",
		},
	}
	if err := f.UpdateShellExecSettings(payload); err != nil {
		t.Fatalf("UpdateShellExecSettings: %v", err)
	}
	if mock.updateShellBlocklistCalls != 0 {
		t.Errorf("UpdateShellBlocklist called %d times, want 0 for an unchanged override", mock.updateShellBlocklistCalls)
	}
}

// TestUpdateShellExecSettings_InvalidPayloadRejected verifies validation: an
// unsupported shell kind or a mis-shaped argv template is rejected and mutates
// nothing.
func TestUpdateShellExecSettings_InvalidPayloadRejected(t *testing.T) {
	f, mock, _ := newTestAPI(t)
	before := f.config.ShellExec

	cases := map[string]ShellExecSettingsResponse{
		"unsupported shell": {
			BashExec: ShellExecToolSettings{
				Command: []string{"/usr/bin/fish", "-c", config.ShellCommandPlaceholder},
				Shell:   "fish",
			},
		},
		"missing placeholder": {
			BashExec: ShellExecToolSettings{
				Command: []string{"/bin/zsh", "-c"},
				Shell:   "zsh",
			},
		},
		"two placeholders": {
			BashExec: ShellExecToolSettings{
				Command: []string{"/bin/zsh", config.ShellCommandPlaceholder, config.ShellCommandPlaceholder},
				Shell:   "zsh",
			},
		},
		"posh invalid": {
			PoshExec: ShellExecToolSettings{
				Command: []string{"pwsh.exe"},
				Shell:   "pwsh",
			},
		},
	}
	for name, payload := range cases {
		if err := f.UpdateShellExecSettings(payload); err == nil {
			t.Errorf("%s: expected error, got nil", name)
		}
	}
	if mock.updateShellBlocklistCalls != 0 {
		t.Errorf("UpdateShellBlocklist must not run for invalid payloads, ran %d times", mock.updateShellBlocklistCalls)
	}
	if !shellExecEqual(f.config.ShellExec, before) {
		t.Error("config mutated by a rejected payload")
	}
}

// TestUpdateShellExecSettings_ReRegistrationFailureRollsBack mirrors the
// blocklist failure atomicity: a failed shell-tool re-registration restores
// the previous section so the live registry and the stored config never
// diverge.
func TestUpdateShellExecSettings_ReRegistrationFailureRollsBack(t *testing.T) {
	f, mock, _ := newTestAPI(t)
	prev := f.config.ShellExec
	mock.updateShellBlocklistErr = errors.New("shell re-registration failure")

	payload := ShellExecSettingsResponse{
		BashExec: ShellExecToolSettings{
			Command: []string{"/bin/zsh", "-c", config.ShellCommandPlaceholder},
			Shell:   "zsh",
		},
	}
	if err := f.UpdateShellExecSettings(payload); err == nil {
		t.Fatal("expected error when the shell re-registration fails")
	}
	if !shellExecEqual(f.config.ShellExec, prev) {
		t.Error("the override section was not rolled back")
	}
}

// TestUpdateShellExecSettings_NoConfig verifies the uninitialized guard.
func TestUpdateShellExecSettings_NoConfig(t *testing.T) {
	f, _, _ := newTestAPI(t)
	f.config = nil
	if err := f.UpdateShellExecSettings(ShellExecSettingsResponse{}); err == nil {
		t.Fatal("expected error for an uninitialized config")
	}
}

func shellExecEqual(a, b config.ShellExecConfig) bool {
	return slices.Equal(a.BashExec.Command, b.BashExec.Command) && a.BashExec.Shell == b.BashExec.Shell &&
		slices.Equal(a.PoshExec.Command, b.PoshExec.Command) && a.PoshExec.Shell == b.PoshExec.Shell
}

// persistedConfigHasShellExec reloads the persisted config and reports whether
// the bash_exec override survived the write.
func persistedConfigHasShellExec(t *testing.T, path string) bool {
	t.Helper()
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("reload persisted config: %v", err)
	}
	return cfg.ShellExec.BashExec.OverrideActive()
}
