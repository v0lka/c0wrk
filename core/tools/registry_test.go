package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	sdktools "github.com/v0lka/sp4rk/tools"
)

// mockTool is a simple echo tool for testing.
type mockTool struct {
	name          string
	description   string
	inputSchema   json.RawMessage
	defaultPolicy sdktools.ToolPolicy
}

func newMockTool(name, description string) *mockTool {
	return &mockTool{
		name:          name,
		description:   description,
		inputSchema:   json.RawMessage(`{"type":"object","properties":{"input":{"type":"string"}}}`),
		defaultPolicy: sdktools.PolicyUserConfirm,
	}
}

func newMockReadOnlyTool(name, description string) *mockTool {
	return &mockTool{
		name:          name,
		description:   description,
		inputSchema:   json.RawMessage(`{"type":"object","properties":{"input":{"type":"string"}}}`),
		defaultPolicy: sdktools.PolicyAlwaysAllow,
	}
}

func (m *mockTool) Name() string {
	return m.name
}

func (m *mockTool) Description() string {
	return m.description
}

func (m *mockTool) InputSchema() json.RawMessage {
	return m.inputSchema
}

func (m *mockTool) DefaultPolicy() sdktools.ToolPolicy {
	return m.defaultPolicy
}

func (m *mockTool) IsUntrusted() bool { return false }

func (m *mockTool) Execute(ctx context.Context, input json.RawMessage) (sdktools.ToolResult, error) {
	// Simple echo: return the input as content
	return sdktools.ToolResult{
		Content: string(input),
		IsError: false,
	}, nil
}

func TestToolRegistry_RegisterAndGet(t *testing.T) {
	registry := NewToolRegistry()
	tool := newMockTool("echo", "An echo tool")

	// Register the tool
	registry.Register(tool)

	// Get should find the tool
	got, ok := registry.Get("echo")
	if !ok {
		t.Fatal("expected to find registered tool 'echo', but got not found")
	}
	if got.Name() != "echo" {
		t.Errorf("expected tool name 'echo', got %q", got.Name())
	}

	// Get non-existent tool should return false
	_, ok = registry.Get("nonexistent")
	if ok {
		t.Error("expected not to find 'nonexistent' tool, but got found")
	}
}

func TestToolRegistry_Execute(t *testing.T) {
	registry := NewToolRegistry()
	tool := newMockTool("echo", "An echo tool")
	registry.Register(tool)

	ctx := context.Background()
	input := json.RawMessage(`{"message":"hello"}`)

	// Execute should work for registered tool
	result, err := registry.Execute(ctx, "echo", input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Content != `{"message":"hello"}` {
		t.Errorf("expected content %q, got %q", `{"message":"hello"}`, result.Content)
	}
	if result.IsError {
		t.Error("expected IsError to be false")
	}
}

func TestToolRegistry_ExecuteNonExistent(t *testing.T) {
	registry := NewToolRegistry()
	ctx := context.Background()
	input := json.RawMessage(`{}`)

	// Execute non-existent tool should return an error result, not an infrastructure error
	result, err := registry.Execute(ctx, "nonexistent", input)
	if err != nil {
		t.Fatalf("expected nil infrastructure error for non-existent tool, got %v", err)
	}
	if !result.IsError {
		t.Error("expected IsError=true for non-existent tool result")
	}
	expectedContent := "tool not found: nonexistent"
	if result.Content != expectedContent {
		t.Errorf("expected content %q, got %q", expectedContent, result.Content)
	}
}

func TestToolRegistry_List(t *testing.T) {
	registry := NewToolRegistry()
	tool := newMockTool("echo", "An echo tool")
	registry.Register(tool)

	descriptors := registry.List()
	if len(descriptors) != 1 {
		t.Fatalf("expected 1 descriptor, got %d", len(descriptors))
	}

	desc := descriptors[0]
	if desc.Name != "echo" {
		t.Errorf("expected descriptor name 'echo', got %q", desc.Name)
	}
	if desc.Description != "An echo tool" {
		t.Errorf("expected descriptor description 'An echo tool', got %q", desc.Description)
	}
	if desc.Source != "core" {
		t.Errorf("expected descriptor source 'core', got %q", desc.Source)
	}
	if string(desc.InputSchema) != `{"type":"object","properties":{"input":{"type":"string"}}}` {
		t.Errorf("unexpected input schema: %s", string(desc.InputSchema))
	}
}

func TestToolRegistry_Unregister(t *testing.T) {
	registry := NewToolRegistry()
	tool := newMockTool("echo", "An echo tool")

	// Register the tool
	registry.Register(tool)

	// Verify it's registered
	_, ok := registry.Get("echo")
	if !ok {
		t.Fatal("expected to find registered tool 'echo'")
	}

	// Unregister the tool
	registry.Unregister("echo")

	// Verify it's no longer registered
	_, ok = registry.Get("echo")
	if ok {
		t.Error("expected tool 'echo' to be unregistered, but found it")
	}
}

func TestToolRegistry_MultipleTools(t *testing.T) {
	registry := NewToolRegistry()
	tool1 := newMockTool("tool1", "First tool")
	tool2 := newMockTool("tool2", "Second tool")

	registry.Register(tool1)
	registry.Register(tool2)

	// Both should be retrievable
	_, ok := registry.Get("tool1")
	if !ok {
		t.Error("expected to find 'tool1'")
	}
	_, ok = registry.Get("tool2")
	if !ok {
		t.Error("expected to find 'tool2'")
	}

	// List should return both
	descriptors := registry.List()
	if len(descriptors) != 2 {
		t.Errorf("expected 2 descriptors, got %d", len(descriptors))
	}
}

func TestConfirmFunc_AllowOnce(t *testing.T) {
	registry := NewToolRegistry()
	tool := newMockTool("mutating", "A mutating tool")
	registry.Register(tool)

	confirmCalled := false
	registry.SetConfirmFunc(func(ctx context.Context, req sdktools.ConfirmationRequest) (sdktools.ConfirmationResponse, error) {
		confirmCalled = true
		if req.ToolName != "mutating" {
			t.Errorf("expected tool name 'mutating', got %q", req.ToolName)
		}
		return sdktools.ConfirmAllowOnce, nil
	})

	ctx := context.Background()
	input := json.RawMessage(`{"data":"test"}`)

	result, err := registry.Execute(ctx, "mutating", input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Error("expected IsError to be false")
	}
	if !confirmCalled {
		t.Error("expected confirmFunc to be called")
	}
}

func TestConfirmFunc_Deny(t *testing.T) {
	registry := NewToolRegistry()
	tool := newMockTool("mutating", "A mutating tool")
	registry.Register(tool)

	registry.SetConfirmFunc(func(ctx context.Context, req sdktools.ConfirmationRequest) (sdktools.ConfirmationResponse, error) {
		return sdktools.ConfirmDeny, nil
	})

	ctx := context.Background()
	input := json.RawMessage(`{"data":"test"}`)

	result, err := registry.Execute(ctx, "mutating", input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected IsError to be true")
	}
	if result.Content != "Tool execution denied by user." {
		t.Errorf("expected content 'Tool execution denied by user.', got %q", result.Content)
	}
}

func TestConfirmFunc_DenyAndStop(t *testing.T) {
	registry := NewToolRegistry()
	tool := newMockTool("mutating", "A mutating tool")
	registry.Register(tool)

	registry.SetConfirmFunc(func(ctx context.Context, req sdktools.ConfirmationRequest) (sdktools.ConfirmationResponse, error) {
		return sdktools.ConfirmDenyAndStop, nil
	})

	ctx := context.Background()
	input := json.RawMessage(`{"data":"test"}`)

	result, err := registry.Execute(ctx, "mutating", input)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled error, got: %v", err)
	}
	if result.Content != "" || result.IsError {
		t.Error("expected empty result on DenyAndStop")
	}
}

