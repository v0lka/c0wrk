package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	sdktools "github.com/v0lka/c0wrk/sdk/tools"
	"github.com/v0lka/c0wrk/sdk/tools/builtins"
)
// ── Registry integration tests ────────────────────────────────────────────

func newRegistryForSymlinkTest(t *testing.T) *ToolRegistry {
	t.Helper()
	r := NewToolRegistry()
	_ = RegisterBuiltinTools(r, BuiltinToolsConfig{
		BashTimeouts: builtins.BashTimeouts{MaxTimeout: 120 * time.Second, WaitDelay: 5 * time.Second},
	})
	return r
}

func TestCheckSymlinksAndConfirm_InternalToolBypass(t *testing.T) {
	r := newRegistryForSymlinkTest(t)
	// finish is an internal tool that bypasses policy and symlink checks.
	// This test verifies that the symlink gate is placed AFTER internal bypass in Execute().
	ctx := context.Background()

	// Execute should succeed without going through symlink check or confirmation
	result, err := r.Execute(ctx, "finish", json.RawMessage(`{"answer":"test"}`))
	if err != nil {
		t.Fatalf("expected no error for internal tool, got %v", err)
	}
	if result.IsError {
		t.Fatalf("expected successful execution for internal tool, got error: %s", result.Content)
	}
}

func TestCheckSymlinksAndConfirm_CleanPathNoIntercept(t *testing.T) {
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

func TestCheckSymlinksAndConfirm_Intercepts(t *testing.T) {
	dir := t.TempDir()
	realDir := filepath.Join(dir, "real")
	_ = os.MkdirAll(realDir, 0o755)
	_ = os.WriteFile(filepath.Join(realDir, "file.txt"), []byte("data"), 0o644)
	symlinkPath := filepath.Join(dir, "link")
	_ = os.Symlink(realDir, symlinkPath)

	r := newRegistryForSymlinkTest(t)
	confirmed := make(chan sdktools.ConfirmationRequest, 1)
	r.SetConfirmFunc(func(ctx context.Context, req sdktools.ConfirmationRequest) (sdktools.ConfirmationResponse, error) {
		confirmed <- req
		return sdktools.ConfirmAllowOnce, nil
	})

	nestedPath := filepath.Join(symlinkPath, "file.txt")
	input, _ := json.Marshal(map[string]string{"path": nestedPath})
	ctx := sdktools.WithWorkspacePath(context.Background(), dir)

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
			t.Fatal("expected non-empty JudgeReasoning for symlink intercept")
		}
		if !stringsContains(req.JudgeReasoning, "link") {
			t.Fatalf("expected reasoning to mention symlink, got: %s", req.JudgeReasoning)
		}
	default:
		t.Fatal("expected confirmFunc to be called")
	}
}

