package builtins

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/user/agent/sdk/tools"
)

const toolEditFileDescription = `Performs a find-and-replace edit on an existing file. Locates a single exact occurrence of old_string in the file and replaces it with new_string. The operation fails if old_string is not found or if it matches more than once — provide enough surrounding context in old_string to ensure a unique match. Prefer this tool over write_file for surgical modifications to existing files.`

// EditFileTool performs find-and-replace edits on files.
type EditFileTool struct {
	*tools.BaseTool
}

// NewEditFileTool creates a new EditFileTool instance.
func NewEditFileTool() *EditFileTool {
	return &EditFileTool{
		BaseTool: &tools.BaseTool{
			ToolName:        "edit_file",
			ToolDescription: toolEditFileDescription,
			Schema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"path": {
						"type": "string",
						"description": "Absolute or relative path to the file to edit. The file must already exist."
					},
					"old_string": {
						"type": "string",
						"description": "The exact substring to find in the file. Must appear exactly once; the operation fails if zero or multiple matches are found. Include sufficient surrounding context to guarantee uniqueness."
					},
					"new_string": {
						"type": "string",
						"description": "The replacement text that will replace old_string. Can be empty to delete the matched text."
					}
				},
				"required": ["path", "old_string", "new_string"]
			}`),
			Policy: tools.PolicyUserConfirm,
		},
	}
}

// EditFileInput represents the input parameters for edit_file.
type EditFileInput struct {
	Path      string `json:"path"`
	OldString string `json:"old_string"`
	NewString string `json:"new_string"`
}

// Judge uses workspace check for write operations.
func (t *EditFileTool) Judge(ctx context.Context, input json.RawMessage) (allowed bool, reason string) {
	var params EditFileInput
	if err := json.Unmarshal(input, &params); err != nil {
		return false, ""
	}
	params.Path = resolvePath(ctx, params.Path)
	return judgeWriteInWorkspace(ctx, params.Path)
}

// Execute performs ACI-style find-and-replace in a file.
func (t *EditFileTool) Execute(ctx context.Context, input json.RawMessage) (tools.ToolResult, error) {
	var params EditFileInput
	if err := json.Unmarshal(input, &params); err != nil {
		return tools.ParseInputError(err)
	}

	if params.Path == "" {
		return tools.ToolResult{Content: "validation error: path is required", IsError: true}, nil
	}
	if params.OldString == "" {
		return tools.ToolResult{Content: "validation error: old_string is required and must not be empty", IsError: true}, nil
	}

	params.Path = resolvePath(ctx, params.Path)

	data, err := os.ReadFile(params.Path)
	if err != nil {
		return tools.ToolResult{Content: fmt.Sprintf("failed to read file: %v", err), IsError: true}, nil
	}

	content := string(data)
	count := strings.Count(content, params.OldString)

	if count == 0 {
		return tools.ToolResult{Content: "old_string not found in file", IsError: true}, nil
	}

	if count > 1 {
		return tools.ToolResult{Content: fmt.Sprintf("old_string is not unique, found %d occurrences", count), IsError: true}, nil
	}

	newContent := strings.Replace(content, params.OldString, params.NewString, 1)

	if err := os.WriteFile(params.Path, []byte(newContent), 0o644); err != nil {
		return tools.ToolResult{Content: fmt.Sprintf("failed to write file: %v", err), IsError: true}, nil
	}

	return tools.ToolResult{Content: "successfully edited file", IsError: false}, nil
}
