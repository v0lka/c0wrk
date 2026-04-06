package builtins

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/user/agent/sdk/tools"
)

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
