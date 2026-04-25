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
			Policy: tools.PolicyAlwaysAllow,
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

// Judge always allows read_file (read-only operation).
func (t *ReadFileTool) Judge(ctx context.Context, input json.RawMessage) (allowed bool, reason string) {
	return true, "read-only file operation"
}

// Execute reads and returns the content of a file with pagination support.
func (t *ReadFileTool) Execute(ctx context.Context, input json.RawMessage) (tools.ToolResult, error) {
	var params ReadFileInput
	if err := json.Unmarshal(input, &params); err != nil {
		return tools.ParseInputError(err)
	}

	params.Path = resolvePath(ctx, params.Path)

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
		endLine = startLine + t.limits.ReadDefaultLines - 1
	}

	// Clamp to bounds
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

	// Truncate individual lines that exceed max length
	for i, line := range selectedLines {
		if len(line) > t.limits.ReadMaxLineLength {
			selectedLines[i] = fmt.Sprintf("%s...(line truncated, original: %d chars)", line[:t.limits.ReadMaxLineLength], len(line))
		}
	}

	// Join lines
	content := strings.Join(selectedLines, "\n")

	// Check byte limit
	if len(content) > t.limits.ReadMaxBytes {
		content = content[:t.limits.ReadMaxBytes] + "\n[Byte limit reached]"
	}

	// Build metadata header
	filename := filepath.Base(params.Path)
	header := fmt.Sprintf("[File: %s | Lines %d-%d of %d | %d bytes]\n", filename, startLine, endLine, totalLines, len(content))

	// Add continuation hint if there are more lines
	if endLine < totalLines {
		content = header + content + fmt.Sprintf("\n[Use start_line=%d to continue reading]", endLine+1)
	} else {
		content = header + content
	}

	return tools.ToolResult{Content: content, IsError: false}, nil
}
