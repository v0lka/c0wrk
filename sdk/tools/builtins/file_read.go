package builtins

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/v0lka/c0wrk/sdk/tools"
)

const toolReadFileDescription = `Reads and returns the contents of a file at the given path. Supports pagination via optional line range parameters. Output includes a metadata header showing the file name, returned line range, and total line count. When more content remains beyond the returned range, a continuation hint is appended.`

// ReadFileTool reads file contents with pagination support.
type ReadFileTool struct {
	*tools.BaseTool
	limits FileLimits
}

// NewReadFileTool creates a new ReadFileTool instance with default limits.
func NewReadFileTool() *ReadFileTool {
	return NewReadFileToolWithLimits(DefaultFileLimits())
}

// NewReadFileToolWithLimits creates a new ReadFileTool instance with specified limits.
func NewReadFileToolWithLimits(limits FileLimits) *ReadFileTool {
	return &ReadFileTool{
		BaseTool: &tools.BaseTool{
			ToolName:        "read_file",
			ToolDescription: toolReadFileDescription,
			Schema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"path": {
						"type": "string",
						"description": "Absolute or relative path to the file to read."
					},
					"start_line": {
						"type": "integer",
						"description": "1-based line number to start reading from. Defaults to the beginning of the file."
					},
					"end_line": {
						"type": "integer",
						"description": "1-based line number to stop reading at (inclusive). Defaults to a fixed window starting from start_line. Values beyond the file length are clamped automatically. Individual lines exceeding the per-line character limit are truncated with a notice."
					}
				},
				"required": ["path"]
			}`),
			Policy:    tools.PolicyAlwaysAllow,
			Untrusted: true,
		},
		limits: limits,
	}
}

// ReadFileInput represents the input parameters for read_file.
type ReadFileInput struct {
	Path      string `json:"path"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
}

// Judge checks whether the read targets a path within the workspace.
// Files outside workspace require user confirmation.
func (t *ReadFileTool) Judge(ctx context.Context, input json.RawMessage) (allowed bool, reason string) {
	return judgeReadInWorkspace(ctx, input)
}

// Execute reads and returns the content of a file with pagination support.
func (t *ReadFileTool) Execute(ctx context.Context, input json.RawMessage) (tools.ToolResult, error) {
	var params ReadFileInput
	if err := json.Unmarshal(input, &params); err != nil {
		return tools.ParseInputError(err)
	}

	if params.Path == "" {
		return tools.ToolResult{Content: "validation error: path is required", IsError: true}, nil
	}
	if params.StartLine < 0 {
		return tools.ToolResult{Content: fmt.Sprintf("validation error: start_line must be >= 1, got %d", params.StartLine), IsError: true}, nil
	}
	if params.EndLine < 0 {
		return tools.ToolResult{Content: fmt.Sprintf("validation error: end_line must be >= 1, got %d", params.EndLine), IsError: true}, nil
	}
	if params.StartLine > 0 && params.EndLine > 0 && params.StartLine > params.EndLine {
		return tools.ToolResult{Content: fmt.Sprintf("validation error: start_line (%d) must not exceed end_line (%d)", params.StartLine, params.EndLine), IsError: true}, nil
	}

	params.Path = resolvePath(ctx, params.Path)

	// Coherence check: detect if file was modified by another session since last read.
	var coherenceWarning string
	if checker := tools.CoherenceFrom(ctx); checker != nil {
		checker.Lock(params.Path)
		if conflict := checker.CheckRead(ctx, params.Path); conflict != nil {
			coherenceWarning = formatReadConflict(conflict)
		}
		checker.Unlock(params.Path)
	}

	data, err := os.ReadFile(params.Path)
	if err != nil {
		return tools.ToolResult{Content: fmt.Sprintf("failed to read file: %v", err), IsError: true}, nil
	}

	// Split content by newlines
	allLines := strings.Split(string(data), "\n")
	totalLines := len(allLines)

	// Apply defaults
	startLine := params.StartLine
	endLine := params.EndLine
	if startLine <= 0 {
		startLine = 1
	}
	if endLine <= 0 {
		endLine = totalLines // return entire file; centralized truncation handles output size
	}

	// Clamp to file bounds
	if startLine > totalLines {
		startLine = totalLines
	}
	if endLine > totalLines {
		endLine = totalLines
	}
	if startLine < 1 {
		startLine = 1
	}

	// Extract the requested range (convert 1-based to 0-based indexing)
	selectedLines := allLines[startLine-1 : endLine]

	// Join lines
	content := strings.Join(selectedLines, "\n")

	// Build metadata header
	filename := filepath.Base(params.Path)
	header := fmt.Sprintf("[File: %s | Lines %d-%d of %d | %d bytes]\n", filename, startLine, endLine, totalLines, len(content))

	// Add continuation hint if there are more lines
	if endLine < totalLines {
		content = header + content + fmt.Sprintf("\n[Use start_line=%d to continue reading]", endLine+1)
	} else {
		content = header + content
	}

	if coherenceWarning != "" {
		content = coherenceWarning + "\n" + content
	}

	return tools.ToolResult{Content: content, IsError: false}, nil
}
