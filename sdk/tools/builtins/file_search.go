package builtins

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/user/agent/sdk/tools"
)

const toolSearchFilesDescription = `Recursively searches a directory tree for files whose names match a glob pattern. Returns the full paths of all matching files.`

// SearchFilesTool searches for files by name pattern.
type SearchFilesTool struct {
	*tools.BaseTool
}

// NewSearchFilesTool creates a new SearchFilesTool instance.
func NewSearchFilesTool() *SearchFilesTool {
	return &SearchFilesTool{
		BaseTool: &tools.BaseTool{
			ToolName:        "search_files",
			ToolDescription: toolSearchFilesDescription,
			Schema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"path": {
						"type": "string",
						"description": "Absolute or relative path to the root directory to search from."
					},
					"pattern": {
						"type": "string",
						"description": "Glob pattern to match against file names (not full paths), e.g. *.go, *.ts, test_*.py."
					}
				},
				"required": ["path", "pattern"]
			}`),
			Policy: tools.PolicyAlwaysAllow,
		},
	}
}

// SearchFilesInput represents the input parameters for search_files.
type SearchFilesInput struct {
	Path    string `json:"path"`
	Pattern string `json:"pattern"`
}

// Judge checks whether the search targets a path within the workspace.
// Paths outside workspace require user confirmation.
func (t *SearchFilesTool) Judge(ctx context.Context, input json.RawMessage) (allowed bool, reason string) {
	return judgeReadInWorkspace(ctx, input)
}

// Execute searches for files matching a glob pattern.
func (t *SearchFilesTool) Execute(ctx context.Context, input json.RawMessage) (tools.ToolResult, error) {
	var params SearchFilesInput
	if err := json.Unmarshal(input, &params); err != nil {
		return tools.ParseInputError(err)
	}

	if params.Path == "" {
		return tools.ToolResult{Content: "validation error: path is required", IsError: true}, nil
	}
	if params.Pattern == "" {
		return tools.ToolResult{Content: "validation error: pattern is required", IsError: true}, nil
	}

	params.Path = resolvePath(ctx, params.Path)

	var matches []string

	err := filepath.Walk(params.Path, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil //nolint:nilerr // continue walking on individual file errors
		}

		if info.IsDir() {
			return nil
		}

		matched, err := filepath.Match(params.Pattern, filepath.Base(path))
		if err != nil {
			return nil //nolint:nilerr // continue walking on individual file errors
		}

		if matched {
			matches = append(matches, path)
		}
		return nil
	})

	if err != nil {
		return tools.ToolResult{Content: fmt.Sprintf("failed to search files: %v", err), IsError: true}, nil
	}

	if len(matches) == 0 {
		return tools.ToolResult{Content: "no matching files found", IsError: false}, nil
	}

	return tools.ToolResult{Content: strings.Join(matches, "\n"), IsError: false}, nil
}
