package builtins

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/localrivet/goripgrep"
	"github.com/user/agent/sdk/tools"
)

const toolRipgrepDescription = `Search file contents using regex or literal patterns. Returns matches in "file:line: content" format with optional surrounding context lines. Automatically respects .gitignore rules and skips binary files. Use this when you need to find code patterns, function definitions, or text within files. Returns up to 200 matches by default. For finding files by name or path pattern, use glob instead.`

// RipgrepTool searches file contents using regex patterns via goripgrep.
type RipgrepTool struct {
	*tools.BaseTool
	limits RipgrepLimits
}

// NewRipgrepTool creates a new RipgrepTool instance with default limits.
func NewRipgrepTool() *RipgrepTool {
	return NewRipgrepToolWithLimits(DefaultRipgrepLimits())
}

// NewRipgrepToolWithLimits creates a new RipgrepTool instance with specified limits.
func NewRipgrepToolWithLimits(limits RipgrepLimits) *RipgrepTool {
	return &RipgrepTool{BaseTool: &tools.BaseTool{
		ToolName:        "ripgrep",
		ToolDescription: toolRipgrepDescription,
		Schema: json.RawMessage(`{
		"type": "object",
		"properties": {
			"pattern": {
				"type": "string",
				"description": "Regex or literal search pattern, e.g. \"func main\" or \"TODO.*fix\""
			},
			"path": {
				"type": "string",
				"description": "Directory to search recursively. Defaults to the project workspace when omitted."
			},
			"file_pattern": {
				"type": "string",
				"description": "Glob filter to restrict which files are searched, e.g. *.php, *.java, *.ts"
			},
			"ignore_case": {
				"type": "boolean",
				"description": "Perform case-insensitive matching. Default: false."
			},
			"context_lines": {
				"type": "integer",
				"description": "Number of lines to show before and after each match. Default: 0."
			},
			"max_results": {
				"type": "integer",
				"description": "Maximum number of matches to return. Default: 200."
			},
			"include_hidden": {
				"type": "boolean",
				"description": "Include hidden files and directories in the search. Default: false."
			}
		},
		"required": ["pattern"]
	}`),
		Policy: tools.PolicyAlwaysAllow,
	},
		limits: limits,
	}
}

// RipgrepInput represents the input parameters for ripgrep search.
type RipgrepInput struct {
	Pattern       string `json:"pattern"`
	Path          string `json:"path"`
	FilePattern   string `json:"file_pattern"`
	IgnoreCase    bool   `json:"ignore_case"`
	ContextLines  int    `json:"context_lines"`
	MaxResults    int    `json:"max_results"`
	IncludeHidden bool   `json:"include_hidden"`
}

// Execute performs the ripgrep search and returns formatted results.
func (t *RipgrepTool) Execute(ctx context.Context, input json.RawMessage) (tools.ToolResult, error) {
	var params RipgrepInput
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
	if params.MaxResults <= 0 {
		params.MaxResults = t.limits.MaxResults
	}

	// Build goripgrep options.
	// NOTE: We intentionally omit WithMaxResults here and truncate results
	// ourselves after the call. The library's MaxResults triggers an early
	// break in performSearch that returns before workers finish, causing a
	// data race on SearchStats fields.
	opts := []goripgrep.Option{
		goripgrep.WithRecursive(true),
		goripgrep.WithGitignore(true),
		goripgrep.WithTimeout(t.limits.Timeout),
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
		var line string
		if m.Column > 0 {
			line = fmt.Sprintf("%s:%d:%d: %s", m.File, m.Line, m.Column, m.Content)
		} else {
			line = fmt.Sprintf("%s:%d: %s", m.File, m.Line, m.Content)
		}
		// Truncate long lines to prevent token waste
		if len(line) > t.limits.MaxLineLength {
			line = line[:t.limits.MaxLineLength] + "...(line truncated)"
		}
		sb.WriteString(line)
		sb.WriteByte('\n')
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
