package builtins

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/v0lka/sp4rk/tools"
)

const toolDeleteDirectoryDescription = `Deletes a directory at the specified path. By default, only empty directories can be deleted. Set recursive to true to remove the directory and all of its contents. Fails if the path is not a directory.`

// DeleteDirectoryTool deletes directories.
type DeleteDirectoryTool struct {
	*tools.BaseTool
}

// NewDeleteDirectoryTool creates a new DeleteDirectoryTool instance.
func NewDeleteDirectoryTool() *DeleteDirectoryTool {
	return &DeleteDirectoryTool{
		BaseTool: &tools.BaseTool{
			ToolName:        "delete_directory",
			ToolDescription: toolDeleteDirectoryDescription,
			Schema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"path": {
						"type": "string",
						"description": "Absolute or relative path of the directory to delete."
					},
					"recursive": {
						"type": "boolean",
						"description": "When true, deletes the directory and all files and subdirectories within it. When false, the operation fails if the directory is not empty."
					}
				},
				"required": ["path", "recursive"]
			}`),
			Policy: tools.PolicyUserConfirm,
		},
	}
}

// DeleteDirectoryInput represents the input parameters for delete_directory.
type DeleteDirectoryInput struct {
	Path      string `json:"path"`
	Recursive bool   `json:"recursive"`
}

// Judge uses session roots check for write operations.
func (t *DeleteDirectoryTool) Judge(ctx context.Context, input json.RawMessage) (allowed bool, reason string) {
	var params DeleteDirectoryInput
	if err := json.Unmarshal(input, &params); err != nil {
		return false, ""
	}
	params.Path = resolvePath(ctx, params.Path)
	if err := validateResolvedPath(params.Path); err != nil {
		return false, err.Error()
	}
	return judgeWriteInSessionRoots(ctx, params.Path)
}

// Execute deletes a directory. If recursive is true, it removes all contents.
func (t *DeleteDirectoryTool) Execute(ctx context.Context, input json.RawMessage) (tools.ToolResult, error) {
	var params DeleteDirectoryInput
	if err := json.Unmarshal(input, &params); err != nil {
		return tools.ParseInputError(err)
	}

	if params.Path == "" {
		return tools.ToolResult{Content: "validation error: path is required", IsError: true}, nil
	}

	params.Path = resolvePath(ctx, params.Path)
	if err := validateResolvedPath(params.Path); err != nil {
		return tools.ToolResult{Content: err.Error(), IsError: true}, nil //nolint:nilerr // error embedded in ToolResult by design
	}

	info, err := os.Stat(params.Path)
	if err != nil {
		return tools.ToolResult{Content: fmt.Sprintf("failed to stat path: %v", err), IsError: true}, nil
	}
	if !info.IsDir() {
		return tools.ToolResult{Content: "path is not a directory", IsError: true}, nil
	}

	if params.Recursive {
		if err := os.RemoveAll(params.Path); err != nil {
			return tools.ToolResult{Content: fmt.Sprintf("failed to delete directory: %v", err), IsError: true}, nil
		}
	} else {
		if err := os.Remove(params.Path); err != nil {
			return tools.ToolResult{Content: fmt.Sprintf("failed to delete directory: %v", err), IsError: true}, nil
		}
	}

	return tools.ToolResult{Content: "successfully deleted directory: " + params.Path, IsError: false}, nil
}
