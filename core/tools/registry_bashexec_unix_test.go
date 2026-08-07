//go:build !windows

package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	sdktools "github.com/v0lka/sp4rk/tools"
	"github.com/v0lka/sp4rk/tools/builtins"
)

// TestBashExec_AlwaysAllow_OutOfRootPath_EscalatesToConfirm is the end-to-end
// proof that the path-containment check added to BashExecTool.Judge closes the
// documented-invariant gap in security-model.md: an always_allow shell tool
// referencing a path OUTSIDE the session roots must escalate to confirmation
// (not execute directly). This uses the REAL BashExecTool (not a mock) so the
// real path-containment analysis in Judge is exercised. The command is never
// executed because confirmation fires first.
//
// BashExecTool is //go:build !windows; on Windows the posh_exec tool covers
// the same containment contract (see step_5 surrogate test in sp4rk
// shellpaths_test.go and posh_test.go).
func TestBashExec_AlwaysAllow_OutOfRootPath_EscalatesToConfirm(t *testing.T) {
	registry := NewToolRegistry()
	// Real bash_exec tool with an empty blacklist so ONLY path-containment
	// triggers the Judge reason (isolating the new behavior).
	tool, err := builtins.NewBashExecTool(nil)
	if err != nil {
		t.Fatalf("NewBashExecTool: %v", err)
	}
	registry.Register(tool)
	// Force always_allow so the default user_confirm policy does not mask the
	// behavior — this is the policy under which the invariant gap existed.
	registry.SetPolicyOverrides(map[string]sdktools.ToolPolicy{
		"bash_exec": sdktools.PolicyAlwaysAllow,
	})

	confirmCalled := false
	var confirmReason string
	registry.SetConfirmFunc(func(ctx context.Context, req sdktools.ConfirmationRequest) (sdktools.ConfirmationResponse, error) {
		confirmCalled = true
		confirmReason = req.JudgeReasoning
		return sdktools.ConfirmDeny, nil // deny to avoid executing bash
	})

	ws := t.TempDir()
	ctx := sdktools.WithWorkspacePath(context.Background(), ws)
	input := json.RawMessage(`{"command": "cat /etc/passwd"}`)

	result, err := registry.Execute(ctx, "bash_exec", input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !confirmCalled {
		t.Fatal("expected confirmFunc to be called: out-of-root command must escalate to confirmation under always_allow")
	}
	if !strings.Contains(confirmReason, "session roots") {
		t.Errorf("expected confirm reason to mention 'session roots', got: %q", confirmReason)
	}
	// Denied confirmation returns an error result (the command did not run).
	if !result.IsError {
		t.Error("expected IsError result from denied confirmation (command must not execute)")
	}
}

// TestBashExec_AlwaysAllow_InRootPath_AutoApproved proves the
// path-containment check does NOT regress workspace auto-approval: when all
// paths are inside the session roots and the command is otherwise benign
// (empty blacklist, no containment reason), the call executes directly.
// Uses a harmless command (echo) so the real bash Execute is safe to invoke.
func TestBashExec_AlwaysAllow_InRootPath_AutoApproved(t *testing.T) {
	registry := NewToolRegistry()
	tool, err := builtins.NewBashExecTool(nil)
	if err != nil {
		t.Fatalf("NewBashExecTool: %v", err)
	}
	registry.Register(tool)
	registry.SetPolicyOverrides(map[string]sdktools.ToolPolicy{
		"bash_exec": sdktools.PolicyAlwaysAllow,
	})

	confirmCalled := false
	registry.SetConfirmFunc(func(ctx context.Context, req sdktools.ConfirmationRequest) (sdktools.ConfirmationResponse, error) {
		confirmCalled = true
		return sdktools.ConfirmAllowOnce, nil
	})

	ws := t.TempDir()
	ctx := sdktools.WithWorkspacePath(context.Background(), ws)
	// A no-argument command referencing no out-of-root paths: Judge returns
	// (false, "") → auto-approval applies (no paths → not AllPathsInSessionRoots
	// trigger, falls to direct execute under AlwaysAllow).
	input := json.RawMessage(`{"command": "echo hello"}`)

	result, err := registry.Execute(ctx, "bash_exec", input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if confirmCalled {
		t.Error("expected confirmFunc NOT to be called for a benign in-root command under always_allow")
	}
	if result.IsError {
		t.Errorf("expected successful execution, got error: %s", result.Content)
	}
}
