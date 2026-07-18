//go:build windows

package tools

import (
	"testing"

	"github.com/v0lka/sp4rk/tools/builtins"
)

// TestShellExecToolName_Windows verifies that on Windows the platform-split
// shell-tool registration produces a tool registered under the "posh_exec"
// name. This is the Windows half of the cross-platform contract: the same
// newShellExecTool call registers "bash_exec" on Unix (see
// shelltool_unix_test.go). Guarding the assertion behind a build tag keeps
// the test aligned with the build-tagged constructor it exercises.
func TestShellExecToolName_Windows(t *testing.T) {
	tool, err := newShellExecTool(nil, builtins.DefaultBashTimeouts())
	if err != nil {
		t.Fatalf("newShellExecTool: unexpected error: %v", err)
	}
	if got := tool.Name(); got != "posh_exec" {
		t.Errorf("tool name = %q, want %q", got, "posh_exec")
	}
}
