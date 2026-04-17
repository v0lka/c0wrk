//go:build !windows

package builtins

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/user/agent/sdk/agent"
	"github.com/user/agent/sdk/tools"
)

// --- RTK integration tests ---

func TestBashExecTool_SetRtkPath(t *testing.T) {
	tool := NewBashExecTool(nil)

	// Initially empty
	if got := tool.getRtkPath(); got != "" {
		t.Errorf("expected empty rtk path, got %q", got)
	}

	// Set path
	tool.SetRtkPath("/usr/local/bin/rtk")
	if got := tool.getRtkPath(); got != "/usr/local/bin/rtk" {
		t.Errorf("expected /usr/local/bin/rtk, got %q", got)
	}

	// Update path
	tool.SetRtkPath("/opt/bin/rtk")
	if got := tool.getRtkPath(); got != "/opt/bin/rtk" {
		t.Errorf("expected /opt/bin/rtk, got %q", got)
	}
}

func TestBashExecTool_Execute_NoRtk(t *testing.T) {
	tool := NewBashExecTool(nil)
	ctx := context.Background()
	input := []byte(`{"command": "echo hello"}`)

	result, err := tool.Execute(ctx, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result: %s", result.Content)
	}
	if !strings.Contains(result.Content, "hello") {
		t.Errorf("expected output to contain 'hello', got %q", result.Content)
	}
}

func TestBashExecTool_Execute_InvalidRtkPath(t *testing.T) {
	tool := NewBashExecTool(nil)
	tool.SetRtkPath("/nonexistent/rtk")
	ctx := context.Background()
	input := []byte(`{"command": "echo hello"}`)

	result, err := tool.Execute(ctx, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result: %s", result.Content)
	}
	// Should still work - falls back to original command
	if !strings.Contains(result.Content, "hello") {
		t.Errorf("expected output to contain 'hello', got %q", result.Content)
	}
}

