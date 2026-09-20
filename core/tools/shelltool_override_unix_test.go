//go:build !windows

// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	sdktools "github.com/v0lka/sp4rk/tools"
	"github.com/v0lka/sp4rk/tools/builtins"
)

// TestNewShellExecTool_InvocationOverride verifies that an operator-configured
// launch-shape override reaches the registered tool: the description names the
// actual invocation (the prompt channel for every agent), while the default
// posture keeps the built-in description byte-for-byte.
func TestNewShellExecTool_InvocationOverride(t *testing.T) {
	inv := sdktools.ShellInvocation{
		Binary: "/bin/sh",
		Args:   []string{"-c", sdktools.ShellCommandPlaceholder},
		Kind:   sdktools.ShellKindSh,
	}
	tool, err := newShellExecTool(nil, builtins.DefaultBashTimeouts(), &inv, nil)
	if err != nil {
		t.Fatalf("newShellExecTool with override: %v", err)
	}
	if !strings.Contains(tool.Description(), "/bin/sh -c <command>") {
		t.Errorf("override description must name the actual invocation, got: %s", tool.Description())
	}

	def, err := newShellExecTool(nil, builtins.DefaultBashTimeouts(), nil, nil)
	if err != nil {
		t.Fatalf("newShellExecTool default: %v", err)
	}
	sameAsDefaultInv := sdktools.DefaultBashInvocation()
	sameAsDefault, err := newShellExecTool(nil, builtins.DefaultBashTimeouts(), &sameAsDefaultInv, nil)
	if err != nil {
		t.Fatalf("newShellExecTool default invocation: %v", err)
	}
	if def.Description() != sameAsDefault.Description() {
		t.Error("an explicit default invocation must keep the built-in description byte-for-byte")
	}
	if def.Description() == tool.Description() {
		t.Error("the override must change the tool description")
	}
}

// TestUpdateShellTool_OverrideReRegistration pins the runtime-update path: a
// settings-save re-registration swaps the live description to the overridden
// one without touching the blocklist posture.
func TestUpdateShellTool_OverrideReRegistration(t *testing.T) {
	registry := NewToolRegistry()
	if err := UpdateShellTool(registry, nil, builtins.DefaultBashTimeouts(), nil, nil); err != nil {
		t.Fatalf("UpdateShellTool: %v", err)
	}
	before, ok := registry.Get(ShellExecToolName())
	if !ok {
		t.Fatal("shell tool not registered")
	}

	inv := sdktools.ShellInvocation{
		Binary: "/opt/homebrew/bin/zsh",
		Args:   []string{"-c", sdktools.ShellCommandPlaceholder},
		Kind:   sdktools.ShellKindZsh,
	}
	if err := UpdateShellTool(registry, nil, builtins.DefaultBashTimeouts(), &inv, nil); err != nil {
		t.Fatalf("UpdateShellTool with override: %v", err)
	}
	after, ok := registry.Get(ShellExecToolName())
	if !ok {
		t.Fatal("shell tool not registered after re-registration")
	}
	if before.Description() == after.Description() {
		t.Error("re-registration must swap the description to the overridden one")
	}
	if !strings.Contains(after.Description(), "zsh-compatible") {
		t.Errorf("overridden description must declare the shell compatibility, got: %s", after.Description())
	}
}

// TestAttachShellAnalysisForTool_OverrideDialect verifies the analysis dialect
// follows the DECLARED shell kind of the registered tool, not the tool name:
// a bash_exec carrying a pwsh-declared override analyzes PowerShell syntax
// with the PowerShell dialect.
func TestAttachShellAnalysisForTool_OverrideDialect(t *testing.T) {
	inv := sdktools.ShellInvocation{
		Binary: "pwsh",
		Args:   []string{"-Command", sdktools.ShellCommandPlaceholder},
		Kind:   sdktools.ShellKindPwsh,
	}
	tool, err := builtins.NewBashExecToolWithInvocation(nil, builtins.DefaultBashTimeouts(), inv)
	if err != nil {
		t.Fatalf("constructor: %v", err)
	}

	ctx := AttachShellAnalysisForTool(context.Background(), tool, "bash_exec", json.RawMessage(`{"command":"Get-Date"}`), nil)
	analysis, err := sdktools.ShellAnalysisFrom(ctx)
	if err != nil || analysis == nil {
		t.Fatalf("analysis not attached: %v", err)
	}
	if analysis.Digest.Lang != "posh" {
		t.Errorf("digest lang = %q, want posh (declared kind must override the tool-name mapping)", analysis.Digest.Lang)
	}

	// The legacy helper (no tool instance) keeps the tool-name mapping.
	ctx = AttachShellAnalysis(context.Background(), "bash_exec", json.RawMessage(`{"command":"Get-Date"}`), nil)
	analysis, err = sdktools.ShellAnalysisFrom(ctx)
	if err != nil || analysis == nil {
		t.Fatalf("legacy analysis not attached: %v", err)
	}
	if analysis.Digest.Lang != "bash" {
		t.Errorf("legacy digest lang = %q, want bash", analysis.Digest.Lang)
	}
}
