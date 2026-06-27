package tools

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

// mockTool is a simple Tool implementation for testing.
type mockTool struct {
	BaseTool
	execFn func(ctx context.Context, input json.RawMessage) (ToolResult, error)
}

func (m *mockTool) Execute(ctx context.Context, input json.RawMessage) (ToolResult, error) {
	if m.execFn != nil {
		return m.execFn(ctx, input)
	}
	return ToolResult{Content: "ok"}, nil
}

func newMockTool(name, desc string) *mockTool {
	return &mockTool{
		BaseTool: BaseTool{
			ToolName:        name,
			ToolDescription: desc,
			Schema:          json.RawMessage(`{"type":"object"}`),
			Policy:          PolicyAlwaysAllow,
		},
	}
}

func TestNewToolRegistry(t *testing.T) {
	reg := NewToolRegistry()
	if reg == nil {
		t.Fatal("expected non-nil registry")
	}
	if list := reg.List(); len(list) != 0 {
		t.Fatalf("expected empty list, got %d", len(list))
	}
}

func TestRegisterAndGet(t *testing.T) {
	reg := NewToolRegistry()
	tool := newMockTool("test_tool", "A test tool")
	reg.Register(tool)

	got, ok := reg.Get("test_tool")
	if !ok {
		t.Fatal("expected tool to be found")
	}
	if got.Name() != "test_tool" {
		t.Errorf("got name %q", got.Name())
	}
}

func TestGetNonExistent(t *testing.T) {
	reg := NewToolRegistry()
	_, ok := reg.Get("missing")
	if ok {
		t.Fatal("expected not found")
	}
}

func TestRegisterDuplicate(t *testing.T) {
	reg := NewToolRegistry()
	tool1 := newMockTool("dup", "first")
	tool2 := newMockTool("dup", "second")

	reg.Register(tool1)
	reg.Register(tool2)

	got, ok := reg.Get("dup")
	if !ok {
		t.Fatal("expected tool to be found")
	}
	// Last registration wins
	if got.Description() != "second" {
		t.Errorf("expected last-registered tool, got description %q", got.Description())
	}
	// Should only appear once in List
	if list := reg.List(); len(list) != 1 {
		t.Errorf("expected 1 descriptor, got %d", len(list))
	}
}

func TestRegisterWithSource(t *testing.T) {
	reg := NewToolRegistry()
	tool := newMockTool("ext_tool", "external")
	reg.RegisterWithSource(tool, "external")

	descriptors := reg.List()
	if len(descriptors) != 1 {
		t.Fatalf("expected 1, got %d", len(descriptors))
	}
	if descriptors[0].Source != "external" {
		t.Errorf("expected source 'external', got %q", descriptors[0].Source)
	}
}

func TestUnregister(t *testing.T) {
	reg := NewToolRegistry()
	tool := newMockTool("removeme", "temp")
	reg.RegisterWithSource(tool, "mcp")
	reg.Unregister("removeme")

	_, ok := reg.Get("removeme")
	if ok {
		t.Fatal("expected tool to be removed")
	}
	if list := reg.List(); len(list) != 0 {
		t.Fatalf("expected 0 tools after unregister, got %d", len(list))
	}
}

func TestUnregisterNonExistent(t *testing.T) {
	reg := NewToolRegistry()
	// Should not panic
	reg.Unregister("nope")
}

func TestUnregisterBySource(t *testing.T) {
	reg := NewToolRegistry()
	reg.RegisterWithSource(newMockTool("tool1", "first"), "server-a")
	reg.RegisterWithSource(newMockTool("tool2", "second"), "server-a")
	reg.RegisterWithSource(newMockTool("tool3", "third"), "server-b")

	// Remove all tools from server-a
	reg.UnregisterBySource("server-a")

	// tool1 and tool2 should be gone
	if _, ok := reg.Get("tool1"); ok {
		t.Error("tool1 should be unregistered")
	}
	if _, ok := reg.Get("tool2"); ok {
		t.Error("tool2 should be unregistered")
	}

	// tool3 should still exist
	if _, ok := reg.Get("tool3"); !ok {
		t.Error("tool3 should still be registered")
	}

	// List should only have 1 tool
	if list := reg.List(); len(list) != 1 {
		t.Errorf("expected 1 tool, got %d", len(list))
	}
}

