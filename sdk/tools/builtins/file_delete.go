package builtins

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/user/agent/sdk/agent"
	"github.com/user/agent/sdk/tools"
)

const toolDeleteFileDescription = `Deletes a single file at the specified path. This is a destructive operation — verify the target path is correct before executing. Fails if the path points to a directory — use delete_directory for directories. Prefer creating new files over deleting existing ones when possible.`

// DeleteFileTool deletes files.
type DeleteFileTool struct {
	*tools.BaseTool
}

// NewDeleteFileTool creates a new DeleteFileTool instance.
func NewDeleteFileTool() *DeleteFileTool {
	return &DeleteFileTool{
		BaseTool: &tools.BaseTool{
			ToolName:        "delete_file",
			ToolDescription: toolDeleteFileDescription,
			Schema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"path": {
						"type": "string",
						"description": "Absolute or relative path of the file to delete. Must point to a regular file, not a directory."
					}
				},
				"required": ["path"]
			}`),
			Policy: tools.PolicyUserConfirm,
		},
	}
}

// DeleteFileInput represents the input parameters for delete_file.
type DeleteFileInput struct {
	Path string `json:"path"`
}

// Judge uses workspace check for write operations.
func (t *DeleteFileTool) Judge(ctx context.Context, input json.RawMessage) (allowed bool, reason string) {
	var params DeleteFileInput
	if err := json.Unmarshal(input, &params); err != nil {
		return false, ""
	}
	params.Path = resolvePath(ctx, params.Path)
	return judgeWriteInWorkspace(ctx, params.Path)
}

// Execute deletes a single file. Returns an error if the path is a directory.
func (t *DeleteFileTool) Execute(ctx context.Context, input json.RawMessage) (tools.ToolResult, error) {
	var params DeleteFileInput
	if err := json.Unmarshal(input, &params); err != nil {
		return tools.ParseInputError(err)
	}

	params.Path = resolvePath(ctx, params.Path)

	info, err := os.Stat(params.Path)
	if err != nil {
		return tools.ToolResult{Content: fmt.Sprintf("failed to stat path: %v", err), IsError: true}, nil
	}
	if info.IsDir() {
		return tools.ToolResult{Content: "path is a directory, use delete_directory instead", IsError: true}, nil
	}

	tracker := agent.FileTrackerFromContext(ctx)
	if tracker != nil {
		tracker.AcquireFileLock(params.Path)
		defer tracker.ReleaseFileLock(params.Path)
		tracker.RecordDelete(ctx, params.Path)
	}

	if err := os.Remove(params.Path); err != nil {
		return tools.ToolResult{Content: fmt.Sprintf("failed to delete file: %v", err), IsError: true}, nil
	}
	return tools.ToolResult{Content: "successfully deleted file: " + params.Path, IsError: false}, nil
}
