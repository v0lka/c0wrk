package mcp

import (
	"context"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/user/agent/internal/tools"
)

func TestMCPTool_DefaultPolicy(t *testing.T) {
	// Create a minimal MCPTool for testing
	server := &MCPServer{} // We need a minimal server, but DefaultPolicy doesn't use it
	tool := NewMCPTool(server, MCPToolInfo{
		Name:        "test_tool",
		Description: "A test MCP tool",
		InputSchema: []byte(`{"type": "object"}`),
	})

	if tool.DefaultPolicy() != tools.PolicyAuto {
		t.Errorf("expected DefaultPolicy() to return PolicyAuto, got %v", tool.DefaultPolicy())
	}
}

func TestMCPTool_ImplementsToolInterface(t *testing.T) {
	// Compile-time check that MCPTool implements Tool interface
	var _ tools.Tool = (*MCPTool)(nil)
}

func TestMCPTool_Execute_InvalidJSON(t *testing.T) {
	server := NewMCPServer("test")
	tool := NewMCPTool(server, MCPToolInfo{
		Name:        "test_tool",
		Description: "A test tool",
		InputSchema: []byte(`{"type": "object"}`),
	})

	// Invalid JSON should return an error result (not a Go error)
	result, err := tool.Execute(context.Background(), []byte(`{invalid json}`))
	if err != nil {
		t.Fatalf("Execute should not return Go error for invalid JSON, got: %v", err)
	}
	if !result.IsError {
		t.Error("expected IsError=true for invalid JSON input")
	}
	if !strings.Contains(result.Content, "failed to parse input") {
		t.Errorf("unexpected error content: %s", result.Content)
	}
}

func TestMCPTool_Execute_NilClient(t *testing.T) {
	server := NewMCPServer("test")
	tool := NewMCPTool(server, MCPToolInfo{
		Name:        "test_tool",
		Description: "A test tool",
		InputSchema: []byte(`{"type": "object"}`),
	})

	// Calling execute on a disconnected server should return error result
	result, err := tool.Execute(context.Background(), []byte(`{"key": "value"}`))
	if err != nil {
		t.Fatalf("Execute should not return Go error, got: %v", err)
	}
	if !result.IsError {
		t.Error("expected IsError=true for disconnected server")
	}
	if !strings.Contains(result.Content, "MCP tool call failed") {
		t.Errorf("unexpected error content: %s", result.Content)
	}
}

func TestMCPTool_Execute_EmptyInput(t *testing.T) {
	server := NewMCPServer("test")
	tool := NewMCPTool(server, MCPToolInfo{
		Name:        "test_tool",
		Description: "A test tool",
		InputSchema: []byte(`{"type": "object"}`),
	})

	// Empty input should still attempt to call (and fail due to nil client)
	result, err := tool.Execute(context.Background(), []byte{})
	if err != nil {
		t.Fatalf("Execute should not return Go error, got: %v", err)
	}
	if !result.IsError {
		t.Error("expected IsError=true for disconnected server")
	}
}

func TestMCPTool_Execute_NilInput(t *testing.T) {
	server := NewMCPServer("test")
	tool := NewMCPTool(server, MCPToolInfo{
		Name:        "test_tool",
		Description: "A test tool",
		InputSchema: []byte(`{"type": "object"}`),
	})

	// Nil input should still attempt to call (and fail due to nil client)
	result, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("Execute should not return Go error, got: %v", err)
	}
	if !result.IsError {
		t.Error("expected IsError=true for disconnected server")
	}
}

func TestExtractTextFromContent_TextContent(t *testing.T) {
	content := mcp.NewTextContent("hello world")
	result := extractTextFromContent(content)
	if result != "hello world" {
		t.Errorf("expected 'hello world', got %q", result)
	}
}

func TestExtractTextFromContent_EmptyText(t *testing.T) {
	content := mcp.NewTextContent("")
	// Empty text from GetTextFromContent, then AsTextContent also empty, then falls through to JSON marshal
	result := extractTextFromContent(content)
	// Result should be a JSON marshaling of the content struct or empty - both acceptable
	_ = result
}

func TestExtractTextFromContent_ImageContent(t *testing.T) {
	content := mcp.NewImageContent("base64data", "image/png")
	result := extractTextFromContent(content)
	// Image content should fall through to JSON marshal
	if result == "" {
		t.Error("expected non-empty result for image content JSON fallback")
	}
	// Should contain the image data in JSON format
	if !strings.Contains(result, "base64data") {
		t.Errorf("expected result to contain image data, got: %s", result)
	}
}

func TestExtractTextFromContent_AudioContent(t *testing.T) {
	content := mcp.NewAudioContent("audiodata", "audio/mp3")
	result := extractTextFromContent(content)
	// Audio content should fall through to JSON marshal
	if result == "" {
		t.Error("expected non-empty result for audio content JSON fallback")
	}
	if !strings.Contains(result, "audiodata") {
		t.Errorf("expected result to contain audio data, got: %s", result)
	}
}

func TestConvertMCPResult_EmptyContent(t *testing.T) {
	result := convertMCPResult(&mcp.CallToolResult{
		Content: []mcp.Content{},
		IsError: false,
	})
	if result.Content != "" {
		t.Errorf("expected empty content, got %q", result.Content)
	}
	if result.IsError {
		t.Error("expected IsError=false")
	}
}

func TestConvertMCPResult_StructuredContent(t *testing.T) {
	structured := map[string]interface{}{
		"key":   "value",
		"count": float64(42),
	}
	result := convertMCPResult(&mcp.CallToolResult{
		Content:           []mcp.Content{},
		StructuredContent: structured,
		IsError:           false,
	})
	// When no text content but structured content exists, it should be JSON marshaled
	if result.Content == "" {
		t.Error("expected structured content to be serialized")
	}
	if !strings.Contains(result.Content, "value") {
		t.Errorf("expected content to contain 'value', got: %s", result.Content)
	}
	if !strings.Contains(result.Content, "42") {
		t.Errorf("expected content to contain '42', got: %s", result.Content)
	}
}

func TestConvertMCPResult_StructuredContent_IgnoredWhenTextPresent(t *testing.T) {
	result := convertMCPResult(&mcp.CallToolResult{
		Content: []mcp.Content{
			mcp.NewTextContent("text result"),
		},
		StructuredContent: map[string]interface{}{"key": "should-be-ignored"},
		IsError:           false,
	})
	// Text content should take precedence
	if result.Content != "text result" {
		t.Errorf("expected 'text result', got %q", result.Content)
	}
	if strings.Contains(result.Content, "should-be-ignored") {
		t.Error("structured content should be ignored when text content is present")
	}
}

func TestConvertMCPResult_ImageContent(t *testing.T) {
	result := convertMCPResult(&mcp.CallToolResult{
		Content: []mcp.Content{
			mcp.NewImageContent("imgdata", "image/png"),
		},
		IsError: false,
	})
	// Image content should be JSON-marshaled via extractTextFromContent
	if result.Content == "" {
		t.Error("expected non-empty content for image result")
	}
}

func TestConvertMCPResult_MixedContent(t *testing.T) {
	result := convertMCPResult(&mcp.CallToolResult{
		Content: []mcp.Content{
			mcp.NewTextContent("first line"),
			mcp.NewTextContent("second line"),
			mcp.NewImageContent("imgdata", "image/png"),
		},
		IsError: false,
	})
	if !strings.Contains(result.Content, "first line") {
		t.Errorf("expected content to contain 'first line', got: %s", result.Content)
	}
	if !strings.Contains(result.Content, "second line") {
		t.Errorf("expected content to contain 'second line', got: %s", result.Content)
	}
}
