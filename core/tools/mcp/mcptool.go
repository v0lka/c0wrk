package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/v0lka/c0wrk/core/tools"
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

// SchemaSanitizer transforms an input schema (JSON Schema) before it is exposed to
// the LLM. It receives the server source name and the original schema, and returns
// the sanitized schema. Return the input unchanged to pass through.
type SchemaSanitizer func(source string, schema json.RawMessage) json.RawMessage

// stripParamsFromSchema removes the named properties from a JSON Schema's
// "properties" object and (if present) from "required". This is used to hide
// parameters that are auto-injected at execution time and should not be visible
// to the LLM.
func StripParamsFromSchema(schema json.RawMessage, paramsToRemove map[string]bool) json.RawMessage {
	if len(paramsToRemove) == 0 {
		return schema
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(schema, &raw); err != nil {
		return schema // not a valid object schema, pass through
	}

	propsRaw, ok := raw["properties"]
	if !ok {
		return schema // no properties, nothing to strip
	}

	var props map[string]json.RawMessage
	if err := json.Unmarshal(propsRaw, &props); err != nil {
		return schema
	}

	changed := false
	for p := range paramsToRemove {
		if _, exists := props[p]; exists {
			delete(props, p)
			changed = true
		}
	}
	if !changed {
		return schema
	}

	propsRaw, _ = json.Marshal(props)
	raw["properties"] = propsRaw

	// Also clean up the "required" array if present.
	if reqRaw, ok := raw["required"]; ok {
		var req []string
		if err := json.Unmarshal(reqRaw, &req); err == nil {
			filtered := make([]string, 0, len(req))
			for _, r := range req {
				if !paramsToRemove[r] {
					filtered = append(filtered, r)
				}
			}
			if len(filtered) != len(req) {
				filteredRaw, _ := json.Marshal(filtered)
				raw["required"] = filteredRaw
			}
		}
	}

	result, err := json.Marshal(raw)
	if err != nil {
		return schema
	}
	return result
}

// NewTool creates a new Tool from the given server and tool info.
// Optional SchemaSanitizers are applied in order to transform the input schema
// before it is stored (e.g., to strip auto-injected parameters).
func NewTool(server *Server, info ToolInfo, sanitizers ...SchemaSanitizer) *Tool {
	schema := info.InputSchema
	for _, s := range sanitizers {
		if s != nil {
			schema = s(server.Name(), schema)
		}
	}
	return &Tool{
		server:      server,
		name:        info.Name,
		description: info.Description,
		inputSchema: schema,
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
