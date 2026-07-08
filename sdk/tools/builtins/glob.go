package builtins

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"strings"

	doublestar "github.com/bmatcuk/doublestar/v4"
	"github.com/v0lka/sp4rk/tools"
)

const toolGlobDescription = `Find files and directories by name using glob patterns. Supports ** for recursive directory matching (e.g. **/*.go, src/**/*.ts, **/*.py, **/*.cs, **/*.java, **/*.php). Use this when you need to locate files by extension, name pattern, or directory structure.`

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
			}
		},
		"required": ["pattern"]
	}`),
		Policy:    tools.PolicyAlwaysAllow,
		Untrusted: true,
	}}
}

// GlobInput represents the input parameters for glob search.
type GlobInput struct {
	Pattern string `json:"pattern"`
	Path    string `json:"path"`
	Type    string `json:"type"`
}

// Judge checks whether the glob targets a path inside the session roots.
// Paths outside workspace/temp require user confirmation.
func (t *GlobTool) Judge(ctx context.Context, input json.RawMessage) (allowed bool, reason string) {
	return judgeReadInSessionRoots(ctx, input)
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
		if err := validateResolvedPath(params.Path); err != nil {
			return tools.ToolResult{Content: err.Error(), IsError: true}, nil //nolint:nilerr // error embedded in ToolResult by design
		}
	}

	// Apply defaults
	if params.Type == "" {
		params.Type = "files"
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
		return nil
	})

	if walkErr != nil {
		return tools.ToolResult{Content: fmt.Sprintf("glob error: %v", walkErr), IsError: true}, nil
	}

	if len(results) == 0 {
		return tools.ToolResult{Content: "no matching files found"}, nil
	}

	output := strings.Join(results, "\n")

	return tools.ToolResult{Content: output}, nil
}