func TestConfirmFunc_ReadOnlyBypass(t *testing.T) {
	registry := NewToolRegistry()
	tool := newMockReadOnlyTool("readonly", "A read-only tool")
	registry.Register(tool)

	confirmCalled := false
	registry.SetConfirmFunc(func(ctx context.Context, req sdktools.ConfirmationRequest) (sdktools.ConfirmationResponse, error) {
		confirmCalled = true
		return sdktools.ConfirmAllowOnce, nil
	})

	ctx := context.Background()
	input := json.RawMessage(`{"data":"test"}`)

	result, err := registry.Execute(ctx, "readonly", input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Error("expected IsError to be false")
	}
	if confirmCalled {
		t.Error("expected confirmFunc NOT to be called for PolicyAlwaysAllow tool")
	}
}

func TestConfirmFunc_NilFunc(t *testing.T) {
	registry := NewToolRegistry()
	// Use a tool with PolicyAlwaysAllow so it executes without confirmation
	tool := newMockReadOnlyTool("readonly", "A read-only tool")
	registry.Register(tool)

	// confirmFunc is nil by default
	ctx := context.Background()
	input := json.RawMessage(`{"data":"test"}`)

	result, err := registry.Execute(ctx, "readonly", input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Error("expected IsError to be false")
	}
}

func TestConfirmFunc_ConfirmFuncError(t *testing.T) {
	registry := NewToolRegistry()
	tool := newMockTool("mutating", "A mutating tool")
	registry.Register(tool)

	expectedErr := context.DeadlineExceeded
	registry.SetConfirmFunc(func(ctx context.Context, req sdktools.ConfirmationRequest) (sdktools.ConfirmationResponse, error) {
		return 0, expectedErr
	})

	ctx := context.Background()
	input := json.RawMessage(`{"data":"test"}`)

	_, err := registry.Execute(ctx, "mutating", input)
	if !errors.Is(err, expectedErr) {
		t.Fatalf("expected error %v, got %v", expectedErr, err)
	}
}

// TestPolicyAlwaysAllow_ExecutesImmediately tests that PolicyAlwaysAllow executes without confirmation.
func TestPolicyAlwaysAllow_ExecutesImmediately(t *testing.T) {
	registry := NewToolRegistry()
	tool := newMockReadOnlyTool("always_allow", "A tool with PolicyAlwaysAllow")
	registry.Register(tool)

	confirmCalled := false
	registry.SetConfirmFunc(func(ctx context.Context, req sdktools.ConfirmationRequest) (sdktools.ConfirmationResponse, error) {
		confirmCalled = true
		return sdktools.ConfirmAllowOnce, nil
	})

	ctx := context.Background()
	input := json.RawMessage(`{"data":"test"}`)

	result, err := registry.Execute(ctx, "always_allow", input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Error("expected IsError to be false")
	}
	if result.Content != `{"data":"test"}` {
		t.Errorf("expected content %q, got %q", `{"data":"test"}`, result.Content)
	}
	if confirmCalled {
		t.Error("expected confirmFunc NOT to be called for PolicyAlwaysAllow")
	}
}

// TestPolicyAlwaysDeny_BlocksExecution tests that PolicyAlwaysDeny blocks execution.
func TestPolicyAlwaysDeny_BlocksExecution(t *testing.T) {
	registry := NewToolRegistry()
	tool := &mockTool{
		name:          "always_deny",
		description:   "A tool with PolicyAlwaysDeny",
		inputSchema:   json.RawMessage(`{"type":"object"}`),
		defaultPolicy: sdktools.PolicyAlwaysDeny,
	}
	registry.Register(tool)

	ctx := context.Background()
	input := json.RawMessage(`{"data":"test"}`)

	result, err := registry.Execute(ctx, "always_deny", input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected IsError to be true for PolicyAlwaysDeny")
	}
	if !strings.Contains(result.Content, "blocked by security policy") {
		t.Errorf("expected security policy error, got: %s", result.Content)
	}
}

// TestPolicyUserConfirm_AlwaysCallsConfirmFunc tests that PolicyUserConfirm always calls confirmFunc.
func TestPolicyUserConfirm_AlwaysCallsConfirmFunc(t *testing.T) {
	registry := NewToolRegistry()
	tool := newMockTool("user_confirm", "A tool with PolicyUserConfirm")
	registry.Register(tool)

	confirmCalled := false
	registry.SetConfirmFunc(func(ctx context.Context, req sdktools.ConfirmationRequest) (sdktools.ConfirmationResponse, error) {
		confirmCalled = true
		return sdktools.ConfirmAllowOnce, nil
	})

	ctx := context.Background()
	input := json.RawMessage(`{"data":"test"}`)

	result, err := registry.Execute(ctx, "user_confirm", input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Error("expected IsError to be false")
	}
	if !confirmCalled {
		t.Error("expected confirmFunc to be called for PolicyUserConfirm")
	}
}

func TestToolRegistry_RegisterWithSource(t *testing.T) {
	registry := NewToolRegistry()

	// Register a tool with source "mcp"
	tool1 := newMockTool("mcp_tool", "An MCP tool")
	registry.RegisterWithSource(tool1, "mcp")

	// Register another tool with source "core"
	tool2 := newMockTool("core_tool", "A core tool")
	registry.RegisterWithSource(tool2, "core")

	// List and verify sources
	descriptors := registry.List()
	if len(descriptors) != 2 {
		t.Fatalf("expected 2 descriptors, got %d", len(descriptors))
	}

	// Build a map for easier lookup
	descMap := make(map[string]sdktools.ToolDescriptor)
	for _, desc := range descriptors {
		descMap[desc.Name] = desc
	}

	// Verify mcp_tool has source "mcp"
	if desc, ok := descMap["mcp_tool"]; !ok {
		t.Error("expected to find 'mcp_tool' in descriptors")
	} else if desc.Source != "mcp" {
		t.Errorf("expected 'mcp_tool' source 'mcp', got %q", desc.Source)
	}

	// Verify core_tool has source "core"
	if desc, ok := descMap["core_tool"]; !ok {
		t.Error("expected to find 'core_tool' in descriptors")
	} else if desc.Source != "core" {
		t.Errorf("expected 'core_tool' source 'core', got %q", desc.Source)
	}
}

