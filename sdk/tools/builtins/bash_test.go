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

func mustNewBashExecTool(t *testing.T, blacklist []string) *BashExecTool {
	t.Helper()
	tool, err := NewBashExecTool(blacklist)
	if err != nil {
		t.Fatalf("NewBashExecTool: %v", err)
	}
	return tool
}

func TestBashExecTool_InvalidBlacklistPattern(t *testing.T) {
	_, err := NewBashExecTool([]string{"valid", "[invalid"})
	if err == nil {
		t.Fatal("expected error for invalid regex pattern, got nil")
	}
	if !strings.Contains(err.Error(), "[invalid") {
		t.Errorf("expected error to mention the invalid pattern, got: %v", err)
	}
}

func TestBashExecTool_Execute_Basic(t *testing.T) {
	tool := mustNewBashExecTool(t, nil)
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

func TestBashExecTool_EchoHello(t *testing.T) {
	tool := mustNewBashExecTool(t, nil)

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
	tool := mustNewBashExecTool(t, nil)

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
	tool := mustNewBashExecTool(t, nil)

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
	tool := mustNewBashExecTool(t, nil)
	if tool.DefaultPolicy() != tools.PolicyUserConfirm {
		t.Errorf("expected DefaultPolicy() to return PolicyUserConfirm, got %v", tool.DefaultPolicy())
	}
}

func TestBashExecTool_Judge_BlacklistMatch(t *testing.T) {
	tool := mustNewBashExecTool(t, []string{"rm -rf", "sudo"})

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
	tool := mustNewBashExecTool(t, []string{"rm -rf", "sudo"})

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
	tool := mustNewBashExecTool(t, nil)

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
	tool := mustNewBashExecTool(t, []string{"rm -rf"})

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
	tool := mustNewBashExecTool(t, nil)

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
	tool := mustNewBashExecTool(t, nil)

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
	tool := mustNewBashExecTool(t, nil)

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

func TestBashExec_NoTracker(t *testing.T) {
	tool := mustNewBashExecTool(t, nil)

	input, _ := json.Marshal(map[string]string{
		"command": "echo hello-world",
	})

	// No tracker in context — should behave exactly as before
	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Errorf("expected IsError=false, got true. Content: %s", result.Content)
	}
	if !strings.Contains(result.Content, "hello-world") {
		t.Errorf("expected output to contain 'hello-world', got: %s", result.Content)
	}
}

func TestBashExecTool_TimeoutKillsChildProcesses(t *testing.T) {
	// This test verifies that timeout kills the entire process group,
	// not just the parent bash process.
	tool := mustNewBashExecTool(t, nil)

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

	tool := mustNewBashExecTool(t, nil)

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
