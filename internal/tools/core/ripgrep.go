package core

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/localrivet/goripgrep"
	"github.com/user/agent/internal/tools"
)

const toolRipgrepDescription = "Search file contents using regex patterns with gitignore support, context lines, and concurrent processing."

// RipgrepTool searches file contents using regex patterns via goripgrep.
type RipgrepTool struct{}

// NewRipgrepTool creates a new RipgrepTool instance.
func NewRipgrepTool() *RipgrepTool {
	return &RipgrepTool{}
}

// ripgrepInput represents the input parameters for ripgrep search.
type ripgrepInput struct {
	Pattern       string `json:"pattern"`
	Path          string `json:"path"`
	FilePattern   string `json:"file_pattern"`
	IgnoreCase    bool   `json:"ignore_case"`
	ContextLines  int    `json:"context_lines"`
	MaxResults    int    `json:"max_results"`
	IncludeHidden bool   `json:"include_hidden"`
}

// Name returns the tool name.
func (t *RipgrepTool) Name() string {
	return "ripgrep"
}

// Description returns the tool description.
func (t *RipgrepTool) Description() string {
	return toolRipgrepDescription
}

// InputSchema returns the JSON schema for the tool input.
func (t *RipgrepTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"pattern": {
				"type": "string",
				"description": "Regex or literal search pattern"
			},
			"path": {
				"type": "string",
				"description": "Directory to search"
			},
			"file_pattern": {
				"type": "string",
				"description": "File glob filter, e.g. *.go"
			},
			"ignore_case": {
				"type": "boolean",
				"description": "Case-insensitive search"
			},
			"context_lines": {
				"type": "integer",
				"description": "Lines of context around matches (default 0)"
			},
			"max_results": {
				"type": "integer",
				"description": "Maximum matches to return (default 200)"
			},
			"include_hidden": {
				"type": "boolean",
				"description": "Include hidden files (default false)"
			}
		},
		"required": ["pattern", "path"]
	}`)
}

// DefaultPolicy returns PolicyAlwaysAllow because ripgrep is a read-only operation.
func (t *RipgrepTool) DefaultPolicy() tools.ToolPolicy {
	return tools.PolicyAlwaysAllow
}

// Execute performs the ripgrep search and returns formatted results.
func (t *RipgrepTool) Execute(_ context.Context, input json.RawMessage) (tools.ToolResult, error) {
	var params ripgrepInput
	if err := json.Unmarshal(input, &params); err != nil {
		return tools.ToolResult{Content: fmt.Sprintf("failed to parse input: %v", err), IsError: true}, nil
	}

	// Apply defaults
	if params.MaxResults <= 0 {
		params.MaxResults = 200
	}

	// Build goripgrep options
	opts := []goripgrep.Option{
		goripgrep.WithRecursive(true),
		goripgrep.WithGitignore(true),
		goripgrep.WithMaxResults(params.MaxResults),
		goripgrep.WithTimeout(60 * time.Second),
	}

	if params.IgnoreCase {
		opts = append(opts, goripgrep.WithIgnoreCase())
	}
	if params.ContextLines > 0 {
		opts = append(opts, goripgrep.WithContextLines(params.ContextLines))
	}
	if params.FilePattern != "" {
		opts = append(opts, goripgrep.WithFilePattern(params.FilePattern))
	}
	if params.IncludeHidden {
		opts = append(opts, goripgrep.WithHidden())
	}

	results, err := goripgrep.Find(params.Pattern, params.Path, opts...)
	if err != nil {
		return tools.ToolResult{Content: fmt.Sprintf("search error: %v", err), IsError: true}, nil
	}

	if !results.HasMatches() {
		return tools.ToolResult{Content: "no matches found"}, nil
	}

	// Format results
	var sb strings.Builder
	for _, m := range results.Matches {
		if m.Column > 0 {
			fmt.Fprintf(&sb, "%s:%d:%d: %s\n", m.File, m.Line, m.Column, m.Content)
		} else {
			fmt.Fprintf(&sb, "%s:%d: %s\n", m.File, m.Line, m.Content)
		}
		if len(m.Context) > 0 {
			for _, cl := range m.Context {
				fmt.Fprintf(&sb, "  %s\n", cl)
			}
		}
	}

	// Append stats
	stats := results.Stats
	fmt.Fprintf(&sb, "\nFound %d matches in %d files (scanned %d bytes in %dms)",
		len(results.Matches),
		len(results.Files()),
		stats.BytesScanned,
		stats.Duration.Milliseconds(),
	)

	return tools.ToolResult{Content: sb.String()}, nil
}
