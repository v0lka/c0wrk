package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	sdktools "github.com/v0lka/sp4rk/tools"
	"github.com/v0lka/sp4rk/tools/builtins"
)

// ── Registry integration tests ────────────────────────────────────────────

func newRegistryForSymlinkTest(t *testing.T) *ToolRegistry {
	t.Helper()
	r := NewToolRegistry()
	setDefaultGroupPolicies(r)
	_ = RegisterBuiltinTools(r, BuiltinToolsConfig{
		BashTimeouts: builtins.BashTimeouts{MaxTimeout: 120 * time.Second, WaitDelay: 5 * time.Second},
	})
	return r
}

// shellExecToolName returns the platform-registered shell-execution tool name.
// "bash_exec" on Unix, "posh_exec" on Windows — matches newShellExecTool in
// shelltool_{unix,windows}.go. Use this instead of a hardcoded "bash_exec"
// literal in tests that exercise the symlink/suspicion gate via the shell tool.
func shellExecToolName() string {
	if runtime.GOOS == "windows" {
		return "posh_exec"
	}
	return "bash_exec"
}

func TestSymlinkGate_SystemToolBypass(t *testing.T) {
	r := newRegistryForSymlinkTest(t)
	// finish is a system-group tool that bypasses policy and symlink checks.
	// This test verifies that the symlink gate is placed AFTER the
	// system-group bypass in Execute().
	ctx := context.Background()

	// Execute should succeed without going through symlink check or confirmation
	result, err := r.Execute(ctx, "finish", json.RawMessage(`{"answer":"test"}`))
	if err != nil {
		t.Fatalf("expected no error for system tool, got %v", err)
	}
	if result.IsError {
		t.Fatalf("expected successful execution for system tool, got error: %s", result.Content)
	}
}

func TestSymlinkGate_CleanPathNoIntercept(t *testing.T) {
	dir := t.TempDir()
	r := newRegistryForSymlinkTest(t)
	r.SetConfirmFunc(func(ctx context.Context, req sdktools.ConfirmationRequest) (sdktools.ConfirmationResponse, error) {
		t.Fatal("confirmFunc should NOT be called for clean path")
		return sdktools.ConfirmDeny, nil
	})

	normalPath := filepath.Join(dir, "normal.txt")
	_ = os.WriteFile(normalPath, []byte("x"), 0o644)
	input, _ := json.Marshal(map[string]string{"path": normalPath})
	ctx := sdktools.WithWorkspacePath(context.Background(), dir)

	result, err := r.Execute(ctx, "read_file", input)
	if err != nil {
		t.Fatalf("expected no error for clean path, got %v", err)
	}
	// read_file should succeed for existing file in workspace
	if result.IsError {
		t.Fatalf("expected success for clean path, got error: %s", result.Content)
	}
}

// TestSymlinkGate_InRootsResolutionAutoApproved proves the relaxed symlink
// contract: a symlink whose resolution stays INSIDE the session roots is not
// a concern — read_file (local_read, allow) executes without confirmation.
func TestSymlinkGate_InRootsResolutionAutoApproved(t *testing.T) {
	dir := t.TempDir()
	realDir := filepath.Join(dir, "real")
	_ = os.MkdirAll(realDir, 0o755)
	_ = os.WriteFile(filepath.Join(realDir, "file.txt"), []byte("data"), 0o644)
	symlinkPath := filepath.Join(dir, "link")
	_ = os.Symlink(realDir, symlinkPath)

	r := newRegistryForSymlinkTest(t)
	r.SetConfirmFunc(func(ctx context.Context, req sdktools.ConfirmationRequest) (sdktools.ConfirmationResponse, error) {
		t.Fatal("confirmFunc should NOT be called: an in-roots symlink resolution is not a concern")
		return sdktools.ConfirmDeny, nil
	})

	nestedPath := filepath.Join(symlinkPath, "file.txt")
	input, _ := json.Marshal(map[string]string{"path": nestedPath})
	ctx := sdktools.WithWorkspacePath(context.Background(), dir)

	result, err := r.Execute(ctx, "read_file", input)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if result.IsError {
		t.Fatalf("expected successful execution through an in-roots symlink, got error: %s", result.Content)
	}
}

