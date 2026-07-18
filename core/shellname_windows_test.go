//go:build windows

package core

import "testing"

// TestActiveShellToolName_Windows verifies that the platform-aware policy
// lookup helper resolves to ToolPoshExec on Windows, so the security
// blacklist/confirm policy read from cfg.Security.ToolPolicies targets the tool
// name actually registered on this platform. The Unix counterpart lives in
// shellname_unix_test.go.
func TestActiveShellToolName_Windows(t *testing.T) {
	if got := activeShellToolName(); got != ToolPoshExec {
		t.Errorf("activeShellToolName() = %q, want %q", got, ToolPoshExec)
	}
}