func TestCheckSymlinksAndConfirm_RespectsAlwaysDeny(t *testing.T) {
	dir := t.TempDir()
	realDir := filepath.Join(dir, "real")
	_ = os.MkdirAll(realDir, 0o755)
	symlinkPath := filepath.Join(dir, "link")
	_ = os.Symlink(realDir, symlinkPath)

	r := newRegistryForSymlinkTest(t)
	r.SetPolicyOverrides(map[string]sdktools.ToolPolicy{
		"read_file": sdktools.PolicyAlwaysDeny,
	})
	r.SetConfirmFunc(func(ctx context.Context, req sdktools.ConfirmationRequest) (sdktools.ConfirmationResponse, error) {
		t.Fatal("confirmFunc should NOT be called when policy is AlwaysDeny")
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
		t.Fatal("expected error result for AlwaysDeny blocked tool")
	}
	if !stringsContains(result.Content, "blocked by security policy") {
		t.Fatalf("expected blocked by security policy message, got: %s", result.Content)
	}
}

func TestCheckSymlinksAndConfirm_DenyResponse(t *testing.T) {
	dir := t.TempDir()
	realDir := filepath.Join(dir, "real")
	_ = os.MkdirAll(realDir, 0o755)
	symlinkPath := filepath.Join(dir, "link")
	_ = os.Symlink(realDir, symlinkPath)

	r := newRegistryForSymlinkTest(t)
	r.SetConfirmFunc(func(ctx context.Context, req sdktools.ConfirmationRequest) (sdktools.ConfirmationResponse, error) {
		return sdktools.ConfirmDeny, nil
	})

	nestedPath := filepath.Join(symlinkPath, "file.txt")
	input, _ := json.Marshal(map[string]string{"path": nestedPath})
	ctx := sdktools.WithWorkspacePath(context.Background(), dir)

	result, err := r.Execute(ctx, "read_file", input)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if !result.IsError {
		t.Fatal("expected error result for denied symlink tool call")
	}
	if !stringsContains(result.Content, "denied by user") {
		t.Fatalf("expected 'denied by user' message, got: %s", result.Content)
	}
}

func TestCheckSymlinksAndConfirm_BashExecWithSymlink(t *testing.T) {
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

	result, err := r.Execute(ctx, "bash_exec", input)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if result.IsError {
		t.Fatalf("expected successful bash exec after confirm, got error: %s", result.Content)
	}

	select {
	case reasoning := <-confirmed:
		if reasoning == "" {
			t.Fatal("expected non-empty reasoning for bash symlink intercept")
		}
	default:
		t.Fatal("expected confirmFunc to be called for bash_exec with symlink")
	}
}

func TestCheckSymlinksAndConfirm_BashExecSuspiciousForceConfirm(t *testing.T) {
	r := newRegistryForSymlinkTest(t)
	confirmed := make(chan struct{}, 1)
	r.SetConfirmFunc(func(ctx context.Context, req sdktools.ConfirmationRequest) (sdktools.ConfirmationResponse, error) {
		confirmed <- struct{}{}
		return sdktools.ConfirmDeny, nil
	})

	input, _ := json.Marshal(map[string]string{"command": "cat $HOME/file"})
	ctx := context.Background()

	result, err := r.Execute(ctx, "bash_exec", input)
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

func TestCheckSymlinksAndConfirm_EmptyInput(t *testing.T) {
	r := newRegistryForSymlinkTest(t)
	// Don't set confirmFunc — we're only checking that the symlink gate
	// doesn't intercept. Using read_file (PolicyAlwaysAllow) so that the
	// normal policy flow doesn't call confirmFunc either.
	// The input has no path field, so the symlink gate should not intercept.
	input, _ := json.Marshal(map[string]string{"timeout": "30s"})
	ctx := sdktools.WithWorkspacePath(context.Background(), "/workspace")
	result, err := r.Execute(ctx, "read_file", input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// read_file requires "path" — tool-level validation error expected, not symlink block
	if result.IsError && stringsContains(result.Content, "security policy") {
		t.Fatal("symlink gate should not have intercepted empty input")
	}
}

func TestCheckSymlinksAndConfirm_TempDirOSLevelSymlink(t *testing.T) {
	// Create a temp dir under /tmp, which is an OS-level symlink to /private/tmp
	// on macOS. Verify that the symlink gate skips this OS-level infrastructure
	// rather than forcing a symlink-specific confirmation.
	//
	// Note: bash_exec defaults to PolicyUserConfirm, so the registry will still
	// require user confirmation for the call — but the confirmation must come
	// from the regular policy path with no symlink reasoning attached, NOT the
	// symlink gate. The test asserts that distinction by allowing the
	// confirmation to fire only when JudgeReasoning is empty (= regular policy
	// confirm), and failing when JudgeReasoning mentions "symlink".
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
		if stringsContains(req.JudgeReasoning, "symlink") {
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

	result, err := r.Execute(ctx, "bash_exec", input)
	if err != nil {
		t.Fatalf("expected no error for temp dir OS-level symlink, got %v", err)
	}
	if result.IsError {
		t.Fatalf("expected success (OS-level temp symlink skipped), got error: %s", result.Content)
	}
}

// ── helpers ───────────────────────────────────────────────────────────────

func stringsContains(s, sub string) bool {
	return sub == "" || len(s) >= len(sub) && containsSub(s, sub)
}

func containsSub(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
