package builtins

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/v0lka/c0wrk/sdk/agent"
	"github.com/v0lka/c0wrk/sdk/tools"
)

const toolResultReadDescription = `Read a previously cached tool result in fragments. Provide the hash from a truncation nudge (e.g. "[This output was truncated ... hash: abc123 ...]") along with a 1-based start_line and the number of lines to read. Use this to retrieve more content from a truncated tool output without re-executing the original tool.`

const defaultResultReadLines = 500

// ToolResultReadTool reads fragments of cached tool results by hash.
type ToolResultReadTool struct {
	*tools.BaseTool
}

// NewToolResultReadTool creates a new ToolResultReadTool instance.
func NewToolResultReadTool() *ToolResultReadTool {
	return &ToolResultReadTool{BaseTool: &tools.BaseTool{
		ToolName:        "tool_result_read",
		ToolDescription: toolResultReadDescription,
		Schema: json.RawMessage(`{
		"type": "object",
		"properties": {
			"hash": {
				"type": "string",
				"description": "The SHA256 hash from the truncation nudge message, e.g. \"abc123def456\""
			},
			"start_line": {
				"type": "integer",
				"description": "1-based line number to start reading from. Defaults to 1."
			},
			"num_lines": {
				"type": "integer",
				"description": "Maximum number of lines to return. Defaults to 500."
			}
		},
		"required": ["hash"]
	}`),
		Policy: tools.PolicyAlwaysAllow,
	}}
}

// toolResultReadInput represents the input parameters for tool_result_read.
type toolResultReadInput struct {
	Hash      string `json:"hash"`
	StartLine int    `json:"start_line"`
	NumLines  int    `json:"num_lines"`
}

// Execute retrieves the cached result fragment.
func (t *ToolResultReadTool) Execute(ctx context.Context, input json.RawMessage) (tools.ToolResult, error) {
	var params toolResultReadInput
	if err := json.Unmarshal(input, &params); err != nil {
		return tools.ParseInputError(err)
	}

	if params.Hash == "" {
		return tools.ToolResult{Content: "validation error: hash is required", IsError: true}, nil
	}

	cache := agent.ToolResultCacheFromContext(ctx)
	if cache == nil {
		return tools.ErrorResult("Tool result cache not available"), nil
	}

	entry, ok := cache.Get(params.Hash)
	if !ok {
		return tools.ErrorResult("No cached result found for hash: %s. The cache entry may have expired or the hash is incorrect.", params.Hash), nil
	}

	// Coherence check for file-based tools.
	if entry.FilePath != "" {
		valid, reason := cache.CheckCoherence(params.Hash)
		if !valid {
			return tools.ErrorResult("Cached result for '%s' is stale: %s. Re-run the original tool to obtain fresh data.", entry.ToolName, reason), nil
		}
	}

	// Apply defaults.
	if params.StartLine <= 0 {
		params.StartLine = 1
	}
	if params.NumLines <= 0 {
		params.NumLines = defaultResultReadLines
	}

	// Enforce num_lines upper bound from per-tool truncation config.
	// The LLM is told in the nudge to keep num_lines <= MaxLines, but we enforce it server-side.
	if perToolCfg := agent.PerToolTruncationFromContext(ctx); perToolCfg != nil {
		if cfg, ok := perToolCfg[entry.ToolName]; ok && cfg.MaxLines > 0 {
			if params.NumLines > cfg.MaxLines {
				params.NumLines = cfg.MaxLines
			}
		}
	}
	// Fallback hard cap if no per-tool config is available.
	if params.NumLines > defaultResultReadLines {
		params.NumLines = defaultResultReadLines
	}

	allLines := strings.Split(entry.Content, "\n")
	totalLines := len(allLines)

	// Clamp start_line.
	if params.StartLine > totalLines {
		params.StartLine = totalLines
	}

	endLine := params.StartLine + params.NumLines - 1
	if endLine > totalLines {
		endLine = totalLines
	}

	selectedLines := allLines[params.StartLine-1 : endLine]
	fragment := strings.Join(selectedLines, "\n")

	var sb strings.Builder
	fmt.Fprintf(&sb, "[Lines %d-%d of %d from cached %s result | hash: %s]\n",
		params.StartLine, endLine, totalLines, entry.ToolName, params.Hash)
	sb.WriteString(fragment)

	// Add continuation nudge if more lines are available.
	if endLine < totalLines {
		fmt.Fprintf(&sb, "\n\n[Use tool_result_read(hash=\"%s\", start_line=%d, num_lines=%d) to continue reading]",
			params.Hash, endLine+1, params.NumLines)
	}

	return tools.ToolResult{Content: sb.String()}, nil
}
