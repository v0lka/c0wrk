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
type RipgrepTool struct {
	*tools.BaseTool
}

// NewRipgrepTool creates a new RipgrepTool instance.
func NewRipgrepTool() *RipgrepTool {
	return &RipgrepTool{BaseTool: &tools.BaseTool{
		ToolName:        "ripgrep",
		ToolDescription: toolRipgrepDescription,
		Schema: json.RawMessage(`{
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
	}`),
		Policy: tools.PolicyAlwaysAllow,
	}}
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

// Execute performs the ripgrep search and returns formatted results.
func (t *RipgrepTool) Execute(_ context.Context, input json.RawMessage) (tools.ToolResult, error) {
	var params ripgrepInput
	if err := json.Unmarshal(input, &params); err != nil {
		return tools.ParseInputError(err)
	}

	// Apply defaults
	if params.MaxResults <= 0 {
		params.MaxResults = 200
	}

	// Build goripgrep options.
	// NOTE: We intentionally omit WithMaxResults here and truncate results
	// ourselves after the call. The library's MaxResults triggers an early
	// break in performSearch that returns before workers finish, causing a
	// data race on SearchStats fields.
	opts := []goripgrep.Option{
		goripgrep.WithRecursive(true),
		goripgrep.WithGitignore(true),
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

	// WithWorkers(1) avoids an upstream data race in goripgrep's
	// SearchStats fields (BytesScanned, FilesScanned) that are updated
	// by concurrent workers without synchronisation.
	opts = append(opts, goripgrep.WithWorkers(1))

	results, err := goripgrep.Find(params.Pattern, params.Path, opts...)
	if err != nil {
		return tools.ToolResult{Content: fmt.Sprintf("search error: %v", err), IsError: true}, nil
	}

	if !results.HasMatches() {
		return tools.ToolResult{Content: "no matches found"}, nil
	}

	// Truncate to MaxResults on our side (see comment above about why
	// we avoid the library's WithMaxResults).
	matches := results.Matches
	if len(matches) > params.MaxResults {
		matches = matches[:params.MaxResults]
	}

	// Format results
	var sb strings.Builder
	for _, m := range matches {
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
		len(matches),
		len(results.Files()),
		stats.BytesScanned,
		stats.Duration.Milliseconds(),
	)

	return tools.ToolResult{Content: sb.String()}, nil
}
