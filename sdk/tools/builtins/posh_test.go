//go:build windows

package builtins

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/v0lka/sp4rk/tools"
)

func mustNewPoshExecTool(t *testing.T, blacklist []string) *PoshExecTool {
	t.Helper()
	tool, err := NewPoshExecTool(blacklist)
	if err != nil {
		t.Fatalf("NewPoshExecTool: %v", err)
	}
	return tool
}

func TestPoshExecTool_InvalidBlacklistPattern(t *testing.T) {
	_, err := NewPoshExecTool([]string{"valid", "[invalid"})
	if err == nil {
		t.Fatal("expected error for invalid regex pattern, got nil")
	}
	if !strings.Contains(err.Error(), "[invalid") {
		t.Errorf("expected error to mention the invalid pattern, got: %v", err)
	}
}

func TestPoshExecTool_Execute_Basic(t *testing.T) {
	tool := mustNewPoshExecTool(t, nil)
	ctx := context.Background()
	input := []byte(`{"command": "Write-Output hello"}`)

	result, err := tool.Execute(ctx, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result: %s", result.Content)
	}
	if !strings.Contains(strings.TrimSpace(result.Content), "hello") {
		t.Errorf("expected output to contain 'hello', got %q", result.Content)
	}
}

func TestPoshExecTool_EchoHello(t *testing.T) {
	tool := mustNewPoshExecTool(t, nil)

	input, _ := json.Marshal(map[string]string{
		"command": "Write-Output hello",
	})

	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.IsError {
		t.Errorf("expected IsError=false, got true. Content: %s", result.Content)
	}

	if got := strings.TrimSpace(result.Content); got != "hello" {
		t.Errorf("expected trimmed content 'hello', got %q", got)
	}
}

func TestPoshExecTool_NonZeroExitCode(t *testing.T) {
	tool := mustNewPoshExecTool(t, nil)

	input, _ := json.Marshal(map[string]string{
		"command": "exit 1",
	})

	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.IsError {
		t.Errorf("expected IsError=true for non-zero exit code")
	}
}

func TestPoshExecTool_Timeout(t *testing.T) {
	tool := mustNewPoshExecTool(t, nil)

	input, _ := json.Marshal(map[string]string{
		"command": "Start-Sleep -Seconds 10",
		"timeout": "1s",
	})

	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.IsError {
		t.Errorf("expected IsError=true for timeout")
	}

	if !strings.Contains(strings.ToLower(result.Content), "timeout") {
		t.Errorf("expected timeout-related error message, got: %s", result.Content)
	}
}

func TestPoshExecTool_DefaultPolicy(t *testing.T) {
	tool := mustNewPoshExecTool(t, nil)
	if tool.DefaultPolicy() != tools.PolicyUserConfirm {
		t.Errorf("expected DefaultPolicy() to return PolicyUserConfirm, got %v", tool.DefaultPolicy())
	}
}

func TestPoshExecTool_Judge_BlacklistMatch(t *testing.T) {
	tool := mustNewPoshExecTool(t, []string{"Remove-Item -Recurse -Force", "Stop-Computer"})

	input, _ := json.Marshal(map[string]string{
		"command": "Remove-Item -Recurse -Force C:\\",
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

func TestPoshExecTool_Judge_NoBlacklistMatch(t *testing.T) {
	tool := mustNewPoshExecTool(t, []string{"Remove-Item -Recurse -Force", "Stop-Computer"})

	input, _ := json.Marshal(map[string]string{
		"command": "Write-Output hello",
	})

	allow, reasoning := tool.Judge(context.Background(), input)
	if allow {
		t.Error("expected Judge to return allow=false for non-blacklisted command")
	}
	if reasoning != "" {
		t.Errorf("expected empty reasoning for non-blacklisted command, got: %s", reasoning)
	}
}

func TestPoshExecTool_Judge_EmptyBlacklist(t *testing.T) {
	tool := mustNewPoshExecTool(t, nil)

	input, _ := json.Marshal(map[string]string{
		"command": "Remove-Item -Recurse -Force C:\\",
	})

	allow, reasoning := tool.Judge(context.Background(), input)
	if allow {
		t.Error("expected Judge to return allow=false with empty blacklist")
	}
	if reasoning != "" {
		t.Errorf("expected empty reasoning with empty blacklist, got: %s", reasoning)
	}
}

func TestPoshExecTool_Judge_InvalidJSON(t *testing.T) {
	tool := mustNewPoshExecTool(t, []string{"Remove-Item -Recurse -Force"})

	allow, reasoning := tool.Judge(context.Background(), json.RawMessage(`{invalid`))
	if allow {
		t.Error("expected Judge to return allow=false for invalid JSON")
	}
	if reasoning != "" {
		t.Errorf("expected empty reasoning for invalid JSON, got: %s", reasoning)
	}
}

func TestPoshExecTool_WorkingDirectory(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "posh_test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	tool := mustNewPoshExecTool(t, nil)

	input, _ := json.Marshal(map[string]string{
		"command":           "Write-Output (Get-Location).Path",
		"working_directory": tmpDir,
	})

	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.IsError {
		t.Errorf("expected IsError=false, got true. Content: %s", result.Content)
	}

	resolvedTmpDir, err := filepath.EvalSymlinks(tmpDir)
	if err != nil {
		t.Fatalf("failed to resolve symlinks for tmpDir: %v", err)
	}

	gotPath := strings.TrimSpace(result.Content)
	resolvedGot, err := filepath.EvalSymlinks(gotPath)
	if err != nil {
		t.Fatalf("failed to resolve symlinks for result: %v", err)
	}

	if !strings.EqualFold(resolvedGot, resolvedTmpDir) {
		t.Errorf("expected working directory %q, got %q", resolvedTmpDir, resolvedGot)
	}
}

func TestPoshExecTool_TimeoutCompletesQuickly(t *testing.T) {
	tool := mustNewPoshExecTool(t, nil)

	input, _ := json.Marshal(map[string]string{
		"command": "Start-Sleep -Seconds 300",
		"timeout": "2s",
	})

	start := time.Now()
	result, err := tool.Execute(context.Background(), input)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("unexpected Go-level error: %v", err)
	}

	if elapsed > 15*time.Second {
		t.Fatalf("command took %v, expected to be killed by timeout within ~7s", elapsed)
	}

	if !strings.Contains(strings.ToLower(result.Content), "timeout") {
		t.Errorf("expected result to mention timeout, got: %s", result.Content)
	}
}