func TestUnregisterBySourceNonExistent(t *testing.T) {
	reg := NewToolRegistry()
	reg.RegisterWithSource(newMockTool("tool1", "test"), "server-a")

	// Unregistering a non-existent source should be a no-op
	reg.UnregisterBySource("nonexistent")

	// tool1 should still exist
	if _, ok := reg.Get("tool1"); !ok {
		t.Error("tool1 should still be registered")
	}
}

func TestUnregisterBySourceEmptyRegistry(t *testing.T) {
	reg := NewToolRegistry()
	// Should not panic
	reg.UnregisterBySource("anything")
}

func TestListDescriptors(t *testing.T) {
	reg := NewToolRegistry()
	reg.Register(newMockTool("a", "tool a"))
	reg.RegisterWithSource(newMockTool("b", "tool b"), "mcp")

	list := reg.List()
	if len(list) != 2 {
		t.Fatalf("expected 2 descriptors, got %d", len(list))
	}

	byName := make(map[string]ToolDescriptor)
	for _, d := range list {
		byName[d.Name] = d
	}

	if d, ok := byName["a"]; !ok {
		t.Error("missing tool a")
	} else {
		if d.Source != "core" {
			t.Errorf("tool a source = %q, want 'core'", d.Source)
		}
		if d.Description != "tool a" {
			t.Errorf("tool a description = %q", d.Description)
		}
	}

	if d, ok := byName["b"]; !ok {
		t.Error("missing tool b")
	} else if d.Source != "mcp" {
		t.Errorf("tool b source = %q, want 'mcp'", d.Source)
	}
}

func TestExecuteSuccess(t *testing.T) {
	reg := NewToolRegistry()
	tool := newMockTool("echo", "echoes")
	tool.execFn = func(_ context.Context, input json.RawMessage) (ToolResult, error) {
		return ToolResult{Content: string(input)}, nil
	}
	reg.Register(tool)

	res, err := reg.Execute(context.Background(), "echo", json.RawMessage(`"hello"`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Content != `"hello"` {
		t.Errorf("content = %q", res.Content)
	}
}

func TestExecuteNotFound(t *testing.T) {
	reg := NewToolRegistry()
	result, err := reg.Execute(context.Background(), "ghost", nil)
	if err != nil {
		t.Fatalf("expected nil infrastructure error for missing tool, got %v", err)
	}
	if !result.IsError {
		t.Fatal("expected IsError=true for missing tool result")
	}
	if result.Content != "tool not found: ghost" {
		t.Errorf("unexpected result content: %q", result.Content)
	}
}

func TestExecuteToolError(t *testing.T) {
	reg := NewToolRegistry()
	tool := newMockTool("failing", "fails")
	tool.execFn = func(_ context.Context, _ json.RawMessage) (ToolResult, error) {
		return ToolResult{}, errors.New("boom")
	}
	reg.Register(tool)

	_, err := reg.Execute(context.Background(), "failing", nil)
	if err == nil || err.Error() != "boom" {
		t.Fatalf("expected 'boom' error, got %v", err)
	}
}

func TestExecuteToolResultError(t *testing.T) {
	reg := NewToolRegistry()
	tool := newMockTool("soft_fail", "returns error result")
	tool.execFn = func(_ context.Context, _ json.RawMessage) (ToolResult, error) {
		return ErrorResult("bad input"), nil
	}
	reg.Register(tool)

	res, err := reg.Execute(context.Background(), "soft_fail", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.IsError {
		t.Error("expected IsError=true")
	}
}

func TestListDefaultSourceIsCore(t *testing.T) {
	reg := NewToolRegistry()
	reg.Register(newMockTool("plain", "no source set"))
	list := reg.List()
	if len(list) != 1 || list[0].Source != "core" {
		t.Errorf("expected source 'core', got %q", list[0].Source)
	}
}

func TestListFiltered_Nil(t *testing.T) {
	reg := NewToolRegistry()
	reg.Register(newMockTool("a", "tool a"))
	reg.Register(newMockTool("b", "tool b"))

	// nil exclude → all tools returned.
	list := reg.ListFiltered(nil)
	if len(list) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(list))
	}
}

func TestListFiltered_Empty(t *testing.T) {
	reg := NewToolRegistry()
	reg.Register(newMockTool("a", "tool a"))

	// empty exclude map → all tools returned.
	list := reg.ListFiltered(map[string]bool{})
	if len(list) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(list))
	}
}

