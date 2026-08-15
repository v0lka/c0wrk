//go:build !windows

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	sdktools "github.com/v0lka/sp4rk/tools"
	"github.com/v0lka/sp4rk/tools/builtins"
)

// TestBashExec_AllowGroup_OutOfRootPath_EscalatesToConfirm is the end-to-end
// proof that the path-containment check added to BashExecTool.Judge closes the
// documented-invariant gap: an execute-group tool set to allow whose command
// references a path OUTSIDE the session roots must escalate to confirmation
// (not execute directly). This uses the REAL BashExecTool (not a mock) so the
// real path-containment analysis in Judge is exercised. The command is never
// executed because confirmation fires first.
//
// BashExecTool is //go:build !windows; on Windows the posh_exec tool covers
// the same containment contract (see step_5 surrogate test in sp4rk
// shellpaths_test.go and posh_test.go).
func TestBashExec_AllowGroup_OutOfRootPath_EscalatesToConfirm(t *testing.T) {
	registry := NewToolRegistry()
	// Real bash_exec tool with an empty blacklist so ONLY path-containment
	// triggers the Judge reason (isolating the new behavior).
	tool, err := builtins.NewBashExecTool(nil)
	if err != nil {
		t.Fatalf("NewBashExecTool: %v", err)
	}
	registry.Register(tool)
	// Widen the execute group to allow — the posture under which the
	// invariant gap existed. The tool's own user_confirm default is ignored:
	// only the group policy counts.
	registry.SetGroupPolicies(map[sdktools.ToolGroup]sdktools.ToolPolicy{
		sdktools.GroupExecute: sdktools.PolicyAlwaysAllow,
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
		t.Fatal("expected confirmFunc to be called: out-of-root command must escalate to confirmation under an allow group")
	}
	if !strings.Contains(confirmReason, "session roots") {
		t.Errorf("expected confirm reason to mention 'session roots', got: %q", confirmReason)
	}
	// Denied confirmation returns an error result (the command did not run).
	if !result.IsError {
		t.Error("expected IsError result from denied confirmation (command must not execute)")
	}
}

// TestBashExec_AllowGroup_InRootPath_AutoApproved proves the
// path-containment check does NOT regress allow-group execution: when all
// paths are inside the session roots and the command is otherwise benign
// (empty blacklist, no containment reason), the call executes directly.
// Uses a harmless command (echo) so the real bash Execute is safe to invoke.
func TestBashExec_AllowGroup_InRootPath_AutoApproved(t *testing.T) {
	registry := NewToolRegistry()
	tool, err := builtins.NewBashExecTool(nil)
	if err != nil {
		t.Fatalf("NewBashExecTool: %v", err)
	}
	registry.Register(tool)
	registry.SetGroupPolicies(map[sdktools.ToolGroup]sdktools.ToolPolicy{
		sdktools.GroupExecute: sdktools.PolicyAlwaysAllow,
	})

	confirmCalled := false
	registry.SetConfirmFunc(func(ctx context.Context, req sdktools.ConfirmationRequest) (sdktools.ConfirmationResponse, error) {
		confirmCalled = true
		return sdktools.ConfirmAllowOnce, nil
	})

	ws := t.TempDir()
	ctx := sdktools.WithWorkspacePath(context.Background(), ws)
	// A no-argument command referencing no out-of-root paths: the Judge
	// reports no concern, so the allow group executes directly.
	input := json.RawMessage(`{"command": "echo hello"}`)

	result, err := registry.Execute(ctx, "bash_exec", input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if confirmCalled {
		t.Error("expected confirmFunc NOT to be called for a benign in-root command under an allow group")
	}
	if result.IsError {
		t.Errorf("expected successful execution, got error: %s", result.Content)
	}
}

// TestExtraShellBlacklist_HardBlockNamesPattern verifies the No Project extra
// shell blacklist is an unconditional hard block — even when the execute
// group is widened to allow and Smart Approve is on — and that the block
// reason names the matched pattern so the user can see which rule fired.
func TestExtraShellBlacklist_HardBlockNamesPattern(t *testing.T) {
	registry := NewToolRegistry()
	tool, err := builtins.NewBashExecTool(nil)
	if err != nil {
		t.Fatalf("NewBashExecTool: %v", err)
	}
	registry.Register(tool)

	const pattern = `^go\s+build\b`
	if err := registry.SetExtraShellBlacklist([]string{pattern}); err != nil {
		t.Fatalf("SetExtraShellBlacklist: %v", err)
	}
	// Policy leniency must not matter: the blacklist is a hard gate.
	registry.SetGroupPolicies(map[sdktools.ToolGroup]sdktools.ToolPolicy{
		sdktools.GroupExecute: sdktools.PolicyAlwaysAllow,
	})
	registry.SetSmartApprove(true)

	confirmCalled := false
	registry.SetConfirmFunc(func(context.Context, sdktools.ConfirmationRequest) (sdktools.ConfirmationResponse, error) {
		confirmCalled = true
		return sdktools.ConfirmAllowOnce, nil
	})

	ctx := sdktools.WithWorkspacePath(context.Background(), t.TempDir())
	result, err := registry.Execute(ctx, "bash_exec", json.RawMessage(`{"command":"go build ./..."}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected the blacklisted command to be hard-blocked")
	}
	if confirmCalled {
		t.Error("the extra blacklist blocks outright — confirmation must not be offered")
	}
	if !strings.Contains(result.Content, fmt.Sprintf("%q", pattern)) {
		t.Errorf("expected the block reason to contain the matched pattern %q (quoted), got %q", pattern, result.Content)
	}
}
