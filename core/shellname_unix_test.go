//go:build !windows

package core

import "testing"

// TestActiveShellToolName_Unix verifies that the platform-aware name helper
// resolves to ToolBashExec on Unix, matching the shell tool actually
// registered on this platform. The Windows counterpart lives in
// shellname_windows_test.go.
func TestActiveShellToolName_Unix(t *testing.T) {
	if got := activeShellToolName(); got != ToolBashExec {
		t.Errorf("activeShellToolName() = %q, want %q", got, ToolBashExec)
	}
}
