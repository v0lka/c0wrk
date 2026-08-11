package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/v0lka/sp4rk/llm"
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

type scriptedJudgeProvider struct {
	mu       sync.Mutex
	response *llm.ChatResponse
	err      error
	calls    int
}

func (p *scriptedJudgeProvider) ChatCompletion(context.Context, llm.ChatRequest) (*llm.ChatResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	return p.response, p.err
}

func (p *scriptedJudgeProvider) Name() string { return "scripted-judge" }

func (p *scriptedJudgeProvider) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

func newStrictJudge(response string, err error) (*sdktools.ToolJudge, *scriptedJudgeProvider) {
	provider := &scriptedJudgeProvider{err: err}
	if response != "" {
		provider.response = &llm.ChatResponse{Message: llm.Message{Content: response}}
	}
	return sdktools.NewToolJudge(provider, "test-model", 1, nil), provider
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
	// newMockTool defaults to PolicyUserConfirm; since nil ConfirmFunc is now
	// fail-closed, install an auto-allow callback for this happy-path test.
	registry.SetConfirmFunc(func(_ context.Context, _ sdktools.ConfirmationRequest) (sdktools.ConfirmationResponse, error) {
		return sdktools.ConfirmAllowOnce, nil
	})

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
	// "mutating" is not a known mutating tool, so it surfaces the generic
	// fallback reason. The denial now always carries the reason the call was
	// flagged for confirmation (previously it was only appended for judge
	// flags, leaving plain user_confirm denials unexplained).
	if !strings.HasPrefix(result.Content, "Tool execution denied by user.") {
		t.Errorf("expected content to start with 'Tool execution denied by user.', got %q", result.Content)
	}
	if !strings.Contains(result.Content, "Reason for confirmation request:") {
		t.Errorf("expected denial to include the confirmation reason, got %q", result.Content)
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

// TestPolicyUserConfirm_SurfacesDefaultReason verifies that when a tool's
// resolved policy is PolicyUserConfirm and no richer reason (symlink traversal,
// judge flag, or auto-approve denial) is available, the confirmation request
// still carries a human-readable reason explaining why approval is needed —
// instead of the empty string that previously left the dialog unexplained.
func TestPolicyUserConfirm_SurfacesDefaultReason(t *testing.T) {
	registry := NewToolRegistry()
	registry.Register(newMockTool("write_file", "writes a file"))

	var gotReason string
	registry.SetConfirmFunc(func(_ context.Context, req sdktools.ConfirmationRequest) (sdktools.ConfirmationResponse, error) {
		gotReason = req.JudgeReasoning
		return sdktools.ConfirmAllowOnce, nil
	})

	if _, err := registry.Execute(context.Background(), "write_file", json.RawMessage(`{}`)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotReason == "" {
		t.Fatal("expected a non-empty human-readable reason for a PolicyUserConfirm tool, got empty string")
	}
	if gotReason != "This tool creates or overwrites a file." {
		t.Errorf("expected write_file-specific reason, got %q", gotReason)
	}
}

// TestPolicyUserConfirm_DefaultReason_GenericFallback verifies that a tool name
// without a specific mapping falls back to the generic mutating-action reason.
func TestPolicyUserConfirm_DefaultReason_GenericFallback(t *testing.T) {
	registry := NewToolRegistry()
	registry.Register(newMockTool("some_custom_tool", "custom"))

	var gotReason string
	registry.SetConfirmFunc(func(_ context.Context, req sdktools.ConfirmationRequest) (sdktools.ConfirmationResponse, error) {
		gotReason = req.JudgeReasoning
		return sdktools.ConfirmAllowOnce, nil
	})

	if _, err := registry.Execute(context.Background(), "some_custom_tool", json.RawMessage(`{}`)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(gotReason, "requires your approval") {
		t.Errorf("expected generic fallback reason, got %q", gotReason)
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

// TestAutoApproval_AllowedRoot verifies that a PolicyAlwaysAllow tool with a
// path inside an additional allowed root (auxiliary work directory) auto-approves
// without confirmation — the same treatment as workspace/temp paths, now that
// auto-approval consults the full set of session roots.
func TestAutoApproval_AllowedRoot(t *testing.T) {
	registry := NewToolRegistry()
	tool := newMockReadOnlyTool("readonly", "A read-only tool") // defaultPolicy: PolicyAlwaysAllow
	registry.Register(tool)

	confirmCalled := false
	registry.SetConfirmFunc(func(ctx context.Context, req sdktools.ConfirmationRequest) (sdktools.ConfirmationResponse, error) {
		confirmCalled = true
		return sdktools.ConfirmAllowOnce, nil
	})

	ctx := context.Background()
	ctx = sdktools.WithAllowedRoots(ctx, []string{"/aux"})
	input := json.RawMessage(`{"path": "/aux/file.txt"}`)

	_, err := registry.Execute(ctx, "readonly", input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if confirmCalled {
		t.Error("expected confirmFunc NOT to be called for PolicyAlwaysAllow tools with paths inside an allowed root")
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
	internalToolNames := []string{"ask_user", "finish", "list_step_outputs", "read_final_result", "read_skill_resource", "read_step_output", "read_attachment", "search_facts", "semantic_search", "update_checklist", "declare_step_complete", "store_fact", "tool_result_read", "delegate", "cancel_delegation", "declare_plan", "reflect", "batch"}

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

// --- Goal-mode tool gating tests ---

// TestIsGoalModeTool_GoalSpecificTools verifies the three goal-mode-only tools
// are recognized as goal-mode tools.
func TestIsGoalModeTool_GoalSpecificTools(t *testing.T) {
	for _, name := range []string{"propose_goal", "declare_goal_status", "declare_verification"} {
		if !IsGoalModeTool(name) {
			t.Errorf("IsGoalModeTool(%q) = false, want true", name)
		}
	}
}

// TestIsGoalModeTool_GeneralCoordinationToolsExcluded verifies the general
// Conductor coordination tools (delegate, declare_plan, reflect, ...) are NOT
// goal-mode tools — they are used in normal (non-goal) Conductor runs too, so
// they must never be gated by goal mode.
func TestIsGoalModeTool_GeneralCoordinationToolsExcluded(t *testing.T) {
	for _, name := range []string{"delegate", "cancel_delegation", "declare_plan", "execute_plan", "reflect", "declare_step_complete", "finish", "ask_user", "read_file"} {
		if IsGoalModeTool(name) {
			t.Errorf("IsGoalModeTool(%q) = true, want false (general tool, not goal-specific)", name)
		}
	}
}

// TestGoalModeTools_AreAllInternal is a completeness guard: every goal-mode
// tool MUST also be an internal tool. Internal classification is what hides
// them from the security UI and exempts them from policy/confirmation. If a
// goal-mode tool ever loses its internal status, the security tab would show
// it and policies would apply, violating the goal-mode tool contract.
func TestGoalModeTools_AreAllInternal(t *testing.T) {
	for name := range goalModeTools {
		if !IsInternalTool(name) {
			t.Errorf("goal-mode tool %q is NOT classified as internal — it must be internal to be hidden from the security UI and exempt from policies", name)
		}
	}
}

// TestDeclareVerification_IsInternal guards the regression where declare_verification
// was documented as internal but missing from the internalTools map. Its doc
// comment and constructor (PolicyAlwaysAllow) promise internal-tool behavior;
// IsInternalTool must honor that.
func TestDeclareVerification_IsInternal(t *testing.T) {
	if !IsInternalTool("declare_verification") {
		t.Error("declare_verification must be classified as internal (hidden from security UI, policy/judge-exempt)")
	}
}

// TestStripGoalModeTools_RemovesGoalSpecificTools verifies the helper strips
// the three goal-only tools while leaving everything else (including general
// Conductor coordination tools) untouched.
func TestStripGoalModeTools_RemovesGoalSpecificTools(t *testing.T) {
	in := []sdktools.ToolDescriptor{
		{Name: "read_file"}, {Name: "bash_exec"}, {Name: "finish"},
		{Name: "delegate"}, {Name: "declare_plan"}, {Name: "reflect"},
		{Name: "propose_goal"}, {Name: "declare_goal_status"}, {Name: "declare_verification"},
	}
	got := StripGoalModeTools(in)
	names := make(map[string]bool, len(got))
	for _, t := range got {
		names[t.Name] = true
	}
	for _, removed := range []string{"propose_goal", "declare_goal_status", "declare_verification"} {
		if names[removed] {
			t.Errorf("StripGoalModeTools: goal tool %q should be removed", removed)
		}
	}
	for _, kept := range []string{"read_file", "bash_exec", "finish", "delegate", "declare_plan", "reflect"} {
		if !names[kept] {
			t.Errorf("StripGoalModeTools: non-goal tool %q should be kept", kept)
		}
	}
}

// TestStripGoalModeTools_EmptyAndNilInputs verifies the helper is safe for
// edge-case inputs (the orchestrator may call it with an empty list).
func TestStripGoalModeTools_EmptyAndNilInputs(t *testing.T) {
	if got := StripGoalModeTools(nil); got != nil {
		t.Errorf("StripGoalModeTools(nil) = %v, want nil", got)
	}
	if got := StripGoalModeTools([]sdktools.ToolDescriptor{}); len(got) != 0 {
		t.Errorf("StripGoalModeTools(empty) returned %d items, want 0", len(got))
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

func TestSmartApprove_UserConfirmFlow(t *testing.T) {
	tests := []struct {
		name             string
		enabled          bool
		judgeResponse    string
		judgeErr         error
		setJudge         bool
		wantConfirm      bool
		wantDisableJudge bool
		wantJudgeCalls   int
		wantResultError  bool
	}{
		{name: "off preserves legacy confirmation", judgeResponse: "VERDICT: ALLOW\nREASON: safe", setJudge: true, wantConfirm: true},
		{name: "strict allow executes without UI", enabled: true, judgeResponse: "VERDICT: ALLOW\nREASON: safe and relevant", setJudge: true, wantJudgeCalls: 1},
		{name: "strict confirm uses manual UI", enabled: true, judgeResponse: "VERDICT: CONFIRM\nREASON: destructive operation", setJudge: true, wantConfirm: true, wantDisableJudge: true, wantJudgeCalls: 1},
		{name: "unparseable uses manual UI", enabled: true, judgeResponse: "probably fine", setJudge: true, wantConfirm: true, wantDisableJudge: true, wantJudgeCalls: 1},
		{name: "provider error uses manual UI", enabled: true, judgeErr: errors.New("provider failed"), setJudge: true, wantConfirm: true, wantDisableJudge: true, wantJudgeCalls: 1},
		{name: "unavailable judge uses manual UI", enabled: true, wantConfirm: true, wantDisableJudge: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			registry := NewToolRegistry()
			registry.SetSmartApprove(tt.enabled)
			registry.RegisterWithSource(newMockTool("mutating", "mutates"), "mcp:test-server")

			judge, provider := newStrictJudge(tt.judgeResponse, tt.judgeErr)
			if tt.setJudge {
				registry.SetJudge(judge)
			}

			var gotRequest sdktools.ConfirmationRequest
			confirmCalled := false
			registry.SetConfirmFunc(func(_ context.Context, req sdktools.ConfirmationRequest) (sdktools.ConfirmationResponse, error) {
				confirmCalled = true
				gotRequest = req
				return sdktools.ConfirmAllowOnce, nil
			})

			ctx := sdktools.WithTaskContext(context.Background(), "update the project")
			result, err := registry.Execute(ctx, "mutating", json.RawMessage(`{"secret":"do-not-log"}`))
			if err != nil {
				t.Fatalf("Execute() error = %v", err)
			}
			if result.IsError != tt.wantResultError {
				t.Errorf("result.IsError = %v, want %v", result.IsError, tt.wantResultError)
			}
			if confirmCalled != tt.wantConfirm {
				t.Errorf("confirm called = %v, want %v", confirmCalled, tt.wantConfirm)
			}
			if confirmCalled && gotRequest.DisableJudge != tt.wantDisableJudge {
				t.Errorf("DisableJudge = %v, want %v", gotRequest.DisableJudge, tt.wantDisableJudge)
			}
			if got := provider.callCount(); got != tt.wantJudgeCalls {
				t.Errorf("strict judge calls = %d, want %d", got, tt.wantJudgeCalls)
			}
		})
	}
}

func TestSmartApprove_WorkspaceAutoApproveHasPriority(t *testing.T) {
	registry := NewToolRegistry()
	registry.SetAutoApproveWorkspaceWrites(true)
	registry.SetSmartApprove(true)
	registry.Register(newMockConfirmJudgerTool("write_file", sdktools.PolicyUserConfirm, true, "within roots"))
	judge, provider := newStrictJudge("VERDICT: CONFIRM\nREASON: should not run", nil)
	registry.SetJudge(judge)

	confirmCalled := false
	registry.SetConfirmFunc(func(context.Context, sdktools.ConfirmationRequest) (sdktools.ConfirmationResponse, error) {
		confirmCalled = true
		return sdktools.ConfirmAllowOnce, nil
	})

	ctx := sdktools.WithWorkspacePath(context.Background(), "/workspace")
	result, err := registry.Execute(ctx, "write_file", json.RawMessage(`{"path":"/workspace/file.txt"}`))
	if err != nil || result.IsError {
		t.Fatalf("Execute() = (%+v, %v), want success", result, err)
	}
	if confirmCalled {
		t.Error("workspace auto-approval must bypass manual confirmation")
	}
	if got := provider.callCount(); got != 0 {
		t.Errorf("strict judge calls = %d, want 0", got)
	}
}

func TestSmartApprove_OnlyEffectiveUserConfirm(t *testing.T) {
	for _, tt := range []struct {
		name   string
		policy sdktools.ToolPolicy
	}{
		{name: "always_allow", policy: sdktools.PolicyAlwaysAllow},
		{name: "always_deny", policy: sdktools.PolicyAlwaysDeny},
	} {
		t.Run(tt.name, func(t *testing.T) {
			policy := tt.policy
			registry := NewToolRegistry()
			registry.SetSmartApprove(true)
			tool := newMockTool("policy_tool", "policy test")
			tool.defaultPolicy = policy
			registry.Register(tool)
			judge, provider := newStrictJudge("VERDICT: ALLOW\nREASON: safe", nil)
			registry.SetJudge(judge)

			result, err := registry.Execute(context.Background(), "policy_tool", json.RawMessage(`{"data":"test"}`))
			if err != nil {
				t.Fatalf("Execute() error = %v", err)
			}
			if policy == sdktools.PolicyAlwaysDeny && !result.IsError {
				t.Error("always_deny must remain blocked")
			}
			if policy == sdktools.PolicyAlwaysAllow && result.IsError {
				t.Error("always_allow must retain legacy execution")
			}
			if got := provider.callCount(); got != 0 {
				t.Errorf("strict judge calls = %d, want 0", got)
			}
		})
	}
}

func TestSmartApprove_LogsStructuredVerdictWithoutRawArgs(t *testing.T) {
	var logs bytes.Buffer
	registry := NewToolRegistry()
	registry.SetLogger(slog.New(slog.NewJSONHandler(&logs, nil)))
	registry.SetSmartApprove(true)
	registry.Register(newMockTool("mutating", "mutates"))
	judge, _ := newStrictJudge("VERDICT: ALLOW\nREASON: safe", nil)
	registry.SetJudge(judge)

	const secret = "sensitive-tool-argument"
	result, err := registry.Execute(context.Background(), "mutating", json.RawMessage(`{"secret":"`+secret+`"}`))
	if err != nil || result.IsError {
		t.Fatalf("Execute() = (%+v, %v), want success", result, err)
	}
	logText := logs.String()
	if strings.Contains(logText, secret) {
		t.Fatalf("structured security log leaked raw tool arguments: %s", logText)
	}
	for _, field := range []string{`"msg":"security: smart approve verdict"`, `"tool":"mutating"`, `"verdict":"ALLOW"`} {
		if !strings.Contains(logText, field) {
			t.Errorf("structured security log missing %s: %s", field, logText)
		}
	}
}

func TestConfirmFunc_NilUserConfirmFailsClosed(t *testing.T) {
	registry := NewToolRegistry()
	registry.Register(newMockTool("mutating", "mutates"))

	result, err := registry.Execute(context.Background(), "mutating", json.RawMessage(`{"data":"test"}`))
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !result.IsError {
		t.Fatal("nil ConfirmFunc must deny PolicyUserConfirm execution")
	}
	if !strings.Contains(result.Content, "confirmation is unavailable") {
		t.Errorf("unexpected denial content: %q", result.Content)
	}
}

// --- PostExecuteHook tests ---

// TestPostExecuteHook_CalledAfterSuccessfulExecution verifies that the
// post-execute hook is called with the correct tool name and result after
// a successful tool execution.
func TestPostExecuteHook_CalledAfterSuccessfulExecution(t *testing.T) {
	registry := NewToolRegistry()
	tool := newMockReadOnlyTool("my_tool", "A tool")
	registry.Register(tool)

	var mu sync.Mutex
	var capturedName string
	var capturedResult sdktools.ToolResult
	var capturedErr error
	registry.SetPostExecuteHook(func(_ context.Context, toolName string, res sdktools.ToolResult, hookErr error) {
		mu.Lock()
		capturedName = toolName
		capturedResult = res
		capturedErr = hookErr
		mu.Unlock()
	})

	ctx := context.Background()
	input := json.RawMessage(`{"data":"hello"}`)

	result, err := registry.Execute(ctx, "my_tool", input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if capturedName != "my_tool" {
		t.Errorf("expected hook toolName %q, got %q", "my_tool", capturedName)
	}
	if capturedResult.Content != result.Content {
		t.Errorf("expected hook result content %q, got %q", result.Content, capturedResult.Content)
	}
	if capturedResult.IsError {
		t.Error("expected hook result IsError to be false")
	}
	if capturedErr != nil {
		t.Errorf("expected hook err to be nil, got %v", capturedErr)
	}
}

// TestPostExecuteHook_NotCalledForInternalTools verifies that the hook is
// NOT invoked for internal tools (e.g. ask_user, finish).
func TestPostExecuteHook_NotCalledForInternalTools(t *testing.T) {
	registry := NewToolRegistry()
	// Register a mock tool under an internal tool name.
	tool := newMockReadOnlyTool("ask_user", "Internal ask_user tool")
	registry.Register(tool)

	var hookCalled bool
	registry.SetPostExecuteHook(func(context.Context, string, sdktools.ToolResult, error) {
		hookCalled = true
	})

	ctx := context.Background()
	input := json.RawMessage(`{"question":"hello?"}`)

	_, err := registry.Execute(ctx, "ask_user", input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if hookCalled {
		t.Error("expected post-execute hook NOT to be called for internal tool 'ask_user'")
	}
}

// TestPostExecuteHook_CalledOnPolicyDeny verifies that the hook IS called
// (with an error result) when a tool is blocked by policy. This is correct
// because the defer covers all return paths — the hook can filter on
// result.IsError.
func TestPostExecuteHook_CalledOnPolicyDeny(t *testing.T) {
	registry := NewToolRegistry()
	tool := newMockTool("denied_tool", "A tool with deny policy")
	registry.Register(tool)
	registry.SetPolicyOverrides(map[string]sdktools.ToolPolicy{"denied_tool": sdktools.PolicyAlwaysDeny})

	var mu sync.Mutex
	var capturedName string
	var capturedIsError bool
	registry.SetPostExecuteHook(func(_ context.Context, toolName string, res sdktools.ToolResult, _ error) {
		mu.Lock()
		capturedName = toolName
		capturedIsError = res.IsError
		mu.Unlock()
	})

	ctx := context.Background()
	input := json.RawMessage(`{"data":"test"}`)

	_, err := registry.Execute(ctx, "denied_tool", input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if capturedName != "denied_tool" {
		t.Errorf("expected hook toolName %q, got %q", "denied_tool", capturedName)
	}
	if !capturedIsError {
		t.Error("expected hook result IsError to be true for denied tool")
	}
}

// TestPostExecuteHook_PreservedInClone verifies that the hook is shared
// with cloned registries (per-session clones inherit the hook).
func TestPostExecuteHook_PreservedInClone(t *testing.T) {
	registry := NewToolRegistry()
	tool := newMockReadOnlyTool("cloned_tool", "A tool")
	registry.Register(tool)

	var hookCalled bool
	registry.SetPostExecuteHook(func(context.Context, string, sdktools.ToolResult, error) {
		hookCalled = true
	})

	cloned := registry.Clone()

	ctx := context.Background()
	input := json.RawMessage(`{"data":"test"}`)

	_, err := cloned.Execute(ctx, "cloned_tool", input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !hookCalled {
		t.Error("expected post-execute hook to be called in cloned registry")
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

// TestValidateRequiredFields verifies the centralized schema required-field
// validator (ASI02-R2 defense-in-depth).
func TestValidateRequiredFields(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","required":["path","content"],"properties":{}}`)

	tests := []struct {
		name  string
		input string
		want  int // number of missing fields
	}{
		{"both present", `{"path":"/x","content":"y"}`, 0},
		{"path missing", `{"content":"y"}`, 1},
		{"content missing", `{"path":"/x"}`, 1},
		{"both missing", `{}`, 2},
		{"non-object input", `"rawstring"`, 0}, // fail-safe: skip
		{"empty input", ``, 0},                 // fail-safe: skip
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			missing := validateRequiredFields(schema, json.RawMessage(tt.input))
			if len(missing) != tt.want {
				t.Errorf("got %d missing (%v), want %d", len(missing), missing, tt.want)
			}
		})
	}

	// Schema with no "required" → always empty (fail-safe).
	noReq := json.RawMessage(`{"type":"object","properties":{}}`)
	if m := validateRequiredFields(noReq, json.RawMessage(`{}`)); len(m) != 0 {
		t.Errorf("expected no missing for schema without required, got %v", m)
	}

	// Unparseable schema → fail-safe empty.
	if m := validateRequiredFields(json.RawMessage(`{bad`), json.RawMessage(`{}`)); len(m) != 0 {
		t.Errorf("expected no missing for unparseable schema, got %v", m)
	}
}
