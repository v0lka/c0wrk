package builtins

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/v0lka/sp4rk/tools"
)

const toolWriteFileDescription = `Creates or overwrites a file with the provided content. Parent directories are created automatically if they do not exist. Use this tool to create new files or fully replace existing file contents. For making targeted changes to an existing file, prefer edit_file instead. Always read the file first before overwriting to avoid accidental data loss.`

// WriteFileTool creates or overwrites files.
type WriteFileTool struct {
	*tools.BaseTool
}

// NewWriteFileTool creates a new WriteFileTool instance.
func NewWriteFileTool() *WriteFileTool {
	return &WriteFileTool{
		BaseTool: &tools.BaseTool{
			ToolName:        "write_file",
			ToolDescription: toolWriteFileDescription,
			Schema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"path": {
						"type": "string",
						"description": "Absolute or relative path to the file to create or overwrite."
					},
					"content": {
						"type": "string",
						"description": "The full text content to write to the file. Replaces any existing content entirely."
					}
				},
				"required": ["path", "content"]
			}`),
			Policy: tools.PolicyUserConfirm,
		},
	}
}

// WriteFileInput represents the input parameters for write_file.
type WriteFileInput struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// Judge uses session roots check for write operations.
func (t *WriteFileTool) Judge(ctx context.Context, input json.RawMessage) (allowed bool, reason string) {
	var params WriteFileInput
	if err := json.Unmarshal(input, &params); err != nil {
		return false, ""
	}
	params.Path = resolvePath(ctx, params.Path)
	if err := validateResolvedPath(params.Path); err != nil {
		return false, err.Error()
	}
	return judgeWriteInSessionRoots(ctx, params.Path)
}

// Execute writes content to a file, creating parent directories if needed.
func (t *WriteFileTool) Execute(ctx context.Context, input json.RawMessage) (tools.ToolResult, error) {
	var params WriteFileInput
	if err := json.Unmarshal(input, &params); err != nil {
		return tools.ParseInputError(err)
	}

	if params.Path == "" {
		return tools.ToolResult{Content: "validation error: path is required", IsError: true}, nil
	}

	params.Path = resolvePath(ctx, params.Path)
	if err := validateResolvedPath(params.Path); err != nil {
		return tools.ToolResult{Content: err.Error(), IsError: true}, nil
	}

	// Coherence check: block write if file was modified since this session's last read.
	checker := tools.CoherenceFrom(ctx)
	if checker != nil {
		checker.Lock(params.Path)
		if conflict := checker.CheckWrite(ctx, params.Path); conflict != nil {
			checker.Unlock(params.Path)
			return tools.ToolResult{Content: formatWriteConflict(conflict), IsError: true}, nil
		}
		defer checker.Unlock(params.Path)
	}

	dir := filepath.Dir(params.Path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return tools.ToolResult{Content: fmt.Sprintf("failed to create directories: %v", err), IsError: true}, nil
	}

	if err := os.WriteFile(params.Path, []byte(params.Content), 0o644); err != nil {
		return tools.ToolResult{Content: fmt.Sprintf("failed to write file: %v", err), IsError: true}, nil
	}

	if checker != nil {
		checker.RecordWrite(ctx, params.Path)
	}

	return tools.ToolResult{Content: fmt.Sprintf("successfully wrote %d bytes to %s", len(params.Content), params.Path), IsError: false}, nil
}
