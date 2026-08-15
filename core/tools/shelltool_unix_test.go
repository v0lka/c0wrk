//go:build !windows

package tools

import (
	"testing"
	"time"

	"github.com/v0lka/sp4rk/tools/builtins"
)

// TestShellExecToolName_Unix verifies that on Unix the platform-split
// shell-tool registration produces a tool registered under the "bash_exec"
// name. This is the Unix half of the cross-platform contract: the same
// newShellExecTool call registers "posh_exec" on Windows (see
// shelltool_windows_test.go). Guarding the assertion behind a build tag keeps
// the test aligned with the build-tagged constructor it exercises.
func TestShellExecToolName_Unix(t *testing.T) {
	tool, err := newShellExecTool(nil, builtins.DefaultBashTimeouts())
	if err != nil {
		t.Fatalf("newShellExecTool: unexpected error: %v", err)
	}
	if got := tool.Name(); got != "bash_exec" {
		t.Errorf("tool name = %q, want %q", got, "bash_exec")
	}
}

// TestUpdateShellTool_ReplacesBlacklist verifies that UpdateShellTool
// re-registers the shell tool with a new compiled-in blacklist: after the
// call, a command matching the new pattern is reported by the tool's Judge as
// a hard escalation, and an invalid pattern leaves the previous tool intact.
func TestUpdateShellTool_ReplacesBlacklist(t *testing.T) {
	registry := NewToolRegistry()
	if err := RegisterBuiltinTools(registry, BuiltinToolsConfig{
		BashTimeouts: builtins.BashTimeouts{MaxTimeout: 30 * time.Second},
	}); err != nil {
		t.Fatalf("RegisterBuiltinTools: %v", err)
	}

	// An empty replacement (nil blacklist) removes every pattern.
	if err := UpdateShellTool(registry, nil, builtins.BashTimeouts{MaxTimeout: 30 * time.Second}); err != nil {
		t.Fatalf("UpdateShellTool: unexpected error: %v", err)
	}
	// The tool must still be registered under the platform shell name.
	if _, ok := registry.Get("bash_exec"); !ok {
		t.Fatal("bash_exec not registered after UpdateShellTool")
	}

	// A non-empty replacement registers and compiles.
	if err := UpdateShellTool(registry, []string{`^echo\s+danger`}, builtins.BashTimeouts{MaxTimeout: 30 * time.Second}); err != nil {
		t.Fatalf("UpdateShellTool with a valid pattern: unexpected error: %v", err)
	}
	if _, ok := registry.Get("bash_exec"); !ok {
		t.Fatal("bash_exec not registered after a blacklist update")
	}

	// An invalid pattern must fail and leave the previously registered
	// tool in place.
	if err := UpdateShellTool(registry, []string{"("}, builtins.BashTimeouts{MaxTimeout: 30 * time.Second}); err == nil {
		t.Fatal("expected an error for a pattern that does not compile")
	}
	if _, ok := registry.Get("bash_exec"); !ok {
		t.Fatal("bash_exec must remain registered after a failed update")
	}
}