func TestBashExecTool_RtkRewrite_MockRtk(t *testing.T) {
	// Create a mock rtk script
	tmpDir := t.TempDir()
	mockRtk := filepath.Join(tmpDir, "rtk")

	// Script: if args are "rewrite <cmd>", output "echo REWRITTEN"
	script := `#!/bin/bash
if [ "$1" = "rewrite" ]; then
    echo "echo REWRITTEN"
else
    echo "unknown"
fi
`
	if err := os.WriteFile(mockRtk, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	tool := NewBashExecToolWithTimeouts(nil, BashTimeouts{
		MaxTimeout: 120 * time.Second,
		WaitDelay:  5 * time.Second,
		RtkTimeout: 5 * time.Second,
	}, "")
	tool.SetRtkPath(mockRtk)
	ctx := context.Background()
	input := []byte(`{"command": "echo hello"}`)

	result, err := tool.Execute(ctx, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result: %s", result.Content)
	}
	// The mock rtk rewrites "echo hello" to "echo REWRITTEN"
	if !strings.Contains(result.Content, "REWRITTEN") {
		t.Errorf("expected rewritten output, got %q", result.Content)
	}
}

func TestBashExecTool_ConstructorWithRtkPath(t *testing.T) {
	tool := NewBashExecToolWithTimeouts(nil, DefaultBashTimeouts(), "/usr/bin/rtk")
	if got := tool.getRtkPath(); got != "/usr/bin/rtk" {
		t.Errorf("expected /usr/bin/rtk from constructor, got %q", got)
	}
}


func TestBashExecTool_EchoHello(t *testing.T) {
	tool := NewBashExecTool(nil)

	input, _ := json.Marshal(map[string]string{
		"command": "echo hello",
	})

	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.IsError {
		t.Errorf("expected IsError=false, got true. Content: %s", result.Content)
	}

	if result.Content != "hello\n" {
		t.Errorf("expected content 'hello\\n', got %q", result.Content)
	}
}

func TestBashExecTool_NonZeroExitCode(t *testing.T) {
	tool := NewBashExecTool(nil)

	input, _ := json.Marshal(map[string]string{
		"command": "false",
	})

	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.IsError {
		t.Errorf("expected IsError=true for non-zero exit code")
	}
}

func TestBashExecTool_Timeout(t *testing.T) {
	tool := NewBashExecTool(nil)

	input, _ := json.Marshal(map[string]string{
		"command": "sleep 10",
		"timeout": "1s",
	})

	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.IsError {
		t.Errorf("expected IsError=true for timeout")
	}

	if !strings.Contains(result.Content, "signal: killed") && !strings.Contains(result.Content, "context deadline exceeded") {
		t.Errorf("expected timeout-related error message, got: %s", result.Content)
	}
}

func TestBashExecTool_DefaultPolicy(t *testing.T) {
	tool := NewBashExecTool(nil)
	if tool.DefaultPolicy() != tools.PolicyUserConfirm {
		t.Errorf("expected DefaultPolicy() to return PolicyUserConfirm, got %v", tool.DefaultPolicy())
	}
}

func TestBashExecTool_Judge_BlacklistMatch(t *testing.T) {
	tool := NewBashExecTool([]string{"rm -rf", "sudo"})

	input, _ := json.Marshal(map[string]string{
		"command": "rm -rf /",
	})

	allow, reasoning := tool.Judge(context.Background(), input)
	if allow {
		t.Error("expected Judge to return allow=false for blacklisted command")
	}
	if reasoning == "" {
		t.Error("expected reasoning to be non-empty for blacklisted command")
	}
	if !strings.Contains(reasoning, "blacklist") {
		t.Errorf("expected reasoning to mention blacklist, got: %s", reasoning)
	}
}

func TestBashExecTool_Judge_NoBlacklistMatch(t *testing.T) {
	tool := NewBashExecTool([]string{"rm -rf", "sudo"})

	input, _ := json.Marshal(map[string]string{
		"command": "echo hello",
	})

	allow, reasoning := tool.Judge(context.Background(), input)
	if allow {
		t.Error("expected Judge to return allow=false for non-blacklisted command")
	}
	if reasoning != "" {
		t.Errorf("expected empty reasoning for non-blacklisted command, got: %s", reasoning)
	}
}

func TestBashExecTool_Judge_EmptyBlacklist(t *testing.T) {
	tool := NewBashExecTool(nil)

	input, _ := json.Marshal(map[string]string{
		"command": "rm -rf /",
	})

	allow, reasoning := tool.Judge(context.Background(), input)
	if allow {
		t.Error("expected Judge to return allow=false with empty blacklist")
	}
	if reasoning != "" {
		t.Errorf("expected empty reasoning with empty blacklist, got: %s", reasoning)
	}
}

func TestBashExecTool_Judge_InvalidJSON(t *testing.T) {
	tool := NewBashExecTool([]string{"rm -rf"})

	allow, reasoning := tool.Judge(context.Background(), json.RawMessage(`{invalid`))
	if allow {
		t.Error("expected Judge to return allow=false for invalid JSON")
	}
	if reasoning != "" {
		t.Errorf("expected empty reasoning for invalid JSON, got: %s", reasoning)
	}
}

func bashCtxWithTracker(t *testing.T, workspaceRoot string) (context.Context, *agent.FileChangeTracker) {
	t.Helper()
	tracker := agent.NewFileChangeTracker(workspaceRoot)
	ctx := agent.WithFileTracker(context.Background(), tracker)
	ctx = agent.WithStepID(ctx, "test-step")
	return ctx, tracker
}

func findChange(changes []agent.FileChange, op string) *agent.FileChange {
	for i := range changes {
		if changes[i].Operation == op {
			return &changes[i]
		}
	}
	return nil
}

func TestBashExec_DetectsFileCreation(t *testing.T) {
	tmpDir := t.TempDir()
	ctx, tracker := bashCtxWithTracker(t, tmpDir)
	tool := NewBashExecTool(nil)

	newFile := filepath.Join(tmpDir, "created.txt")
	input, _ := json.Marshal(map[string]string{
		"command": `echo "hello" > ` + newFile,
	})

	result, err := tool.Execute(ctx, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected success, got error: %s", result.Content)
	}

	changes := tracker.GetStepChanges("test-step")
	if len(changes) == 0 {
		t.Fatal("expected at least one change, got none")
	}

	c := findChange(changes, "CREATE")
	if c == nil {
		t.Fatalf("expected CREATE operation, got: %+v", changes)
	}
	if c.Path != "created.txt" {
		t.Errorf("expected path 'created.txt', got %q", c.Path)
	}
}

func TestBashExec_DetectsFileModification(t *testing.T) {
	tmpDir := t.TempDir()

	// Create existing file before tracker snapshot
	existingFile := filepath.Join(tmpDir, "existing.txt")
	if err := os.WriteFile(existingFile, []byte("original"), 0o644); err != nil {
		t.Fatalf("failed to create existing file: %v", err)
	}

	ctx, tracker := bashCtxWithTracker(t, tmpDir)
	tool := NewBashExecTool(nil)

	input, _ := json.Marshal(map[string]string{
		"command": `echo "modified" > ` + existingFile,
	})

	result, err := tool.Execute(ctx, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected success, got error: %s", result.Content)
	}

	changes := tracker.GetStepChanges("test-step")
	if len(changes) == 0 {
		t.Fatal("expected at least one change, got none")
	}

	c := findChange(changes, "MODIFY")
	if c == nil {
		t.Fatalf("expected MODIFY operation, got: %+v", changes)
	}
	if c.Path != "existing.txt" {
		t.Errorf("expected path 'existing.txt', got %q", c.Path)
	}
}

func TestBashExec_DetectsFileDeletion(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a file to be deleted
	victimFile := filepath.Join(tmpDir, "victim.txt")
	if err := os.WriteFile(victimFile, []byte("doomed"), 0o644); err != nil {
		t.Fatalf("failed to create victim file: %v", err)
	}

	ctx, tracker := bashCtxWithTracker(t, tmpDir)
	tool := NewBashExecTool(nil)

	input, _ := json.Marshal(map[string]string{
		"command": "rm " + victimFile,
	})

	result, err := tool.Execute(ctx, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected success, got error: %s", result.Content)
	}

	changes := tracker.GetStepChanges("test-step")
	if len(changes) == 0 {
		t.Fatal("expected at least one change, got none")
	}

	c := findChange(changes, "DELETE")
	if c == nil {
		t.Fatalf("expected DELETE operation, got: %+v", changes)
	}
	if c.Path != "victim.txt" {
		t.Errorf("expected path 'victim.txt', got %q", c.Path)
	}
}

func TestBashExec_NoTracker_BackwardCompat(t *testing.T) {
	tool := NewBashExecTool(nil)

	input, _ := json.Marshal(map[string]string{
		"command": "echo backward-compat",
	})

	// No tracker in context — should behave exactly as before
	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Errorf("expected IsError=false, got true. Content: %s", result.Content)
	}
	if !strings.Contains(result.Content, "backward-compat") {
		t.Errorf("expected output to contain 'backward-compat', got: %s", result.Content)
	}
}

