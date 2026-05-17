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

const toolGlobDescription = `Find files and directories by name using glob patterns. Supports ** for recursive directory matching (e.g. **/*.go, src/**/*.ts, **/*.py, **/*.cs, **/*.java, **/*.php). Use this when you need to locate files by extension, name pattern, or directory structure. Respects .gitignore rules automatically. Returns up to 200 results by default.`

// GlobTool finds files and directories matching doublestar glob patterns.
type GlobTool struct {
	*tools.BaseTool
	limits GlobLimits
}

// NewGlobTool creates a new GlobTool instance with default limits.
func NewGlobTool() *GlobTool {
	return NewGlobToolWithLimits(DefaultGlobLimits())
}

// NewGlobToolWithLimits creates a new GlobTool instance with specified limits.
func NewGlobToolWithLimits(limits GlobLimits) *GlobTool {
	return &GlobTool{BaseTool: &tools.BaseTool{
		ToolName:        "glob",
		ToolDescription: toolGlobDescription,
		Schema: json.RawMessage(`{
		"type": "object",
		"properties": {
			"pattern": {
				"type": "string",
				"description": "Glob pattern to match against file paths, e.g. **/*.java, src/**/*.ts, **/*.cs, *.json"
			},
			"path": {
				"type": "string",
				"description": "Base directory to search from. Defaults to the project workspace when omitted."
			},
			"type": {
				"type": "string",
				"enum": ["files", "dirs", "all"],
				"description": "Filter results: \"files\" (default), \"dirs\", or \"all\""
			},
			"max_results": {
				"type": "integer",
				"description": "Maximum number of results to return. Default: 200."
			}
		},
		"required": ["pattern"]
	}`),
		Policy: tools.PolicyAlwaysAllow,
	},
		limits: limits,
	}
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

// Judge checks whether the glob targets a path within the workspace.
// Paths outside workspace require user confirmation.
func (t *GlobTool) Judge(ctx context.Context, input json.RawMessage) (allowed bool, reason string) {
	return judgeReadInWorkspace(ctx, input)
}

// Execute runs the glob pattern search and returns matching file paths.
func (t *GlobTool) Execute(ctx context.Context, input json.RawMessage) (tools.ToolResult, error) {
	var params GlobInput
	if err := json.Unmarshal(input, &params); err != nil {
		return tools.ParseInputError(err)
	}

	if params.Pattern == "" {
		return tools.ToolResult{Content: "validation error: pattern is required", IsError: true}, nil
	}

	if params.Path == "" {
		params.Path = tools.WorkspacePathFrom(ctx)
		if params.Path == "" {
			return tools.ToolResult{Content: "path is required when no workspace is available", IsError: true}, nil
		}
	} else {
		params.Path = resolvePath(ctx, params.Path)
	}

	// Apply defaults
	if params.Type == "" {
		params.Type = "files"
	}
	if params.MaxResults <= 0 {
		params.MaxResults = t.limits.MaxResults
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
