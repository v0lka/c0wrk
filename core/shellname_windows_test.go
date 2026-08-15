//go:build windows

package core

import "testing"

// TestActiveShellToolName_Windows verifies that the platform-aware name
// helper resolves to ToolPoshExec on Windows, matching the shell tool
// actually registered on this platform. The Unix counterpart lives in
// shellname_unix_test.go.
func TestActiveShellToolName_Windows(t *testing.T) {
	if got := activeShellToolName(); got != ToolPoshExec {
		t.Errorf("activeShellToolName() = %q, want %q", got, ToolPoshExec)
	}
}