func TestListFiltered_ExcludeOne(t *testing.T) {
	reg := NewToolRegistry()
	reg.Register(newMockTool("keep", "keep me"))
	reg.Register(newMockTool("drop", "drop me"))

	list := reg.ListFiltered(map[string]bool{"drop": true})
	if len(list) != 1 {
		t.Fatalf("expected 1 tool after exclude, got %d", len(list))
	}
	if list[0].Name != "keep" {
		t.Errorf("expected 'keep', got %q", list[0].Name)
	}
}

func TestListFiltered_ExcludeAll(t *testing.T) {
	reg := NewToolRegistry()
	reg.Register(newMockTool("a", "tool a"))
	reg.Register(newMockTool("b", "tool b"))

	list := reg.ListFiltered(map[string]bool{"a": true, "b": true})
	if len(list) != 0 {
		t.Fatalf("expected 0 tools, got %d", len(list))
	}
}

func TestListFiltered_MCPSource(t *testing.T) {
	reg := NewToolRegistry()
	reg.RegisterWithSource(newMockTool("mcp_tool", "mcp tool"), "mcp:server")

	list := reg.ListFiltered(map[string]bool{})
	if len(list) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(list))
	}
	if list[0].SourceCategory != SourceCategoryMCP {
		t.Errorf("expected MCP source category, got %q", list[0].SourceCategory)
	}
}

func TestGetToolSource_NotFound(t *testing.T) {
	reg := NewToolRegistry()
	if src := reg.GetToolSource("nonexistent"); src != "" {
		t.Errorf("expected empty string for unknown tool, got %q", src)
	}
}

func TestGetToolSource_NoSource(t *testing.T) {
	reg := NewToolRegistry()
	reg.Register(newMockTool("plain", "no source"))
	if src := reg.GetToolSource("plain"); src != "core" {
		t.Errorf("expected 'core' for plain tool, got %q", src)
	}
}

func TestGetToolSource_WithSource(t *testing.T) {
	reg := NewToolRegistry()
	reg.RegisterWithSource(newMockTool("ext", "external"), "mcp:server")
	if src := reg.GetToolSource("ext"); src != "mcp:server" {
		t.Errorf("expected 'mcp:server', got %q", src)
	}
}

func TestIsToolUntrusted_NotFound(t *testing.T) {
	reg := NewToolRegistry()
	if reg.IsToolUntrusted("ghost") {
		t.Error("expected false for unknown tool")
	}
}

func TestIsToolUntrusted_TrustedCore(t *testing.T) {
	reg := NewToolRegistry()
	tool := newMockTool("safe", "safe tool")
	tool.Untrusted = false
	reg.Register(tool)
	if reg.IsToolUntrusted("safe") {
		t.Error("expected false for trusted core tool")
	}
}

func TestIsToolUntrusted_ToolFlag(t *testing.T) {
	reg := NewToolRegistry()
	tool := newMockTool("risky", "risky tool")
	tool.Untrusted = true
	reg.Register(tool)
	if !reg.IsToolUntrusted("risky") {
		t.Error("expected true for tool with Untrusted=true")
	}
}

func TestIsToolUntrusted_MCPSource(t *testing.T) {
	reg := NewToolRegistry()
	tool := newMockTool("mcp_tool", "mcp tool")
	tool.Untrusted = false // tool claims trusted, but MCP source overrides
	reg.RegisterWithSource(tool, "mcp:server")
	if !reg.IsToolUntrusted("mcp_tool") {
		t.Error("expected true for MCP-sourced tool regardless of IsUntrusted()")
	}
}
