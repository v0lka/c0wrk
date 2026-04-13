package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/user/agent/core/tools"
)

// Compile-time check that Tool implements tools.ToolJudger.
var _ tools.ToolJudger = (*Tool)(nil)

// Tool wraps an MCP server tool as a tools.Tool interface implementation.
type Tool struct {
	server      *Server
	name        string
	description string
	inputSchema json.RawMessage
}

// NewTool creates a new Tool from the given server and tool info.
func NewTool(server *Server, info ToolInfo) *Tool {
	return &Tool{
		server:      server,
		name:        info.Name,
		description: info.Description,
		inputSchema: info.InputSchema,
	}
}

// Name returns the tool's name.
func (t *Tool) Name() string {
	return t.name
}

// Description returns the tool's description.
func (t *Tool) Description() string {
	return t.description
}

// InputSchema returns the tool's JSON schema for input parameters.
func (t *Tool) InputSchema() json.RawMessage {
	return t.inputSchema
}

// DefaultPolicy returns PolicyUserConfirm as a conservative default for MCP tools.
func (t *Tool) DefaultPolicy() tools.ToolPolicy {
	return tools.PolicyUserConfirm
}

// Execute calls the MCP server's tools/call endpoint with the provided input.
func (t *Tool) Execute(ctx context.Context, input json.RawMessage) (tools.ToolResult, error) {
	// Parse input JSON into a map for the MCP call
	var arguments map[string]any
	if len(input) > 0 {
		if err := json.Unmarshal(input, &arguments); err != nil {
			return tools.ParseInputError(err)
		}
	}

	// Call the tool on the MCP server
	result, err := t.server.CallTool(ctx, t.name, arguments)
	if err != nil {
		return tools.ToolResult{
			Content: fmt.Sprintf("MCP tool call failed: %v", err),
			IsError: true,
		}, nil
	}

	// Convert MCP result to ToolResult
	return convertMCPResult(result), nil
}

// convertMCPResult converts an MCP CallToolResult to our ToolResult format.
func convertMCPResult(result *mcp.CallToolResult) tools.ToolResult {
	if result == nil {
		return tools.ToolResult{
			Content: "",
			IsError: false,
		}
	}

	// Extract text content from the result
	var contentParts []string
	for _, content := range result.Content {
		text := extractTextFromContent(content)
		if text != "" {
			contentParts = append(contentParts, text)
		}
	}

	content := strings.Join(contentParts, "\n")

	// If there's structured content and no text content, try to marshal it
	if content == "" && result.StructuredContent != nil {
		jsonBytes, err := json.Marshal(result.StructuredContent)
		if err != nil {
			content = fmt.Sprintf("(failed to serialize structured content: %v)", err)
		} else {
			content = string(jsonBytes)
		}
	}

	return tools.ToolResult{
		Content: content,
		IsError: result.IsError,
	}
}

// extractTextFromContent extracts text from an MCP Content interface.
func extractTextFromContent(content mcp.Content) string {
	// Try to extract text using the mcp helper
	text := mcp.GetTextFromContent(content)
	if text != "" {
		return text
	}

	// Fall back to type assertion for TextContent
	if tc, ok := mcp.AsTextContent(content); ok {
		return tc.Text
	}

	// Try to marshal as JSON for other content types
	jsonBytes, err := json.Marshal(content)
	if err != nil {
		return fmt.Sprintf("(failed to serialize content: %v)", err)
	}
	return string(jsonBytes)
}

// ServerName returns the name of the MCP server this tool belongs to.
func (t *Tool) ServerName() string {
	return t.server.Name()
}

// Judge implements tools.ToolJudger for MCP tools.
// MCP tools are remote and opaque, so we always defer to the LLM Judge.
func (t *Tool) Judge(_ context.Context, _ json.RawMessage) (allowed bool, reason string) {
	return false, "" // defer to LLM Judge
}
