// SPDX-License-Identifier: Apache-2.0
//
// shell_exec.go — the `shell_exec` config section: the operator override of
// HOW the shell-execution tool (bash_exec on Unix, posh_exec on Windows)
// launches the agent's command, plus the mandatory declaration of which shell
// the command text is written in. Design contract (optional override, argv
// template, closed shell enum, fail-soft load, platform-default seeding):
// specs/decisions/058-shell-execution-override.md.

package config

import (
	"errors"
	"fmt"
	"strings"
)

// ShellCommandPlaceholder marks the argv element replaced by the agent's
// command text. Must match sp4rk's tools.ShellCommandPlaceholder — c0wrk
// spells it locally because backend/config never imports the engine.
const ShellCommandPlaceholder = "{command}"

// Supported shell kinds — the closed enum the deterministic flowsh command
// analysis understands (specs/decisions/058-shell-execution-override.md).
const (
	ShellKindBash       = "bash"       // bash syntax family
	ShellKindSh         = "sh"         // bash syntax family
	ShellKindZsh        = "zsh"        // bash syntax family
	ShellKindKsh        = "ksh"        // bash syntax family
	ShellKindDash       = "dash"       // bash syntax family
	ShellKindPowerShell = "powershell" // PowerShell syntax family
	ShellKindPwsh       = "pwsh"       // PowerShell syntax family
)

// Platform-default shell kinds — the syntax family each tool's command text
// is written in when the operator overrides the launch shape without
// declaring a shell.
const (
	DefaultShellKindBash = ShellKindBash
	DefaultShellKindPosh = ShellKindPowerShell
)

// ShellExecConfig is the top-level `shell_exec:` section, keyed by tool name:
// `bash_exec` is consumed on Unix, `posh_exec` on Windows — exactly one is
// live per platform (the build-tag split mirrors the tool registration).
type ShellExecConfig struct {
	BashExec ShellExecToolConfig `yaml:"bash_exec"`
	PoshExec ShellExecToolConfig `yaml:"posh_exec"`
}

// ShellExecToolConfig overrides one shell-exec tool's launch shape.
// The zero value keeps the built-in launch shape.
type ShellExecToolConfig struct {
	// Command is the argv template for launching the agent's command, e.g.
	// ["/opt/homebrew/bin/zsh", "-c", "{command}"]. Empty = built-in default.
	Command []string `yaml:"command"`
	// Shell declares which shell the command text the agent writes is
	// compatible with. Required when Command is set (seeded with the
	// platform default when omitted); validated against the closed enum.
	Shell string `yaml:"shell"`
}

// OverrideActive reports whether the section carries an override (a non-empty
// command template). A normalized config guarantees that an active override
// also carries a valid declared shell.
func (c ShellExecToolConfig) OverrideActive() bool {
	return len(c.Command) > 0
}

// ValidateShellExecCommand checks the argv template shape: a non-empty binary
// (never the placeholder itself) and exactly one standalone placeholder
// element; any other element containing the placeholder as a substring is
// rejected — a partial splice would reintroduce the quoting layer the
// element model exists to avoid.
func ValidateShellExecCommand(command []string) error {
	if len(command) == 0 {
		return nil // no override — nothing to validate
	}
	binary := command[0]
	if strings.TrimSpace(binary) == "" {
		return errors.New("shell_exec: command[0] (binary) is required")
	}
	if binary == ShellCommandPlaceholder {
		return fmt.Errorf("shell_exec: command[0] (binary) must not be the %s placeholder", ShellCommandPlaceholder)
	}
	if len(command) == 1 {
		return fmt.Errorf("shell_exec: command must contain the %s placeholder argument", ShellCommandPlaceholder)
	}
	placeholders := 0
	for _, arg := range command[1:] {
		if arg == ShellCommandPlaceholder {
			placeholders++
			continue
		}
		if strings.Contains(arg, ShellCommandPlaceholder) {
			return fmt.Errorf("shell_exec: %s must be a standalone command element, not embedded in %q", ShellCommandPlaceholder, arg)
		}
	}
	if placeholders != 1 {
		return fmt.Errorf("shell_exec: command must contain exactly one %s element, got %d", ShellCommandPlaceholder, placeholders)
	}
	return nil
}

// ValidateShellKind resolves a declared shell kind against the closed enum.
func ValidateShellKind(kind string) error {
	switch kind {
	case ShellKindBash, ShellKindSh, ShellKindZsh, ShellKindKsh, ShellKindDash,
		ShellKindPowerShell, ShellKindPwsh:
		return nil
	default:
		return fmt.Errorf(
			"shell_exec: unsupported shell kind %q (want one of: bash, sh, zsh, ksh, dash, powershell, pwsh) — "+
				"shells the deterministic command analysis cannot parse (fish, nushell, cmd.exe, …) are not supported",
			kind,
		)
	}
}

// normalizeShellExec validates the shell_exec section in place, fail-soft:
// an invalid entry is warned about and reset to the zero value (built-in
// launch shape) — never fatal, never half-applied. Must run AFTER
// ApplyDefaults (which seeds the platform-default kind for an active
// override) and BEFORE validate.
func normalizeShellExec(cfg *Config) []string {
	var warnings []string
	reset := func(name string, tool *ShellExecToolConfig, err error) {
		warnings = append(warnings, fmt.Sprintf(
			"shell_exec.%s ignored (falling back to the built-in launch shape): %v", name, err))
		*tool = ShellExecToolConfig{}
	}

	// Iterate a fixed-order slice (not a map literal): the warning order is
	// surfaced verbatim in the UI, so it must be deterministic when both
	// sections are invalid.
	for _, entry := range []struct {
		name string
		tool *ShellExecToolConfig
	}{
		{"bash_exec", &cfg.ShellExec.BashExec},
		{"posh_exec", &cfg.ShellExec.PoshExec},
	} {
		tool := entry.tool
		if !tool.OverrideActive() {
			continue
		}
		if err := ValidateShellExecCommand(tool.Command); err != nil {
			reset(entry.name, tool, err)
			continue
		}
		if err := ValidateShellKind(tool.Shell); err != nil {
			reset(entry.name, tool, err)
		}
	}
	return warnings
}