func TestBashExecTool_TimeoutKillsChildProcesses(t *testing.T) {
	// This test verifies that timeout kills the entire process group,
	// not just the parent bash process.
	tool := NewBashExecTool(nil)

	input, _ := json.Marshal(map[string]string{
		"command": "bash -c 'sleep 300 & sleep 300 & wait'",
		"timeout": "2s",
	})

	start := time.Now()
	result, err := tool.Execute(context.Background(), input)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("unexpected Go-level error: %v", err)
	}

	// Should complete in roughly 2 seconds + grace period, not 300 seconds
	if elapsed > 15*time.Second {
		t.Fatalf("command took %v, expected to be killed by timeout within ~7s", elapsed)
	}

	// The result should indicate timeout
	if !strings.Contains(strings.ToLower(result.Content), "timeout") {
		t.Errorf("expected result to mention timeout, got: %s", result.Content)
	}
}

func TestBashExecTool_WorkingDirectory(t *testing.T) {
	// Create a temp directory
	tmpDir, err := os.MkdirTemp("", "bash_test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	tool := NewBashExecTool(nil)

	input, _ := json.Marshal(map[string]string{
		"command":           "pwd",
		"working_directory": tmpDir,
	})

	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.IsError {
		t.Errorf("expected IsError=false, got true. Content: %s", result.Content)
	}

	// On macOS, /var is a symlink to /private/var, so we need to resolve symlinks for both paths
	resolvedTmpDir, err := filepath.EvalSymlinks(tmpDir)
	if err != nil {
		t.Fatalf("failed to resolve symlinks for tmpDir: %v", err)
	}

	gotPath := strings.TrimSpace(result.Content)
	resolvedGot, err := filepath.EvalSymlinks(gotPath)
	if err != nil {
		t.Fatalf("failed to resolve symlinks for result: %v", err)
	}

	if resolvedGot != resolvedTmpDir {
		t.Errorf("expected working directory %q, got %q", resolvedTmpDir, resolvedGot)
	}
}