// TestSymlinkGate_EscapeForcesHardConfirm proves the hard side of the
// contract: a symlink escaping the session roots forces a confirmation with
// the advisory judge disabled, and the reasoning explains the escape.
func TestSymlinkGate_EscapeForcesHardConfirm(t *testing.T) {
	ws := t.TempDir()
	outside := t.TempDir() // different root: outside the workspace
	_ = os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("data"), 0o644)
	symlinkPath := filepath.Join(ws, "escape")
	_ = os.Symlink(outside, symlinkPath)

	r := newRegistryForSymlinkTest(t)
	confirmed := make(chan sdktools.ConfirmationRequest, 1)
	r.SetConfirmFunc(func(ctx context.Context, req sdktools.ConfirmationRequest) (sdktools.ConfirmationResponse, error) {
		confirmed <- req
		return sdktools.ConfirmAllowOnce, nil
	})

	nestedPath := filepath.Join(symlinkPath, "secret.txt")
	input, _ := json.Marshal(map[string]string{"path": nestedPath})
	ctx := sdktools.WithWorkspacePath(context.Background(), ws)

	result, err := r.Execute(ctx, "read_file", input)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if result.IsError {
		t.Fatalf("expected successful execution after confirm, got error: %s", result.Content)
	}

	select {
	case req := <-confirmed:
		if req.JudgeReasoning == "" {
			t.Fatal("expected non-empty JudgeReasoning for a symlink escape")
		}
		if !strings.Contains(strings.ToLower(req.JudgeReasoning), "symlink") {
			t.Fatalf("expected reasoning to mention the symlink, got: %s", req.JudgeReasoning)
		}
		if !req.DisableJudge {
			t.Error("symlink-escape confirmation must disable the advisory judge")
		}
	default:
		t.Fatal("expected confirmFunc to be called for a symlink escaping the roots")
	}
}

func TestSymlinkGate_RespectsGroupDeny(t *testing.T) {
	dir := t.TempDir()
	realDir := filepath.Join(dir, "real")
	_ = os.MkdirAll(realDir, 0o755)
	symlinkPath := filepath.Join(dir, "link")
	_ = os.Symlink(realDir, symlinkPath)

	r := newRegistryForSymlinkTest(t)
	r.SetGroupPolicies(map[sdktools.ToolGroup]sdktools.ToolPolicy{
		sdktools.GroupLocalRead: sdktools.PolicyAlwaysDeny,
	})
	r.SetConfirmFunc(func(ctx context.Context, req sdktools.ConfirmationRequest) (sdktools.ConfirmationResponse, error) {
		t.Fatal("confirmFunc should NOT be called when the group policy is deny")
		return sdktools.ConfirmDeny, nil
	})

	nestedPath := filepath.Join(symlinkPath, "file.txt")
	input, _ := json.Marshal(map[string]string{"path": nestedPath})
	ctx := sdktools.WithWorkspacePath(context.Background(), dir)

	result, err := r.Execute(ctx, "read_file", input)
	if err != nil {
		t.Fatalf("expected no error (blocked gracefully), got %v", err)
	}
	if !result.IsError {
		t.Fatal("expected error result for a deny-group blocked tool")
	}
	if !strings.Contains(result.Content, "blocked by security policy") {
		t.Fatalf("expected blocked by security policy message, got: %s", result.Content)
	}
}

func TestSymlinkGate_DenyResponse(t *testing.T) {
	ws := t.TempDir()
	outside := t.TempDir()
	symlinkPath := filepath.Join(ws, "escape")
	_ = os.Symlink(outside, symlinkPath)

	r := newRegistryForSymlinkTest(t)
	r.SetConfirmFunc(func(ctx context.Context, req sdktools.ConfirmationRequest) (sdktools.ConfirmationResponse, error) {
		return sdktools.ConfirmDeny, nil
	})

	nestedPath := filepath.Join(symlinkPath, "file.txt")
	input, _ := json.Marshal(map[string]string{"path": nestedPath})
	ctx := sdktools.WithWorkspacePath(context.Background(), ws)

	result, err := r.Execute(ctx, "read_file", input)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if !result.IsError {
		t.Fatal("expected error result for denied symlink tool call")
	}
	if !strings.Contains(result.Content, "denied by user") {
		t.Fatalf("expected 'denied by user' message, got: %s", result.Content)
	}
}

