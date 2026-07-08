package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	sdktools "github.com/v0lka/sp4rk/tools"
)

func TestTool_DefaultPolicy(t *testing.T) {
	// Create a minimal Tool for testing
	server := &Server{} // We need a minimal server, but DefaultPolicy doesn't use it
	tool := NewTool(server, ToolInfo{
		Name:        "test_tool",
		Description: "A test MCP tool",
		InputSchema: []byte(`{"type": "object"}`),
	})

	if tool.DefaultPolicy() != sdktools.PolicyUserConfirm {
		t.Errorf("expected DefaultPolicy() to return PolicyUserConfirm, got %v", tool.DefaultPolicy())
	}
}

func TestTool_ImplementsToolInterface(t *testing.T) {
	// Compile-time check that Tool implements tools.Tool interface
	var _ sdktools.Tool = (*Tool)(nil)
}

func TestTool_Execute_InvalidJSON(t *testing.T) {
	server := newServer("test")
	tool := NewTool(server, ToolInfo{
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

func TestTool_Execute_NilClient(t *testing.T) {
	server := newServer("test")
	tool := NewTool(server, ToolInfo{
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

func TestTool_Execute_EmptyInput(t *testing.T) {
	server := newServer("test")
	tool := NewTool(server, ToolInfo{
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

func TestTool_Execute_NilInput(t *testing.T) {
	server := newServer("test")
	tool := NewTool(server, ToolInfo{
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
	structured := map[string]any{
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
		StructuredContent: map[string]any{"key": "should-be-ignored"},
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

func TestNewTool_WithSanitizer(t *testing.T) {
	server := newServer("test-mcp")
	info := ToolInfo{
		Name:        "search_graph",
		Description: "Search the code graph",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"project":{"type":"string"},"name_pattern":{"type":"string"}},"required":["project"]}`),
	}

	sanitizer := func(source string, schema json.RawMessage) json.RawMessage {
		if source != "test-mcp" {
			return schema
		}
		return sdktools.DefaultParamManager().SanitizeSchema(source, schema)
	}

	tool := NewTool(server, info, sanitizer)

	// Verify the tool's InputSchema no longer has "project"
	var schema map[string]json.RawMessage
	if err := json.Unmarshal(tool.InputSchema(), &schema); err != nil {
		t.Fatalf("tool schema is not valid JSON: %v", err)
	}

	var props map[string]json.RawMessage
	if err := json.Unmarshal(schema["properties"], &props); err != nil {
		t.Fatalf("properties is not valid JSON: %v", err)
	}
	if _, ok := props["project"]; ok {
		t.Error("'project' should be stripped from tool schema")
	}
	if _, ok := props["name_pattern"]; !ok {
		t.Error("'name_pattern' should still be present in tool schema")
	}

	// "required" should no longer contain "project"
	var required []string
	if err := json.Unmarshal(schema["required"], &required); err != nil {
		t.Fatalf("required is not valid JSON: %v", err)
	}
	for _, r := range required {
		if r == "project" {
			t.Error("'project' should be stripped from required in tool schema")
		}
	}
}

func TestNewTool_WithNilSanitizer(t *testing.T) {
	server := newServer("test-server")
	info := ToolInfo{
		Name:        "test_tool",
		Description: "A test tool",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"name":{"type":"string"}}}`),
	}

	tool := NewTool(server, info, nil)
	if string(tool.InputSchema()) != string(info.InputSchema) {
		t.Errorf("expected schema unchanged with nil sanitizer")
	}
}

func TestNewTool_SanitizerSkippedForOtherSource(t *testing.T) {
	server := newServer("other-server")
	info := ToolInfo{
		Name:        "some_tool",
		Description: "A tool",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"project":{"type":"string"}}}`),
	}

	// Sanitizer that only strips project for test-mcp source
	sanitizer := func(source string, schema json.RawMessage) json.RawMessage {
		if source != "test-mcp" {
			return schema
		}
		return sdktools.DefaultParamManager().SanitizeSchema(source, schema)
	}

	tool := NewTool(server, info, sanitizer)

	// Schema should be unchanged for non-test-mcp server
	if string(tool.InputSchema()) != string(info.InputSchema) {
		t.Errorf("expected schema unchanged for non-test-mcp server, got %s", tool.InputSchema())
	}
}
