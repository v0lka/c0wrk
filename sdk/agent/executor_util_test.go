package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/v0lka/sp4rk/tools"
)

// mockToolExecutorMCP is a mock ToolExecutor that simulates MCP tools.
type mockToolExecutorMCP struct{}

func (m *mockToolExecutorMCP) Execute(_ context.Context, _ string, _ json.RawMessage) (tools.ToolResult, error) {
	return tools.ToolResult{Content: "mcp result"}, nil
}
func (m *mockToolExecutorMCP) GetToolSource(_ string) string { return "mcp:test" }
func (m *mockToolExecutorMCP) IsToolUntrusted(_ string) bool { return true }

var _ ToolExecutor = (*mockToolExecutorMCP)(nil)

func TestBuildCacheMeta_FileTool_Nonexistent(t *testing.T) {
	tmpDir := t.TempDir()
	ctx := tools.WithWorkspacePath(context.Background(), tmpDir)
	mockTools := newMockToolExecutor()
	exec := newExecutorDefaultHITL(&mockLLMCaller{}, mockTools, &mockTokenCounter{}, 10, nil, false, ToolResultBudget{}, defaultCircuitBreakerConfig)
	meta := exec.buildCacheMeta(ctx, tools.ToolReadFile, json.RawMessage(`{"path": "/nonexistent/file.txt"}`))
	if meta.FilePath != "" {
		t.Error("buildCacheMeta should return empty FilePath for nonexistent file")
	}
}

func TestBuildCacheMeta_MCPTool(t *testing.T) {
	mcpTools := &mockToolExecutorMCP{}
	exec := newExecutorDefaultHITL(&mockLLMCaller{}, mcpTools, &mockTokenCounter{}, 10, nil, false, ToolResultBudget{}, defaultCircuitBreakerConfig)
	meta := exec.buildCacheMeta(context.Background(), "mcp_search", json.RawMessage(`{}`))
	if !meta.IsMCP {
		t.Error("buildCacheMeta should set IsMCP=true for MCP tools")
	}
	if meta.FilePath != "" {
		t.Error("MCP tools should not have file coherence metadata")
	}
}

func TestBuildCacheMeta_PathOutsideWorkspace(t *testing.T) {
	tmpDir := t.TempDir()
	ctx := tools.WithWorkspacePath(context.Background(), tmpDir)
	mockTools := newMockToolExecutor()
	exec := newExecutorDefaultHITL(&mockLLMCaller{}, mockTools, &mockTokenCounter{}, 10, nil, false, ToolResultBudget{}, defaultCircuitBreakerConfig)
	meta := exec.buildCacheMeta(ctx, tools.ToolReadFile, json.RawMessage(`{"path": "/etc/hosts"}`))
	if meta.FilePath != "" {
		t.Error("buildCacheMeta should return empty meta for path outside workspace")
	}
}

func TestBuildCacheMeta_NonFileTool(t *testing.T) {
	mockTools := newMockToolExecutor()
	exec := newExecutorDefaultHITL(&mockLLMCaller{}, mockTools, &mockTokenCounter{}, 10, nil, false, ToolResultBudget{}, defaultCircuitBreakerConfig)
	meta := exec.buildCacheMeta(context.Background(), "search", json.RawMessage(`{}`))
	if meta.FilePath != "" {
		t.Error("non-file tool should not have FilePath")
	}
	if meta.IsMCP {
		t.Error("non-MCP tool should not have IsMCP=true")
	}
}

func TestBuildCacheMeta_ExistingFile(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "test.txt")
	if err := os.WriteFile(filePath, []byte("hello world"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := tools.WithWorkspacePath(context.Background(), tmpDir)
	mockTools := newMockToolExecutor()
	exec := newExecutorDefaultHITL(&mockLLMCaller{}, mockTools, &mockTokenCounter{}, 10, nil, false, ToolResultBudget{}, defaultCircuitBreakerConfig)
	input, _ := json.Marshal(map[string]string{"path": "test.txt"})
	meta := exec.buildCacheMeta(ctx, tools.ToolReadFile, input)
	if meta.FilePath == "" {
		t.Error("buildCacheMeta should set FilePath for existing file")
	}
	info, err := os.Stat(filePath)
	if err != nil {
		t.Fatal(err)
	}
	if meta.FileMtime != info.ModTime().UnixNano() {
		t.Error("FileMtime should match file's mod time")
	}
	if meta.FileSize != info.Size() {
		t.Errorf("FileSize = %d, want %d", meta.FileSize, info.Size())
	}
}

func TestBuildCacheMeta_WriteFile_ExistingFile(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "output.txt")
	if err := os.WriteFile(filePath, []byte("output content"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := tools.WithWorkspacePath(context.Background(), tmpDir)
	mockTools := newMockToolExecutor()
	exec := newExecutorDefaultHITL(&mockLLMCaller{}, mockTools, &mockTokenCounter{}, 10, nil, false, ToolResultBudget{}, defaultCircuitBreakerConfig)
	input, _ := json.Marshal(map[string]string{"path": "output.txt"})
	meta := exec.buildCacheMeta(ctx, tools.ToolWriteFile, input)
	if meta.FilePath == "" {
		t.Error("buildCacheMeta should set FilePath for existing file with WriteFile")
	}
}

func TestBuildCacheMeta_EditFile_ExistingFile(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "edit_me.txt")
	if err := os.WriteFile(filePath, []byte("edit content"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := tools.WithWorkspacePath(context.Background(), tmpDir)
	mockTools := newMockToolExecutor()
	exec := newExecutorDefaultHITL(&mockLLMCaller{}, mockTools, &mockTokenCounter{}, 10, nil, false, ToolResultBudget{}, defaultCircuitBreakerConfig)
	input, _ := json.Marshal(map[string]string{"path": "edit_me.txt"})
	meta := exec.buildCacheMeta(ctx, tools.ToolEditFile, input)
	if meta.FilePath == "" {
		t.Error("buildCacheMeta should set FilePath for existing file with EditFile")
	}
}
