//go:build windows

package core

import "testing"

// activeShellToolName returns the tool name under which the shell-execution
// tool is registered on this platform (posh_exec on Windows). It is
// referenced by tests only: production code resolves the shell tool via its
// `execute` capability group, never by name.
func activeShellToolName() string {
	return ToolPoshExec
}

// TestActiveShellToolName_Windows verifies that the platform-aware name
// helper resolves to ToolPoshExec on Windows, matching the shell tool
// actually registered on this platform. The Unix counterpart lives in
// shellname_unix_test.go.
func TestActiveShellToolName_Windows(t *testing.T) {
	if got := activeShellToolName(); got != ToolPoshExec {
		t.Errorf("activeShellToolName() = %q, want %q", got, ToolPoshExec)
	}
}
