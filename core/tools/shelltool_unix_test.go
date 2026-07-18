//go:build !windows

package tools

import (
	"testing"

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
