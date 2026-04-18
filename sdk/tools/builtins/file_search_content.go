package builtins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/user/agent/sdk/tools"
)

const toolSearchContentDescription = `Recursively searches all files under the given directory for lines matching a regular expression. Returns matching lines with their file paths and line numbers. Results are capped at a configured maximum. For faster, more feature-rich content search (with .gitignore support, context lines, etc.), prefer ripgrep instead.`

// SearchContentTool searches file contents for regex matches.
type SearchContentTool struct {
	*tools.BaseTool
	limits FileLimits
}

// NewSearchContentTool creates a new SearchContentTool instance with default limits.
func NewSearchContentTool() *SearchContentTool {
	return NewSearchContentToolWithLimits(DefaultFileLimits())
}

// NewSearchContentToolWithLimits creates a new SearchContentTool instance with specified limits.
func NewSearchContentToolWithLimits(limits FileLimits) *SearchContentTool {
	return &SearchContentTool{
		BaseTool: &tools.BaseTool{
			ToolName:        "search_content",
			ToolDescription: toolSearchContentDescription,
			Schema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"path": {
						"type": "string",
						"description": "Absolute or relative path to the root directory to search from."
					},
					"regex": {
						"type": "string",
						"description": "Go-compatible regular expression pattern to match against each line of each file."
					}
				},
				"required": ["path", "regex"]
			}`),
			Policy: tools.PolicyAlwaysAllow,
		},
		limits: limits,
	}
}

// SearchContentInput represents the input parameters for search_content.
type SearchContentInput struct {
	Path  string `json:"path"`
	Regex string `json:"regex"`
}

// Judge always allows search_content (read-only operation).
func (t *SearchContentTool) Judge(ctx context.Context, input json.RawMessage) (allowed bool, reason string) {
	return true, "read-only file operation"
}

// Execute searches file contents for regex matches.
func (t *SearchContentTool) Execute(ctx context.Context, input json.RawMessage) (tools.ToolResult, error) {
	var params SearchContentInput
	if err := json.Unmarshal(input, &params); err != nil {
		return tools.ParseInputError(err)
	}

	params.Path = resolvePath(ctx, params.Path)

	re, err := regexp.Compile(params.Regex)
	if err != nil {
		return tools.ToolResult{Content: fmt.Sprintf("invalid regex: %v", err), IsError: true}, nil
	}

	maxMatches := t.limits.FileSearchMatches
	var results []string

	err = filepath.Walk(params.Path, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil //nolint:nilerr // continue walking on individual file errors
		}

		if len(results) >= maxMatches {
			return filepath.SkipAll
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return nil //nolint:nilerr // continue walking on individual file errors
		}

		lines := strings.Split(string(data), "\n")
		for lineNum, line := range lines {
			if re.MatchString(line) {
				results = append(results, fmt.Sprintf("%s:%d: %s", path, lineNum+1, line))
				if len(results) >= maxMatches {
					return filepath.SkipAll
				}
			}
		}

		return nil
	})

	if err != nil && !errors.Is(err, filepath.SkipAll) {
		return tools.ToolResult{Content: fmt.Sprintf("failed to search content: %v", err), IsError: true}, nil
	}

	if len(results) == 0 {
		return tools.ToolResult{Content: "no matches found", IsError: false}, nil
	}

	content := strings.Join(results, "\n")
	if len(results) >= maxMatches {
		content += fmt.Sprintf("\n(results limited to %d matches)", maxMatches)
	}

	return tools.ToolResult{Content: content, IsError: false}, nil
}