// TestSymlinkGate_BashExecWithInRootsSymlink verifies that an in-roots
// symlink no longer attaches symlink reasoning to bash_exec: the command is
// user_confirm (execute group), so the confirmation that fires carries the
// regular per-tool reason, not a symlink escalation.
func TestSymlinkGate_BashExecWithInRootsSymlink(t *testing.T) {
	dir := t.TempDir()
	realDir := filepath.Join(dir, "real")
	_ = os.MkdirAll(realDir, 0o755)
	_ = os.WriteFile(filepath.Join(realDir, "file.txt"), []byte("data"), 0o644)
	symlinkPath := filepath.Join(dir, "link")
	_ = os.Symlink(realDir, symlinkPath)

	r := newRegistryForSymlinkTest(t)
	confirmed := make(chan string, 1)
	r.SetConfirmFunc(func(ctx context.Context, req sdktools.ConfirmationRequest) (sdktools.ConfirmationResponse, error) {
		confirmed <- req.JudgeReasoning
		return sdktools.ConfirmAllowOnce, nil
	})

	command := "cat " + filepath.Join(symlinkPath, "file.txt")
	input, _ := json.Marshal(map[string]string{"command": command, "working_directory": dir})
	ctx := sdktools.WithWorkspacePath(context.Background(), dir)

	result, err := r.Execute(ctx, shellExecToolName(), input)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if result.IsError {
		t.Fatalf("expected successful shell exec after confirm, got error: %s", result.Content)
	}

	select {
	case reasoning := <-confirmed:
		if strings.Contains(strings.ToLower(reasoning), "symlink") {
			t.Fatalf("in-roots symlink must not attach symlink reasoning, got: %s", reasoning)
		}
		if reasoning == "" {
			t.Fatal("expected the regular user_confirm reason to be attached")
		}
	default:
		t.Fatal("expected confirmFunc to be called (execute group is user_confirm)")
	}
}

func TestSymlinkGate_BashExecSuspiciousForceConfirm(t *testing.T) {
	r := newRegistryForSymlinkTest(t)
	confirmed := make(chan struct{}, 1)
	r.SetConfirmFunc(func(ctx context.Context, req sdktools.ConfirmationRequest) (sdktools.ConfirmationResponse, error) {
		confirmed <- struct{}{}
		return sdktools.ConfirmDeny, nil
	})

	input, _ := json.Marshal(map[string]string{"command": "cat $HOME/file"})
	ctx := context.Background()

	result, err := r.Execute(ctx, shellExecToolName(), input)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if !result.IsError {
		t.Fatal("expected error result for denied suspicious command")
	}

	select {
	case <-confirmed:
		// Confirmation was triggered — correct
	default:
		t.Fatal("expected confirmFunc to be called for suspicious bash command")
	}
}

func TestSymlinkGate_EmptyInput(t *testing.T) {
	r := newRegistryForSymlinkTest(t)
	// Don't set confirmFunc — we're only checking that the symlink gate
	// doesn't intercept. Using read_file (allow group) so that the normal
	// policy flow doesn't call confirmFunc either.
	// The input has no path field, so the symlink gate should not intercept.
	input, _ := json.Marshal(map[string]string{"timeout": "30s"})
	ctx := sdktools.WithWorkspacePath(context.Background(), "/workspace")
	result, err := r.Execute(ctx, "read_file", input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// read_file requires "path" — validation error expected, not a symlink block
	if result.IsError && strings.Contains(result.Content, "security policy") {
		t.Fatal("symlink gate should not have intercepted empty input")
	}
}

func TestSymlinkGate_TempDirOSLevelSymlink(t *testing.T) {
	// Create a temp dir under /tmp, which is an OS-level symlink to /private/tmp
	// on macOS. Verify that the symlink gate skips this OS-level infrastructure
	// rather than forcing a symlink-specific confirmation.
	//
	// Note: bash_exec resolves to user_confirm (execute group), so the registry
	// will still require user confirmation for the call — but the confirmation
	// must come from the regular policy path with no symlink reasoning
	// attached, NOT the symlink gate. The test asserts that distinction by
	// failing when JudgeReasoning mentions "symlink".
	tmpDir, err := os.MkdirTemp("/tmp", "c0wrk-symlink-test-*")
	if err != nil {
		t.Skipf("cannot create temp dir under /tmp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tmpDir) })

	testFile := filepath.Join(tmpDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("data"), 0o644); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}

	// Workspace is a non-overlapping directory (so any /tmp → /private/tmp
	// symlink traversal lands "outside" workspace, testing the OS-level gate).
	ws := t.TempDir()

	r := newRegistryForSymlinkTest(t)
	r.SetConfirmFunc(func(ctx context.Context, req sdktools.ConfirmationRequest) (sdktools.ConfirmationResponse, error) {
		if strings.Contains(req.JudgeReasoning, "symlink") {
			t.Fatalf("symlink gate should NOT have intercepted OS-level temp dir symlink; JudgeReasoning=%q", req.JudgeReasoning)
		}
		return sdktools.ConfirmAllowOnce, nil
	})

	command := "cat " + testFile
	input, _ := json.Marshal(map[string]string{
		"command":           command,
		"working_directory": tmpDir,
	})
	ctx := sdktools.WithWorkspacePath(context.Background(), ws)
	ctx = sdktools.WithTempDir(ctx, tmpDir)

	result, err := r.Execute(ctx, shellExecToolName(), input)
	if err != nil {
		t.Fatalf("expected no error for temp dir OS-level symlink, got %v", err)
	}
	if result.IsError {
		t.Fatalf("expected success (OS-level temp symlink skipped), got error: %s", result.Content)
	}
}
