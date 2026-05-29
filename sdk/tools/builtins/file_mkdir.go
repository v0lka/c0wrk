package builtins

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/v0lka/c0wrk/sdk/tools"
)

const toolCreateDirectoryDescription = `Creates a directory at the specified path, including any necessary parent directories (like mkdir -p). Succeeds silently if the directory already exists. Use this to set up directory structure before writing files into it.`

// CreateDirectoryTool creates directories.
type CreateDirectoryTool struct {
	*tools.BaseTool
}

// NewCreateDirectoryTool creates a new CreateDirectoryTool instance.
func NewCreateDirectoryTool() *CreateDirectoryTool {
	return &CreateDirectoryTool{
		BaseTool: &tools.BaseTool{
			ToolName:        "create_directory",
			ToolDescription: toolCreateDirectoryDescription,
			Schema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"path": {
						"type": "string",
						"description": "Absolute or relative path of the directory to create. All intermediate parent directories are created as needed."
					}
				},
				"required": ["path"]
			}`),
			Policy: tools.PolicyUserConfirm,
		},
	}
}

// CreateDirectoryInput represents the input parameters for create_directory.
type CreateDirectoryInput struct {
	Path string `json:"path"`
}

// Judge uses workspace check for write operations.
func (t *CreateDirectoryTool) Judge(ctx context.Context, input json.RawMessage) (allowed bool, reason string) {
	var params CreateDirectoryInput
	if err := json.Unmarshal(input, &params); err != nil {
		return false, ""
	}
	params.Path = resolvePath(ctx, params.Path)
	return judgeWriteInWorkspace(ctx, params.Path)
}

// Execute creates a directory and all parent directories.
func (t *CreateDirectoryTool) Execute(ctx context.Context, input json.RawMessage) (tools.ToolResult, error) {
	var params CreateDirectoryInput
	if err := json.Unmarshal(input, &params); err != nil {
		return tools.ParseInputError(err)
	}

	if params.Path == "" {
		return tools.ToolResult{Content: "validation error: path is required", IsError: true}, nil
	}

	params.Path = resolvePath(ctx, params.Path)

	if err := os.MkdirAll(params.Path, 0o755); err != nil {
		return tools.ToolResult{Content: fmt.Sprintf("failed to create directory: %v", err), IsError: true}, nil
	}
	return tools.ToolResult{Content: "successfully created directory: " + params.Path, IsError: false}, nil
}
