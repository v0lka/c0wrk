//go:build !windows

package core

import "testing"

// activeShellToolName returns the tool name under which the shell-execution
// tool is registered on this platform (bash_exec on Unix). It is referenced
// by tests only: production code resolves the shell tool via its `execute`
// capability group, never by name.
func activeShellToolName() string {
	return ToolBashExec
}

// TestActiveShellToolName_Unix verifies that the platform-aware name helper
// resolves to ToolBashExec on Unix, matching the shell tool actually
// registered on this platform. The Windows counterpart lives in
// shellname_windows_test.go.
func TestActiveShellToolName_Unix(t *testing.T) {
	if got := activeShellToolName(); got != ToolBashExec {
		t.Errorf("activeShellToolName() = %q, want %q", got, ToolBashExec)
	}
}