func TestToolRegistry_UnregisterCleansUpSource(t *testing.T) {
	registry := NewToolRegistry()

	// Register a tool with source "mcp"
	tool := newMockTool("mcp_tool", "An MCP tool")
	registry.RegisterWithSource(tool, "mcp")

	// Verify it's registered with correct source
	descriptors := registry.List()
	if len(descriptors) != 1 {
		t.Fatalf("expected 1 descriptor, got %d", len(descriptors))
	}
	if descriptors[0].Source != "mcp" {
		t.Errorf("expected source 'mcp', got %q", descriptors[0].Source)
	}

	// Unregister the tool
	registry.Unregister("mcp_tool")

	// Verify it's gone
	_, ok := registry.Get("mcp_tool")
	if ok {
		t.Error("expected tool 'mcp_tool' to be unregistered")
	}

	// Re-register using regular Register (should default to "core")
	tool2 := newMockTool("mcp_tool", "A core tool now")
	registry.Register(tool2)

	descriptors = registry.List()
	if len(descriptors) != 1 {
		t.Fatalf("expected 1 descriptor, got %d", len(descriptors))
	}
	if descriptors[0].Source != "core" {
		t.Errorf("expected source 'core' after re-registering with Register(), got %q", descriptors[0].Source)
	}
}

// TestPolicyOverrideFromConfig tests that policy overrides from config take precedence.
func TestPolicyOverrideFromConfig(t *testing.T) {
	registry := NewToolRegistry()
	// Tool has PolicyUserConfirm by default
	tool := newMockTool("overridden_tool", "A tool with overridden policy")
	registry.Register(tool)

	// Override to PolicyAlwaysAllow
	registry.SetPolicyOverrides(map[string]sdktools.ToolPolicy{
		"overridden_tool": sdktools.PolicyAlwaysAllow,
	})

	confirmCalled := false
	registry.SetConfirmFunc(func(ctx context.Context, req sdktools.ConfirmationRequest) (sdktools.ConfirmationResponse, error) {
		confirmCalled = true
		return sdktools.ConfirmAllowOnce, nil
	})

	ctx := context.Background()
	input := json.RawMessage(`{"data":"test"}`)

	result, err := registry.Execute(ctx, "overridden_tool", input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Error("expected IsError to be false")
	}
	if confirmCalled {
		t.Error("expected confirmFunc NOT to be called when policy is overridden to PolicyAlwaysAllow")
	}
}

