package builtins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"

	doublestar "github.com/bmatcuk/doublestar/v4"
	"github.com/user/agent/sdk/tools"
)

const toolGlobDescription = "Find files and directories matching glob patterns. Supports ** for recursive matching (e.g. **/*.go, src/**/*.ts)."

// GlobTool finds files and directories matching doublestar glob patterns.
type GlobTool struct {
	*tools.BaseTool
}

// NewGlobTool creates a new GlobTool instance.
func NewGlobTool() *GlobTool {
	return &GlobTool{BaseTool: &tools.BaseTool{
		ToolName:        "glob",
		ToolDescription: toolGlobDescription,
		Schema: json.RawMessage(`{
		"type": "object",
		"properties": {
			"pattern": {
				"type": "string",
				"description": "Glob pattern to match, e.g. **/*.go, src/**/*.ts"
			},
			"path": {
				"type": "string",
				"description": "Base directory to search from"
			},
			"type": {
				"type": "string",
				"enum": ["files", "dirs", "all"],
				"description": "Filter results by type. Default: files"
			},
			"max_results": {
				"type": "integer",
				"description": "Maximum number of results to return. Default: 1000"
			}
		},
		"required": ["pattern", "path"]
	}`),
		Policy: tools.PolicyAlwaysAllow,
	}}
}

// GlobInput represents the input parameters for glob search.
type GlobInput struct {
	Pattern    string `json:"pattern"`
	Path       string `json:"path"`
	Type       string `json:"type"`
	MaxResults int    `json:"max_results"`
}

// errMaxResults is a sentinel error used to stop walking when max results is reached.
var errMaxResults = errors.New("max results reached")

// Execute runs the glob pattern search and returns matching file paths.
func (t *GlobTool) Execute(_ context.Context, input json.RawMessage) (tools.ToolResult, error) {
	var params GlobInput
	if err := json.Unmarshal(input, &params); err != nil {
		return tools.ParseInputError(err)
	}

	// Apply defaults
	if params.Type == "" {
		params.Type = "files"
	}
	if params.MaxResults <= 0 {
		params.MaxResults = 1000
	}

	// Validate path exists and is a directory
	info, err := os.Stat(params.Path)
	if err != nil {
		return tools.ToolResult{Content: fmt.Sprintf("path error: %v", err), IsError: true}, nil
	}
	if !info.IsDir() {
		return tools.ToolResult{Content: "path is not a directory: " + params.Path, IsError: true}, nil
	}

	var results []string
	truncated := false

	walkErr := doublestar.GlobWalk(os.DirFS(params.Path), params.Pattern, func(p string, d fs.DirEntry) error {
		// Filter by type
		switch params.Type {
		case "files":
			if d.IsDir() {
				return nil
			}
		case "dirs":
			if !d.IsDir() {
				return nil
			}
			// "all": no filtering
		}

		results = append(results, p)

		if len(results) >= params.MaxResults {
			truncated = true
			return errMaxResults
		}
		return nil
	})

	if walkErr != nil && !errors.Is(walkErr, errMaxResults) {
		return tools.ToolResult{Content: fmt.Sprintf("glob error: %v", walkErr), IsError: true}, nil
	}

	if len(results) == 0 {
		return tools.ToolResult{Content: "no matching files found"}, nil
	}

	output := strings.Join(results, "\n")
	if truncated {
		output += fmt.Sprintf("\n(results limited to %d)", params.MaxResults)
	}

	return tools.ToolResult{Content: output}, nil
}