func TestWithWorkspacePath_AndWorkspacePathFrom_Wrapper(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		expected string
	}{
		{"non-empty path", "/home/user/project", "/home/user/project"},
		{"empty path", "", ""},
		{"path with spaces", "/home/user/my project", "/home/user/my project"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			ctx = sdktools.WithWorkspacePath(ctx, tt.path)
			got := sdktools.WorkspacePathFrom(ctx)
			if got != tt.expected {
				t.Errorf("WorkspacePathFrom() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestWorkspacePathFrom_EmptyContext(t *testing.T) {
	ctx := context.Background()
	got := sdktools.WorkspacePathFrom(ctx)
	if got != "" {
		t.Errorf("WorkspacePathFrom() on empty context = %q, want empty", got)
	}
}

func TestSetDefaultPolicy(t *testing.T) {
	reg := NewToolRegistry()
	tool := newMockReadOnlyTool("test_tool", "desc")
	reg.Register(tool)

	// Before setting default policy, tool's own default is used
	result, err := reg.Execute(context.Background(), "test_tool", []byte(`{"input":"hello"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Error("expected no error with default policy")
	}

	// Set global default to AlwaysDeny
	reg.SetDefaultPolicy(sdktools.PolicyAlwaysDeny)
	result, err = reg.Execute(context.Background(), "test_tool", []byte(`{"input":"hello"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected IsError=true when global default is AlwaysDeny")
	}
	if !strings.Contains(result.Content, "blocked by security policy") {
		t.Errorf("expected blocked message, got: %s", result.Content)
	}
}

func TestSetDefaultPolicy_OverriddenByPerTool(t *testing.T) {
	reg := NewToolRegistry()
	tool := newMockReadOnlyTool("test_tool", "desc")
	reg.Register(tool)

	// Set global default to deny
	reg.SetDefaultPolicy(sdktools.PolicyAlwaysDeny)

	// But per-tool override to allow
	reg.SetPolicyOverrides(map[string]sdktools.ToolPolicy{
		"test_tool": sdktools.PolicyAlwaysAllow,
	})

	result, err := reg.Execute(context.Background(), "test_tool", []byte(`{"input":"hello"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Error("per-tool override should take precedence over global default")
	}
}

// mockJudgerTool is a tool that implements ToolJudger for testing.
type mockJudgerTool struct {
	mockTool
	judgeResult    bool
	judgeReasoning string
}

func newMockJudgerTool(name string, allow bool, reasoning string) *mockJudgerTool {
	return &mockJudgerTool{
		mockTool: mockTool{
			name:          name,
			description:   "A tool with ToolJudger",
			inputSchema:   json.RawMessage(`{"type":"object","properties":{"input":{"type":"string"}}}`),
			defaultPolicy: sdktools.PolicyAlwaysAllow,
		},
		judgeResult:    allow,
		judgeReasoning: reasoning,
	}
}

func (m *mockJudgerTool) Judge(ctx context.Context, input json.RawMessage) (allow bool, reasoning string) {
	return m.judgeResult, m.judgeReasoning
}

// mockConfirmJudgerTool is a tool with PolicyUserConfirm that implements ToolJudger.
// Used to test workspace auto-approval for write tools.
type mockConfirmJudgerTool struct {
	mockTool
	judgeResult    bool
	judgeReasoning string
}

func newMockConfirmJudgerTool(name string, policy sdktools.ToolPolicy, allow bool, reasoning string) *mockConfirmJudgerTool {
	return &mockConfirmJudgerTool{
		mockTool: mockTool{
			name:          name,
			description:   "A confirm tool with ToolJudger",
			inputSchema:   json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}}}`),
			defaultPolicy: policy,
		},
		judgeResult:    allow,
		judgeReasoning: reasoning,
	}
}

func (m *mockConfirmJudgerTool) Judge(ctx context.Context, input json.RawMessage) (allow bool, reasoning string) {
	return m.judgeResult, m.judgeReasoning
}

// TestPolicyAlwaysAllow_WithToolJudgerFlags tests that PolicyAlwaysAllow escalates to confirmation
// when the tool implements ToolJudger and returns allow=false with non-empty reasoning.
func TestPolicyAlwaysAllow_WithToolJudgerFlags(t *testing.T) {
	registry := NewToolRegistry()
	tool := newMockJudgerTool("judger_tool", false, "dangerous command detected")
	registry.Register(tool)

	confirmCalled := false
	var receivedReasoning string
	registry.SetConfirmFunc(func(ctx context.Context, req sdktools.ConfirmationRequest) (sdktools.ConfirmationResponse, error) {
		confirmCalled = true
		receivedReasoning = req.JudgeReasoning
		return sdktools.ConfirmAllowOnce, nil
	})

	ctx := context.Background()
	input := json.RawMessage(`{"data":"test"}`)

	result, err := registry.Execute(ctx, "judger_tool", input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Error("expected IsError to be false after confirmation")
	}
	if !confirmCalled {
		t.Error("expected confirmFunc to be called when ToolJudger flags the call")
	}
	if receivedReasoning != "dangerous command detected" {
		t.Errorf("expected reasoning %q, got %q", "dangerous command detected", receivedReasoning)
	}
}

// TestPolicyAlwaysAllow_WithToolJudgerAllows tests that PolicyAlwaysAllow executes directly
// when the tool implements ToolJudger and returns allow=true.
func TestPolicyAlwaysAllow_WithToolJudgerAllows(t *testing.T) {
	registry := NewToolRegistry()
	tool := newMockJudgerTool("judger_tool", true, "")
	registry.Register(tool)

	confirmCalled := false
	registry.SetConfirmFunc(func(ctx context.Context, req sdktools.ConfirmationRequest) (sdktools.ConfirmationResponse, error) {
		confirmCalled = true
		return sdktools.ConfirmAllowOnce, nil
	})

	ctx := context.Background()
	input := json.RawMessage(`{"data":"test"}`)

	result, err := registry.Execute(ctx, "judger_tool", input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Error("expected IsError to be false")
	}
	if result.Content != `{"data":"test"}` {
		t.Errorf("expected content %q, got %q", `{"data":"test"}`, result.Content)
	}
	if confirmCalled {
		t.Error("expected confirmFunc NOT to be called when ToolJudger allows the call")
	}
}

// TestPolicyAlwaysAllow_WithoutToolJudger tests that PolicyAlwaysAllow executes directly
// when the tool does not implement ToolJudger.
func TestPolicyAlwaysAllow_WithoutToolJudger(t *testing.T) {
	registry := NewToolRegistry()
	tool := newMockReadOnlyTool("no_judger_tool", "A tool without ToolJudger")
	registry.Register(tool)

	confirmCalled := false
	registry.SetConfirmFunc(func(ctx context.Context, req sdktools.ConfirmationRequest) (sdktools.ConfirmationResponse, error) {
		confirmCalled = true
		return sdktools.ConfirmAllowOnce, nil
	})

	ctx := context.Background()
	input := json.RawMessage(`{"data":"test"}`)

	result, err := registry.Execute(ctx, "no_judger_tool", input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Error("expected IsError to be false")
	}
	if result.Content != `{"data":"test"}` {
		t.Errorf("expected content %q, got %q", `{"data":"test"}`, result.Content)
	}
	if confirmCalled {
		t.Error("expected confirmFunc NOT to be called for tool without ToolJudger")
	}
}

// TestPolicyAlwaysAllow_WithToolJudgerEmptyReasoning tests that PolicyAlwaysAllow executes directly
// when the tool implements ToolJudger but returns empty reasoning (no concern to report).
func TestPolicyAlwaysAllow_WithToolJudgerEmptyReasoning(t *testing.T) {
	registry := NewToolRegistry()
	tool := newMockJudgerTool("judger_tool", false, "")
	registry.Register(tool)

	confirmCalled := false
	registry.SetConfirmFunc(func(ctx context.Context, req sdktools.ConfirmationRequest) (sdktools.ConfirmationResponse, error) {
		confirmCalled = true
		return sdktools.ConfirmAllowOnce, nil
	})

	ctx := context.Background()
	input := json.RawMessage(`{"data":"test"}`)

	result, err := registry.Execute(ctx, "judger_tool", input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Error("expected IsError to be false")
	}
	if result.Content != `{"data":"test"}` {
		t.Errorf("expected content %q, got %q", `{"data":"test"}`, result.Content)
	}
	if confirmCalled {
		t.Error("expected confirmFunc NOT to be called when ToolJudger returns empty reasoning")
	}
}

// TestAutoApproval_WorkspacePath verifies that PolicyUserConfirm is NOT bypassed
// even when all paths in the input are within the workspace directory.
// (Per C-2 in the 2026-06-05 review: workspace-locality MUST NOT silently
// downgrade an explicit user_confirm policy.)
func TestAutoApproval_WorkspacePath(t *testing.T) {
	registry := NewToolRegistry()
	tool := newMockTool("mutating", "A mutating tool") // defaultPolicy: PolicyUserConfirm
	registry.Register(tool)

	confirmCalled := false
	registry.SetConfirmFunc(func(ctx context.Context, req sdktools.ConfirmationRequest) (sdktools.ConfirmationResponse, error) {
		confirmCalled = true
		return sdktools.ConfirmAllowOnce, nil
	})

	ctx := context.Background()
	ctx = sdktools.WithWorkspacePath(ctx, "/workspace")
	input := json.RawMessage(`{"path": "/workspace/file.txt"}`)

	result, err := registry.Execute(ctx, "mutating", input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Error("expected IsError to be false")
	}
	if !confirmCalled {
		t.Error("expected confirmFunc to be called: workspace-locality must not bypass PolicyUserConfirm")
	}
}

// TestAutoApproval_UserConfirmWithJudger_WorkspaceEnabled tests that when
// autoApproveWorkspaceWrites is enabled and a PolicyUserConfirm tool's Judge
// returns allow=true, the tool executes without confirmation for workspace paths.
func TestAutoApproval_UserConfirmWithJudger_WorkspaceEnabled(t *testing.T) {
	registry := NewToolRegistry()
	registry.SetAutoApproveWorkspaceWrites(true)

	// Tool with PolicyUserConfirm + ToolJudger that allows workspace paths
	tool := newMockConfirmJudgerTool("write_file", sdktools.PolicyUserConfirm, true, "target is within session workspace")
	registry.Register(tool)

	confirmCalled := false
	registry.SetConfirmFunc(func(ctx context.Context, req sdktools.ConfirmationRequest) (sdktools.ConfirmationResponse, error) {
		confirmCalled = true
		return sdktools.ConfirmAllowOnce, nil
	})

	ctx := context.Background()
	ctx = sdktools.WithWorkspacePath(ctx, "/workspace")
	input := json.RawMessage(`{"path": "/workspace/file.txt"}`)

	_, err := registry.Execute(ctx, "write_file", input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if confirmCalled {
		t.Error("expected confirmFunc NOT to be called when auto-approve is enabled and Judge allows")
	}
}

// TestAutoApproval_UserConfirmWithJudger_WorkspaceDisabled tests that when
// autoApproveWorkspaceWrites is disabled, a PolicyUserConfirm tool always
// requires confirmation regardless of Judge result.
func TestAutoApproval_UserConfirmWithJudger_WorkspaceDisabled(t *testing.T) {
	registry := NewToolRegistry()
	// autoApproveWorkspaceWrites defaults to false

	tool := newMockConfirmJudgerTool("write_file", sdktools.PolicyUserConfirm, true, "target is within session workspace")
	registry.Register(tool)

	confirmCalled := false
	registry.SetConfirmFunc(func(ctx context.Context, req sdktools.ConfirmationRequest) (sdktools.ConfirmationResponse, error) {
		confirmCalled = true
		return sdktools.ConfirmAllowOnce, nil
	})

	ctx := context.Background()
	ctx = sdktools.WithWorkspacePath(ctx, "/workspace")
	input := json.RawMessage(`{"path": "/workspace/file.txt"}`)

	_, err := registry.Execute(ctx, "write_file", input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !confirmCalled {
		t.Error("expected confirmFunc to be called when auto-approve is disabled")
	}
}

// TestAutoApproval_UserConfirmWithJudger_OutsideWorkspace tests that even with
// auto-approve enabled, a PolicyUserConfirm tool still requires confirmation
// when the Judge returns allow=false (e.g., path outside workspace).
func TestAutoApproval_UserConfirmWithJudger_OutsideWorkspace(t *testing.T) {
	registry := NewToolRegistry()
	registry.SetAutoApproveWorkspaceWrites(true)

	// Tool with PolicyUserConfirm + ToolJudger that denies (returns allow=false)
	// This simulates bash_exec's Judge or a write tool targeting outside workspace.
	tool := newMockConfirmJudgerTool("write_file", sdktools.PolicyUserConfirm, false, "")
	registry.Register(tool)

	confirmCalled := false
	registry.SetConfirmFunc(func(ctx context.Context, req sdktools.ConfirmationRequest) (sdktools.ConfirmationResponse, error) {
		confirmCalled = true
		return sdktools.ConfirmAllowOnce, nil
	})

	ctx := context.Background()
	ctx = sdktools.WithWorkspacePath(ctx, "/workspace")
	input := json.RawMessage(`{"path": "/etc/passwd"}`)

	_, err := registry.Execute(ctx, "write_file", input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !confirmCalled {
		t.Error("expected confirmFunc to be called when Judge returns allow=false")
	}
}

// TestAutoApproval_UserConfirmWithJudger_OutsideWorkspace_ReasoningSurfaced
// verifies that when auto-approve is enabled and the Judge denies with a
// non-empty reason (e.g., "path is outside the session workspace and temp
// directory: /etc/passwd"), that reason is surfaced to the user via
// ConfirmationRequest.JudgeReasoning rather than being discarded.
//
// The security-model spec states: "user_confirm tools: confirmation is
// already required by policy; the Judge reason is surfaced in the
// confirmation dialog." Without surfacing, the user sees a confirm prompt
// with no explanation of why the call was escalated.
func TestAutoApproval_UserConfirmWithJudger_OutsideWorkspace_ReasoningSurfaced(t *testing.T) {
	registry := NewToolRegistry()
	registry.SetAutoApproveWorkspaceWrites(true)

	tool := newMockConfirmJudgerTool("write_file", sdktools.PolicyUserConfirm, false, "path is outside the session workspace and temp directory: /etc/passwd")
	registry.Register(tool)

	var receivedReasoning string
	registry.SetConfirmFunc(func(ctx context.Context, req sdktools.ConfirmationRequest) (sdktools.ConfirmationResponse, error) {
		receivedReasoning = req.JudgeReasoning
		return sdktools.ConfirmDeny, nil
	})

	ctx := context.Background()
	ctx = sdktools.WithWorkspacePath(ctx, "/workspace")
	input := json.RawMessage(`{"path": "/etc/passwd"}`)

	_, err := registry.Execute(ctx, "write_file", input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if receivedReasoning == "" {
		t.Error("expected Judge reasoning to be surfaced in ConfirmationRequest when auto-approve denies, got empty string")
	}
	if !strings.Contains(receivedReasoning, "outside") {
		t.Errorf("expected reasoning to mention 'outside', got: %s", receivedReasoning)
	}
}

// even when all paths in the input are within the session temp directory.
func TestAutoApproval_TempDir(t *testing.T) {
	registry := NewToolRegistry()
	tool := newMockTool("mutating", "A mutating tool") // defaultPolicy: PolicyUserConfirm
	registry.Register(tool)

	confirmCalled := false
	registry.SetConfirmFunc(func(ctx context.Context, req sdktools.ConfirmationRequest) (sdktools.ConfirmationResponse, error) {
		confirmCalled = true
		return sdktools.ConfirmAllowOnce, nil
	})

	ctx := context.Background()
	ctx = sdktools.WithTempDir(ctx, "/tmp/session123")
	input := json.RawMessage(`{"path": "/tmp/session123/tempfile.txt"}`)

	result, err := registry.Execute(ctx, "mutating", input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Error("expected IsError to be false")
	}
	if !confirmCalled {
		t.Error("expected confirmFunc to be called: temp-dir locality must not bypass PolicyUserConfirm")
	}
}

// TestAutoApproval_AlwaysAllow_WorkspacePath verifies that PolicyAlwaysAllow
// tools still execute without confirmation when paths are inside the workspace
// (the auto-approval optimization remains for explicitly-allow policies).
func TestAutoApproval_AlwaysAllow_WorkspacePath(t *testing.T) {
	registry := NewToolRegistry()
	tool := newMockReadOnlyTool("readonly", "A read-only tool") // defaultPolicy: PolicyAlwaysAllow
	registry.Register(tool)

	confirmCalled := false
	registry.SetConfirmFunc(func(ctx context.Context, req sdktools.ConfirmationRequest) (sdktools.ConfirmationResponse, error) {
		confirmCalled = true
		return sdktools.ConfirmAllowOnce, nil
	})

	ctx := context.Background()
	ctx = sdktools.WithWorkspacePath(ctx, "/workspace")
	input := json.RawMessage(`{"path": "/workspace/file.txt"}`)

	_, err := registry.Execute(ctx, "readonly", input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if confirmCalled {
		t.Error("expected confirmFunc NOT to be called for PolicyAlwaysAllow tools")
	}
}

// TestAutoApproval_AlwaysAllow_JudgerFlagsBeforeAutoApprove is a regression
// test for a security hole: when a PolicyAlwaysAllow tool implements ToolJudger
// and the Judge flags the call (allow=false with non-empty reasoning), the call
// MUST escalate to user confirmation even if all paths in the input are inside
// the session workspace or temp directory.
//
// Previously, workspace/temp auto-approval ran BEFORE the Judge check, so a
// command like "rm -rf /workspace/.git" (paths inside workspace, but matches
// the bash_exec blacklist) would execute without confirmation. Now the Judge
// runs first and short-circuits to confirmation on flagged calls.
func TestAutoApproval_AlwaysAllow_JudgerFlagsBeforeAutoApprove(t *testing.T) {
	registry := NewToolRegistry()
	// Tool with AlwaysAllow + ToolJudger that flags the call (simulates
	// bash_exec with a blacklisted command like "rm -rf /workspace/.git").
	tool := newMockJudgerTool("bash_exec", false, "command matches blacklist pattern: rm -rf")
	registry.Register(tool)

	confirmCalled := false
	registry.SetConfirmFunc(func(ctx context.Context, req sdktools.ConfirmationRequest) (sdktools.ConfirmationResponse, error) {
		confirmCalled = true
		return sdktools.ConfirmAllowOnce, nil
	})

	ctx := context.Background()
	ctx = sdktools.WithWorkspacePath(ctx, "/workspace")
	// Input has paths inside the workspace — would trigger auto-approval
	// if the Judge check ran after. The Judge must run first.
	input := json.RawMessage(`{"command": "rm -rf /workspace/.git"}`)

	_, err := registry.Execute(ctx, "bash_exec", input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !confirmCalled {
		t.Error("expected confirmFunc to be called: Judge-flagged calls must escalate to confirmation even when paths are inside workspace")
	}
}

// TestAutoApproval_AlwaysAllow_JudgerAllowsWithWorkspacePath verifies that a
// PolicyAlwaysAllow tool whose Judge returns allow=true still auto-approves
// when paths are inside the workspace (Judge ran first, cleared the call,
// then workspace auto-approval applied).
func TestAutoApproval_AlwaysAllow_JudgerAllowsWithWorkspacePath(t *testing.T) {
	registry := NewToolRegistry()
	// Tool with AlwaysAllow + ToolJudger that allows (simulates read_file
	// reading inside workspace).
	tool := newMockJudgerTool("read_file", true, "within workspace")
	registry.Register(tool)

	confirmCalled := false
	registry.SetConfirmFunc(func(ctx context.Context, req sdktools.ConfirmationRequest) (sdktools.ConfirmationResponse, error) {
		confirmCalled = true
		return sdktools.ConfirmAllowOnce, nil
	})

	ctx := context.Background()
	ctx = sdktools.WithWorkspacePath(ctx, "/workspace")
	input := json.RawMessage(`{"path": "/workspace/file.txt"}`)

	_, err := registry.Execute(ctx, "read_file", input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if confirmCalled {
		t.Error("expected confirmFunc NOT to be called when Judge allows and path is inside workspace")
	}
}

// TestAutoApproval_AlwaysAllow_JudgerEmptyReasoningWithWorkspacePath verifies
// that a PolicyAlwaysAllow tool whose Judge returns allow=false with EMPTY
// reasoning (e.g., bash_exec without blacklist match) still auto-approves
// when paths are inside the workspace. Empty reasoning means "no concern to
// report" — the call is not escalated, only flagged calls with non-empty
// reasoning trigger confirmation.
func TestAutoApproval_AlwaysAllow_JudgerEmptyReasoningWithWorkspacePath(t *testing.T) {
	registry := NewToolRegistry()
	// Tool with AlwaysAllow + ToolJudger that returns allow=false, reasoning=""
	// (simulates bash_exec without blacklist match).
	tool := newMockJudgerTool("bash_exec", false, "")
	registry.Register(tool)

	confirmCalled := false
	registry.SetConfirmFunc(func(ctx context.Context, req sdktools.ConfirmationRequest) (sdktools.ConfirmationResponse, error) {
		confirmCalled = true
		return sdktools.ConfirmAllowOnce, nil
	})

	ctx := context.Background()
	ctx = sdktools.WithWorkspacePath(ctx, "/workspace")
	input := json.RawMessage(`{"command": "ls /workspace"}`)

	_, err := registry.Execute(ctx, "bash_exec", input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if confirmCalled {
		t.Error("expected confirmFunc NOT to be called when Judge returns empty reasoning (no concern) and path is inside workspace")
	}
}

// TestAutoApproval_OutsideWorkspace tests that PolicyUserConfirm still requires
// confirmation when paths are outside the workspace.
func TestAutoApproval_OutsideWorkspace(t *testing.T) {
	registry := NewToolRegistry()
	tool := newMockTool("mutating", "A mutating tool")
	registry.Register(tool)

	confirmCalled := false
	registry.SetConfirmFunc(func(ctx context.Context, req sdktools.ConfirmationRequest) (sdktools.ConfirmationResponse, error) {
		confirmCalled = true
		return sdktools.ConfirmAllowOnce, nil
	})

	ctx := context.Background()
	ctx = sdktools.WithWorkspacePath(ctx, "/workspace")
	input := json.RawMessage(`{"path": "/etc/passwd"}`)

	result, err := registry.Execute(ctx, "mutating", input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Error("expected IsError to be false")
	}
	if !confirmCalled {
		t.Error("expected confirmFunc to be called when path is outside workspace")
	}
}

// TestIsInternalTool_ReturnsTrueForInternalTools tests that IsInternalTool()
// returns true for each internal tool.
func TestIsInternalTool_ReturnsTrueForInternalTools(t *testing.T) {
	internalToolNames := []string{"ask_user", "finish", "list_step_outputs", "read_final_result", "read_skill_resource", "read_step_output", "search_facts", "semantic_search", "update_checklist", "declare_step_complete", "store_fact", "tool_result_read", "delegate", "cancel_delegation", "declare_plan", "reflect", "batch"}

	for _, name := range internalToolNames {
		t.Run(name, func(t *testing.T) {
			if !IsInternalTool(name) {
				t.Errorf("IsInternalTool(%q) = false, want true", name)
			}
		})
	}
}

// TestIsInternalTool_ReturnsFalseForNonInternalTools tests that IsInternalTool()
// returns false for non-internal tools like "bash_exec".
func TestIsInternalTool_ReturnsFalseForNonInternalTools(t *testing.T) {
	nonInternalTools := []string{"bash_exec", "file_write", "file_read", "search_code", "edit_file"}

	for _, name := range nonInternalTools {
		t.Run(name, func(t *testing.T) {
			if IsInternalTool(name) {
				t.Errorf("IsInternalTool(%q) = true, want false", name)
			}
		})
	}
}

// TestInternalTool_BypassesPolicyAlwaysDeny tests that internal tools bypass
// policy resolution and execute even when the default policy is PolicyAlwaysDeny.
func TestInternalTool_BypassesPolicyAlwaysDeny(t *testing.T) {
	registry := NewToolRegistry()
	// Register a mock internal tool (using "finish" as the name)
	tool := &mockTool{
		name:          "finish",
		description:   "Finish the task",
		inputSchema:   json.RawMessage(`{"type":"object"}`),
		defaultPolicy: sdktools.PolicyAlwaysDeny, // Even with AlwaysDeny as tool's default
	}
	registry.Register(tool)

	// Set global default policy to AlwaysDeny
	registry.SetDefaultPolicy(sdktools.PolicyAlwaysDeny)

	// Set up a confirm func that should NOT be called for internal tools
	confirmCalled := false
	registry.SetConfirmFunc(func(ctx context.Context, req sdktools.ConfirmationRequest) (sdktools.ConfirmationResponse, error) {
		confirmCalled = true
		return sdktools.ConfirmAllowOnce, nil
	})

	ctx := context.Background()
	input := json.RawMessage(`{"status":"success"}`)

	// Execute the internal tool - it should bypass policy and execute successfully
	result, err := registry.Execute(ctx, "finish", input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Errorf("expected IsError to be false for internal tool, got true with content: %s", result.Content)
	}
	if result.Content != `{"status":"success"}` {
		t.Errorf("expected content %q, got %q", `{"status":"success"}`, result.Content)
	}
	if confirmCalled {
		t.Error("expected confirmFunc NOT to be called for internal tools")
	}
}

// TestInternalTool_BypassesPolicyUserConfirm tests that internal tools bypass
// policy resolution and execute even when the policy would require user confirmation.
func TestInternalTool_BypassesPolicyUserConfirm(t *testing.T) {
	registry := NewToolRegistry()
	// Register a mock internal tool
	tool := newMockTool("ask_user", "Ask the user a question")
	registry.Register(tool)

	// Set up a confirm func that should NOT be called for internal tools
	confirmCalled := false
	registry.SetConfirmFunc(func(ctx context.Context, req sdktools.ConfirmationRequest) (sdktools.ConfirmationResponse, error) {
		confirmCalled = true
		return sdktools.ConfirmAllowOnce, nil
	})

	ctx := context.Background()
	input := json.RawMessage(`{"question":"What is your name?"}`)

	// Execute the internal tool - it should bypass confirmation
	result, err := registry.Execute(ctx, "ask_user", input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Errorf("expected IsError to be false for internal tool, got true")
	}
	if confirmCalled {
		t.Error("expected confirmFunc NOT to be called for internal tools")
	}
}

// --- PreExecuteHook tests ---

// TestPreExecuteHook_BlocksUntilReleased verifies that the hook can block
// tool execution until released, and then the tool result is correct.
func TestPreExecuteHook_BlocksUntilReleased(t *testing.T) {
	registry := NewToolRegistry()
	tool := newMockReadOnlyTool("blocked_tool", "A tool that waits for hook")
	registry.Register(tool)

	gate := make(chan struct{})
	registry.SetPreExecuteHook(func(ctx context.Context, toolName, source string) error {
		<-gate // block until released
		return nil
	})

	ctx := context.Background()
	input := json.RawMessage(`{"data":"hello"}`)

	var result sdktools.ToolResult
	var execErr error
	done := make(chan struct{})
	go func() {
		result, execErr = registry.Execute(ctx, "blocked_tool", input)
		close(done)
	}()

	// Verify it hasn't completed yet
	select {
	case <-done:
		t.Fatal("Execute returned before hook was released")
	case <-time.After(50 * time.Millisecond):
		// expected: still blocked
	}

	// Release the gate
	close(gate)

	// Wait for completion
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Execute did not complete after hook was released")
	}

	if execErr != nil {
		t.Fatalf("unexpected error: %v", execErr)
	}
	if result.Content != `{"data":"hello"}` {
		t.Errorf("expected content %q, got %q", `{"data":"hello"}`, result.Content)
	}
	if result.IsError {
		t.Error("expected IsError to be false")
	}
}

// TestPreExecuteHook_ErrorPreventsExecution verifies that when the hook returns
// an error, tool execution is aborted and the error is returned in the result.
func TestPreExecuteHook_ErrorPreventsExecution(t *testing.T) {
	registry := NewToolRegistry()
	tool := newMockReadOnlyTool("error_hook_tool", "A tool with failing hook")
	registry.Register(tool)

	registry.SetPreExecuteHook(func(ctx context.Context, toolName, source string) error {
		return errors.New("indexing not ready")
	})

	ctx := context.Background()
	input := json.RawMessage(`{"data":"test"}`)

	result, err := registry.Execute(ctx, "error_hook_tool", input)
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if !result.IsError {
		t.Error("expected IsError to be true when hook returns error")
	}
	if !strings.Contains(result.Content, "indexing not ready") {
		t.Errorf("expected error message to contain 'indexing not ready', got: %s", result.Content)
	}
}

// TestPreExecuteHook_NotCalledForInternalTools verifies that the pre-execute
// hook is NOT invoked for internal tools (e.g., ask_user).
func TestPreExecuteHook_NotCalledForInternalTools(t *testing.T) {
	registry := NewToolRegistry()
	// Register a mock tool under an internal tool name
	tool := newMockReadOnlyTool("ask_user", "Internal ask_user tool")
	registry.Register(tool)

	var hookCalled bool
	registry.SetPreExecuteHook(func(ctx context.Context, toolName, source string) error {
		hookCalled = true
		return nil
	})

	ctx := context.Background()
	input := json.RawMessage(`{"question":"hello?"}`)

	result, err := registry.Execute(ctx, "ask_user", input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Error("expected IsError to be false for internal tool")
	}
	if hookCalled {
		t.Error("expected pre-execute hook NOT to be called for internal tool 'ask_user'")
	}
}

// TestPreExecuteHook_ReceivesCorrectSource verifies that the hook receives the
// correct source string for a tool registered with RegisterWithSource.
func TestPreExecuteHook_ReceivesCorrectSource(t *testing.T) {
	registry := NewToolRegistry()
	tool := newMockReadOnlyTool("mcp_search", "An MCP tool")
	registry.RegisterWithSource(tool, "mcp:test-server")

	var mu sync.Mutex
	var capturedName, capturedSource string
	registry.SetPreExecuteHook(func(ctx context.Context, toolName, source string) error {
		mu.Lock()
		capturedName = toolName
		capturedSource = source
		mu.Unlock()
		return nil
	})

	ctx := context.Background()
	input := json.RawMessage(`{"q":"test"}`)

	_, err := registry.Execute(ctx, "mcp_search", input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if capturedName != "mcp_search" {
		t.Errorf("expected hook toolName %q, got %q", "mcp_search", capturedName)
	}
	if capturedSource != "mcp:test-server" {
		t.Errorf("expected hook source %q, got %q", "mcp:test-server", capturedSource)
	}
}

// --- ToolFilter tests ---

// TestToolFilter_BlocksRegistration verifies that a filter can reject tools during registration.
func TestToolFilter_BlocksRegistration(t *testing.T) {
	registry := NewToolRegistry()
	registry.SetToolFilter(func(toolName, source string) bool {
		return source != "blocked-source"
	})

	tool := newMockTool("blocked_tool", "A tool from blocked source")
	registry.RegisterWithSource(tool, "blocked-source")

	_, ok := registry.Get("blocked_tool")
	if ok {
		t.Error("expected tool to be filtered out, but it was registered")
	}
	if len(registry.List()) != 0 {
		t.Errorf("expected 0 tools in registry, got %d", len(registry.List()))
	}
}

// TestToolFilter_AllowsRegistration verifies that a permissive filter allows tools.
func TestToolFilter_AllowsRegistration(t *testing.T) {
	registry := NewToolRegistry()
	registry.SetToolFilter(func(toolName, source string) bool {
		return true
	})

	tool := newMockTool("allowed_tool", "A tool from allowed source")
	registry.RegisterWithSource(tool, "allowed-source")

	_, ok := registry.Get("allowed_tool")
	if !ok {
		t.Error("expected tool to be registered, but it was not found")
	}
}

// TestToolFilter_NilAllowsAll verifies that a nil filter allows all tools (default behavior).
func TestToolFilter_NilAllowsAll(t *testing.T) {
	registry := NewToolRegistry()
	// No filter set

	tool := newMockTool("any_tool", "A tool with no filter")
	registry.RegisterWithSource(tool, "some-source")

	_, ok := registry.Get("any_tool")
	if !ok {
		t.Error("expected tool to be registered with nil filter, but it was not found")
	}
}

// --- ParamManager tests ---

// injectOnlyParamManager is a test helper that implements sdktools.ParamManager
// with a user-supplied InjectParams implementation. SanitizeSchema passes through unchanged.
type injectOnlyParamManager struct {
	injectFn func(ctx context.Context, toolName, source string, input json.RawMessage) json.RawMessage
}

func (m *injectOnlyParamManager) SanitizeSchema(_ string, schema json.RawMessage) json.RawMessage {
	return schema
}

func (m *injectOnlyParamManager) InjectParams(ctx context.Context, toolName, source string, input json.RawMessage) json.RawMessage {
	return m.injectFn(ctx, toolName, source, input)
}

// TestParamManager_ModifiesInput verifies that ParamManager transforms input before execution.
func TestParamManager_ModifiesInput(t *testing.T) {
	registry := NewToolRegistry()
	tool := newMockReadOnlyTool("echo_tool", "An echo tool")
	registry.Register(tool)

	registry.SetParamManager(&injectOnlyParamManager{
		injectFn: func(_ context.Context, _, _ string, input json.RawMessage) json.RawMessage {
			var m map[string]any
			if err := json.Unmarshal(input, &m); err != nil {
				return input
			}
			m["injected"] = "value"
			out, _ := json.Marshal(m)
			return out
		},
	})

	ctx := context.Background()
	input := json.RawMessage(`{"data":"test"}`)

	result, err := registry.Execute(ctx, "echo_tool", input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Errorf("expected IsError to be false, got true with content: %s", result.Content)
	}
	if !strings.Contains(result.Content, `"injected":"value"`) {
		t.Errorf("expected injected param in result content, got: %s", result.Content)
	}
	if !strings.Contains(result.Content, `"data":"test"`) {
		t.Errorf("expected original param preserved in result content, got: %s", result.Content)
	}
}

// TestParamManager_NilPassesThrough verifies that nil ParamManager passes input unchanged.
func TestParamManager_NilPassesThrough(t *testing.T) {
	registry := NewToolRegistry()
	tool := newMockReadOnlyTool("echo_tool", "An echo tool")
	registry.Register(tool)
	// No ParamManager set

	ctx := context.Background()
	input := json.RawMessage(`{"data":"test"}`)

	result, err := registry.Execute(ctx, "echo_tool", input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Content != `{"data":"test"}` {
		t.Errorf("expected content %q, got %q", `{"data":"test"}`, result.Content)
	}
}

// TestParamManager_RunsAfterPreExecuteHook verifies that ParamManager injection runs
// only after the pre-execute hook completes.
func TestParamManager_RunsAfterPreExecuteHook(t *testing.T) {
	registry := NewToolRegistry()
	tool := newMockReadOnlyTool("ordered_tool", "A tool for ordering test")
	registry.Register(tool)

	gate := make(chan struct{})
	registry.SetPreExecuteHook(func(ctx context.Context, toolName, source string) error {
		<-gate // block until released
		return nil
	})

	var mu sync.Mutex
	injectorCalled := false
	registry.SetParamManager(&injectOnlyParamManager{
		injectFn: func(_ context.Context, _, _ string, input json.RawMessage) json.RawMessage {
			mu.Lock()
			injectorCalled = true
			mu.Unlock()
			return input
		},
	})

	ctx := context.Background()
	input := json.RawMessage(`{"data":"test"}`)

	done := make(chan struct{})
	go func() {
		_, _ = registry.Execute(ctx, "ordered_tool", input)
		close(done)
	}()

	// While hook is blocked, injector should NOT have been called
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	if injectorCalled {
		t.Error("expected injector NOT to be called while hook is blocked")
	}
	mu.Unlock()

	// Release the hook
	close(gate)

	// Wait for completion
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Execute did not complete after hook was released")
	}

	mu.Lock()
	defer mu.Unlock()
	if !injectorCalled {
		t.Error("expected injector to be called after hook completed")
	}
}

// TestAutoApproval_AlwaysDenyRespected tests that PolicyAlwaysDeny is still
// respected even when all paths are within the workspace.
func TestAutoApproval_AlwaysDenyRespected(t *testing.T) {
	registry := NewToolRegistry()
	tool := &mockTool{
		name:          "always_deny",
		description:   "A tool with PolicyAlwaysDeny",
		inputSchema:   json.RawMessage(`{"type":"object"}`),
		defaultPolicy: sdktools.PolicyAlwaysDeny,
	}
	registry.Register(tool)

	confirmCalled := false
	registry.SetConfirmFunc(func(ctx context.Context, req sdktools.ConfirmationRequest) (sdktools.ConfirmationResponse, error) {
		confirmCalled = true
		return sdktools.ConfirmAllowOnce, nil
	})

	ctx := context.Background()
	ctx = sdktools.WithWorkspacePath(ctx, "/workspace")
	input := json.RawMessage(`{"path": "/workspace/file.txt"}`)

	result, err := registry.Execute(ctx, "always_deny", input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected IsError to be true for PolicyAlwaysDeny")
	}
	if !strings.Contains(result.Content, "blocked by security policy") {
		t.Errorf("expected security policy error, got: %s", result.Content)
	}
	if confirmCalled {
		t.Error("expected confirmFunc NOT to be called for PolicyAlwaysDeny")
	}
}
