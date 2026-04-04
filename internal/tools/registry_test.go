package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/user/agent/internal/llm"
)

// mockTool is a simple echo tool for testing.
type mockTool struct {
	name          string
	description   string
	inputSchema   json.RawMessage
	defaultPolicy ToolPolicy
}

func newMockTool(name, description string) *mockTool {
	return &mockTool{
		name:          name,
		description:   description,
		inputSchema:   json.RawMessage(`{"type":"object","properties":{"input":{"type":"string"}}}`),
		defaultPolicy: PolicyUserConfirm,
	}
}

func newMockReadOnlyTool(name, description string) *mockTool {
	return &mockTool{
		name:          name,
		description:   description,
		inputSchema:   json.RawMessage(`{"type":"object","properties":{"input":{"type":"string"}}}`),
		defaultPolicy: PolicyAlwaysAllow,
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

func (m *mockTool) DefaultPolicy() ToolPolicy {
	return m.defaultPolicy
}

func (m *mockTool) Execute(ctx context.Context, input json.RawMessage) (ToolResult, error) {
	// Simple echo: return the input as content
	return ToolResult{
		Content: string(input),
		IsError: false,
	}, nil
}

// mockJudgerTool is a mock tool that implements ToolJudger for testing.
type mockJudgerTool struct {
	mockTool
	judgeAllow     bool
	judgeReasoning string
}

func (m *mockJudgerTool) Judge(ctx context.Context, input json.RawMessage) (allow bool, reasoning string) {
	return m.judgeAllow, m.judgeReasoning
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

	// Execute non-existent tool should return error
	_, err := registry.Execute(ctx, "nonexistent", input)
	if err == nil {
		t.Fatal("expected error when executing non-existent tool, got nil")
	}
	expectedErrMsg := "tool not found: nonexistent"
	if err.Error() != expectedErrMsg {
		t.Errorf("expected error message %q, got %q", expectedErrMsg, err.Error())
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
	registry.SetConfirmFunc(func(ctx context.Context, req ConfirmationRequest) (ConfirmationResponse, error) {
		confirmCalled = true
		if req.ToolName != "mutating" {
			t.Errorf("expected tool name 'mutating', got %q", req.ToolName)
		}
		return ConfirmAllowOnce, nil
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

	registry.SetConfirmFunc(func(ctx context.Context, req ConfirmationRequest) (ConfirmationResponse, error) {
		return ConfirmDeny, nil
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

	registry.SetConfirmFunc(func(ctx context.Context, req ConfirmationRequest) (ConfirmationResponse, error) {
		return ConfirmDenyAndStop, nil
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
	registry.SetConfirmFunc(func(ctx context.Context, req ConfirmationRequest) (ConfirmationResponse, error) {
		confirmCalled = true
		return ConfirmAllowOnce, nil
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
	registry.SetConfirmFunc(func(ctx context.Context, req ConfirmationRequest) (ConfirmationResponse, error) {
		return 0, expectedErr
	})

	ctx := context.Background()
	input := json.RawMessage(`{"data":"test"}`)

	_, err := registry.Execute(ctx, "mutating", input)
	if !errors.Is(err, expectedErr) {
		t.Fatalf("expected error %v, got %v", expectedErr, err)
	}
}

func TestJudge_AllowSkipsConfirm(t *testing.T) {
	registry := NewToolRegistry()
	// Use PolicyAuto tool so the judge is invoked
	tool := &mockTool{
		name:          "auto_tool",
		description:   "A tool with PolicyAuto",
		inputSchema:   json.RawMessage(`{"type":"object"}`),
		defaultPolicy: PolicyAuto,
	}
	registry.Register(tool)

	// Set up a judge that returns VerdictAllow
	mockProvider := &mockLLMProvider{
		response: &llm.ChatResponse{
			Message: llm.Message{Content: "VERDICT: ALLOW\nREASON: Safe operation"},
		},
	}
	judge := NewToolJudge(mockProvider, "test-model")
	registry.SetJudge(judge)

	// Set a confirmFunc that should NOT be called
	confirmCalled := false
	registry.SetConfirmFunc(func(ctx context.Context, req ConfirmationRequest) (ConfirmationResponse, error) {
		confirmCalled = true
		return ConfirmAllowOnce, nil
	})

	ctx := context.Background()
	input := json.RawMessage(`{"data":"test"}`)

	result, err := registry.Execute(ctx, "auto_tool", input)
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
		t.Error("expected confirmFunc NOT to be called when judge returns VerdictAllow")
	}
	if mockProvider.callCount != 1 {
		t.Errorf("expected judge to be called once, got %d calls", mockProvider.callCount)
	}
}

func TestJudge_ConfirmCallsConfirmFunc(t *testing.T) {
	registry := NewToolRegistry()
	// Use PolicyAuto tool so the judge is invoked
	tool := &mockTool{
		name:          "auto_tool",
		description:   "A tool with PolicyAuto",
		inputSchema:   json.RawMessage(`{"type":"object"}`),
		defaultPolicy: PolicyAuto,
	}
	registry.Register(tool)

	// Set up a judge that returns VerdictConfirm
	mockProvider := &mockLLMProvider{
		response: &llm.ChatResponse{
			Message: llm.Message{Content: "VERDICT: CONFIRM\nREASON: Potentially dangerous"},
		},
	}
	judge := NewToolJudge(mockProvider, "test-model")
	registry.SetJudge(judge)

	// Set a confirmFunc that returns ConfirmAllowOnce
	confirmCalled := false
	var receivedReasoning string
	registry.SetConfirmFunc(func(ctx context.Context, req ConfirmationRequest) (ConfirmationResponse, error) {
		confirmCalled = true
		receivedReasoning = req.JudgeReasoning
		if req.ToolName != "auto_tool" {
			t.Errorf("expected tool name 'auto_tool', got %q", req.ToolName)
		}
		return ConfirmAllowOnce, nil
	})

	ctx := context.Background()
	input := json.RawMessage(`{"data":"test"}`)

	result, err := registry.Execute(ctx, "auto_tool", input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Error("expected IsError to be false")
	}
	if result.Content != `{"data":"test"}` {
		t.Errorf("expected content %q, got %q", `{"data":"test"}`, result.Content)
	}
	if !confirmCalled {
		t.Error("expected confirmFunc to be called when judge returns VerdictConfirm")
	}
	if receivedReasoning != "Potentially dangerous" {
		t.Errorf("expected reasoning 'Potentially dangerous', got %q", receivedReasoning)
	}
	if mockProvider.callCount != 1 {
		t.Errorf("expected judge to be called once, got %d calls", mockProvider.callCount)
	}
}

func TestJudge_NilJudge_ExistingBehavior(t *testing.T) {
	registry := NewToolRegistry()
	// Use PolicyAuto tool so the judge path is triggered (but no judge set)
	tool := &mockTool{
		name:          "auto_tool",
		description:   "A tool with PolicyAuto",
		inputSchema:   json.RawMessage(`{"type":"object"}`),
		defaultPolicy: PolicyAuto,
	}
	registry.Register(tool)

	// Do NOT set a judge - verify existing behavior is preserved

	confirmCalled := false
	registry.SetConfirmFunc(func(ctx context.Context, req ConfirmationRequest) (ConfirmationResponse, error) {
		confirmCalled = true
		return ConfirmAllowOnce, nil
	})

	ctx := context.Background()
	input := json.RawMessage(`{"data":"test"}`)

	result, err := registry.Execute(ctx, "auto_tool", input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Error("expected IsError to be false")
	}
	if result.Content != `{"data":"test"}` {
		t.Errorf("expected content %q, got %q", `{"data":"test"}`, result.Content)
	}
	if !confirmCalled {
		t.Error("expected confirmFunc to be called when no judge is set")
	}
}

// TestPolicyAlwaysAllow_ExecutesImmediately tests that PolicyAlwaysAllow executes without confirmation.
func TestPolicyAlwaysAllow_ExecutesImmediately(t *testing.T) {
	registry := NewToolRegistry()
	tool := newMockReadOnlyTool("always_allow", "A tool with PolicyAlwaysAllow")
	registry.Register(tool)

	confirmCalled := false
	registry.SetConfirmFunc(func(ctx context.Context, req ConfirmationRequest) (ConfirmationResponse, error) {
		confirmCalled = true
		return ConfirmAllowOnce, nil
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
		defaultPolicy: PolicyAlwaysDeny,
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
	registry.SetConfirmFunc(func(ctx context.Context, req ConfirmationRequest) (ConfirmationResponse, error) {
		confirmCalled = true
		return ConfirmAllowOnce, nil
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

// TestPolicyAuto_ToolJudgerAllows tests PolicyAuto with ToolJudger that allows execution.
func TestPolicyAuto_ToolJudgerAllows(t *testing.T) {
	registry := NewToolRegistry()
	tool := &mockJudgerTool{
		mockTool: mockTool{
			name:          "auto_tool",
			description:   "A tool with PolicyAuto",
			inputSchema:   json.RawMessage(`{"type":"object"}`),
			defaultPolicy: PolicyAuto,
		},
		judgeAllow:     true,
		judgeReasoning: "safe operation",
	}
	registry.Register(tool)

	confirmCalled := false
	registry.SetConfirmFunc(func(ctx context.Context, req ConfirmationRequest) (ConfirmationResponse, error) {
		confirmCalled = true
		return ConfirmAllowOnce, nil
	})

	ctx := context.Background()
	input := json.RawMessage(`{"data":"test"}`)

	result, err := registry.Execute(ctx, "auto_tool", input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Error("expected IsError to be false")
	}
	if confirmCalled {
		t.Error("expected confirmFunc NOT to be called when ToolJudger allows")
	}
}

// TestPolicyAuto_ToolJudgerDeniesWithReason tests PolicyAuto with ToolJudger that denies with reason.
func TestPolicyAuto_ToolJudgerDeniesWithReason(t *testing.T) {
	registry := NewToolRegistry()
	tool := &mockJudgerTool{
		mockTool: mockTool{
			name:          "auto_tool",
			description:   "A tool with PolicyAuto",
			inputSchema:   json.RawMessage(`{"type":"object"}`),
			defaultPolicy: PolicyAuto,
		},
		judgeAllow:     false,
		judgeReasoning: "potentially dangerous operation detected",
	}
	registry.Register(tool)

	confirmCalled := false
	var receivedReasoning string
	registry.SetConfirmFunc(func(ctx context.Context, req ConfirmationRequest) (ConfirmationResponse, error) {
		confirmCalled = true
		receivedReasoning = req.JudgeReasoning
		return ConfirmAllowOnce, nil
	})

	ctx := context.Background()
	input := json.RawMessage(`{"data":"test"}`)

	result, err := registry.Execute(ctx, "auto_tool", input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Error("expected IsError to be false")
	}
	if !confirmCalled {
		t.Error("expected confirmFunc to be called when ToolJudger denies with reason")
	}
	if receivedReasoning != "potentially dangerous operation detected" {
		t.Errorf("expected reasoning %q, got %q", "potentially dangerous operation detected", receivedReasoning)
	}
}

// TestPolicyAuto_ToolJudgerDefersToLLM tests PolicyAuto with ToolJudger that defers (empty reasoning).
func TestPolicyAuto_ToolJudgerDefersToLLM(t *testing.T) {
	registry := NewToolRegistry()
	tool := &mockJudgerTool{
		mockTool: mockTool{
			name:          "auto_tool",
			description:   "A tool with PolicyAuto",
			inputSchema:   json.RawMessage(`{"type":"object"}`),
			defaultPolicy: PolicyAuto,
		},
		judgeAllow:     false,
		judgeReasoning: "", // Empty reasoning defers to LLM Judge
	}
	registry.Register(tool)

	// Set up LLM Judge that allows
	mockProvider := &mockLLMProvider{
		response: &llm.ChatResponse{
			Message: llm.Message{Content: "VERDICT: ALLOW\nREASON: Safe operation"},
		},
	}
	judge := NewToolJudge(mockProvider, "test-model")
	registry.SetJudge(judge)

	confirmCalled := false
	registry.SetConfirmFunc(func(ctx context.Context, req ConfirmationRequest) (ConfirmationResponse, error) {
		confirmCalled = true
		return ConfirmAllowOnce, nil
	})

	ctx := context.Background()
	input := json.RawMessage(`{"data":"test"}`)

	result, err := registry.Execute(ctx, "auto_tool", input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Error("expected IsError to be false")
	}
	if confirmCalled {
		t.Error("expected confirmFunc NOT to be called when LLM Judge allows")
	}
	if mockProvider.callCount != 1 {
		t.Errorf("expected LLM Judge to be called once, got %d", mockProvider.callCount)
	}
}

// TestPolicyAuto_WithoutToolJudgerUsesLLM tests PolicyAuto without ToolJudger uses LLM Judge.
func TestPolicyAuto_WithoutToolJudgerUsesLLM(t *testing.T) {
	registry := NewToolRegistry()
	// Regular mockTool doesn't implement ToolJudger
	tool := &mockTool{
		name:          "auto_tool_no_judger",
		description:   "A tool with PolicyAuto but no ToolJudger",
		inputSchema:   json.RawMessage(`{"type":"object"}`),
		defaultPolicy: PolicyAuto,
	}
	registry.Register(tool)

	// Set up LLM Judge that requires confirmation
	mockProvider := &mockLLMProvider{
		response: &llm.ChatResponse{
			Message: llm.Message{Content: "VERDICT: CONFIRM\nREASON: Needs user review"},
		},
	}
	judge := NewToolJudge(mockProvider, "test-model")
	registry.SetJudge(judge)

	confirmCalled := false
	var receivedReasoning string
	registry.SetConfirmFunc(func(ctx context.Context, req ConfirmationRequest) (ConfirmationResponse, error) {
		confirmCalled = true
		receivedReasoning = req.JudgeReasoning
		return ConfirmAllowOnce, nil
	})

	ctx := context.Background()
	input := json.RawMessage(`{"data":"test"}`)

	result, err := registry.Execute(ctx, "auto_tool_no_judger", input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Error("expected IsError to be false")
	}
	if !confirmCalled {
		t.Error("expected confirmFunc to be called when LLM Judge returns CONFIRM")
	}
	if receivedReasoning != "Needs user review" {
		t.Errorf("expected reasoning %q, got %q", "Needs user review", receivedReasoning)
	}
}

func TestToolRegistry_RegisterWithSource(t *testing.T) {
	registry := NewToolRegistry()

	// Register a tool with source "mcp"
	tool1 := newMockTool("mcp_tool", "An MCP tool")
	registry.RegisterWithSource(tool1, "mcp")

	// Register another tool with source "external"
	tool2 := newMockTool("external_tool", "An external tool")
	registry.RegisterWithSource(tool2, "external")

	// List and verify sources
	descriptors := registry.List()
	if len(descriptors) != 2 {
		t.Fatalf("expected 2 descriptors, got %d", len(descriptors))
	}

	// Build a map for easier lookup
	descMap := make(map[string]ToolDescriptor)
	for _, desc := range descriptors {
		descMap[desc.Name] = desc
	}

	// Verify mcp_tool has source "mcp"
	if desc, ok := descMap["mcp_tool"]; !ok {
		t.Error("expected to find 'mcp_tool' in descriptors")
	} else if desc.Source != "mcp" {
		t.Errorf("expected 'mcp_tool' source 'mcp', got %q", desc.Source)
	}

	// Verify external_tool has source "external"
	if desc, ok := descMap["external_tool"]; !ok {
		t.Error("expected to find 'external_tool' in descriptors")
	} else if desc.Source != "external" {
		t.Errorf("expected 'external_tool' source 'external', got %q", desc.Source)
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
	registry.SetPolicyOverrides(map[string]ToolPolicy{
		"overridden_tool": PolicyAlwaysAllow,
	})

	confirmCalled := false
	registry.SetConfirmFunc(func(ctx context.Context, req ConfirmationRequest) (ConfirmationResponse, error) {
		confirmCalled = true
		return ConfirmAllowOnce, nil
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

func TestWithWorkspacePath_AndWorkspacePathFrom(t *testing.T) {
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
			ctx = WithWorkspacePath(ctx, tt.path)
			got := WorkspacePathFrom(ctx)
			if got != tt.expected {
				t.Errorf("WorkspacePathFrom() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestWorkspacePathFrom_EmptyContext(t *testing.T) {
	ctx := context.Background()
	got := WorkspacePathFrom(ctx)
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
	reg.SetDefaultPolicy(PolicyAlwaysDeny)
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

// TestPolicyUniformity_AllToolFamilies verifies that the PolicyAuto execution
// pipeline works identically for all three tool families (core, external, MCP).
// Each family implements both Tool and ToolJudger, returning false/"" to defer
// to the LLM Judge. Two scenarios are tested per family:
//   - Scenario A: No judge, no confirmFunc → tool executes directly (CLI fallback)
//   - Scenario B: No judge, with confirmFunc → confirmFunc is called with
//     "no judge available" reasoning
func TestPolicyUniformity_AllToolFamilies(t *testing.T) {
	families := []string{"core", "external", "mcp"}

	// Build one mock tool per family. Each implements Tool + ToolJudger and
	// defers to the LLM Judge (Judge returns false, "").
	makeFamilyTool := func(family string) *mockJudgerTool {
		return &mockJudgerTool{
			mockTool: mockTool{
				name:          family + "_uniform_tool",
				description:   "PolicyAuto tool from " + family + " family",
				inputSchema:   json.RawMessage(`{"type":"object"}`),
				defaultPolicy: PolicyAuto,
			},
			judgeAllow:     false,
			judgeReasoning: "", // defer to LLM Judge
		}
	}

	t.Run("ScenarioA_NoJudge_NoConfirmFunc_DirectExecute", func(t *testing.T) {
		// With no judge and no confirmFunc (CLI mode), executeAuto should
		// fall through to confirmAndExecute which sees nil confirmFunc and
		// executes the tool directly.
		results := make([]ToolResult, 0, len(families))
		for _, family := range families {
			tool := makeFamilyTool(family)

			registry := NewToolRegistry()
			registry.RegisterWithSource(tool, family)
			// No judge set, no confirmFunc set

			ctx := context.Background()
			input := json.RawMessage(`{"action":"test"}`)

			result, err := registry.Execute(ctx, tool.Name(), input)
			if err != nil {
				t.Fatalf("[%s] unexpected error: %v", family, err)
			}
			if result.IsError {
				t.Errorf("[%s] expected IsError=false", family)
			}
			if result.Content != `{"action":"test"}` {
				t.Errorf("[%s] expected content %q, got %q", family, `{"action":"test"}`, result.Content)
			}
			results = append(results, result)
		}

		// Key assertion: all three families produce identical results.
		for i := 1; i < len(results); i++ {
			if results[i].Content != results[0].Content || results[i].IsError != results[0].IsError {
				t.Errorf("result mismatch between %s and %s: %+v vs %+v",
					families[0], families[i], results[0], results[i])
			}
		}
	})

	t.Run("ScenarioB_NoJudge_WithConfirmFunc_CallsConfirm", func(t *testing.T) {
		// With no judge but a confirmFunc set, executeAuto should fall
		// through and call confirmFunc with "no judge available" reasoning.
		type callRecord struct {
			family         string
			toolName       string
			judgeReasoning string
		}
		results := make([]ToolResult, 0, len(families))
		var calls []callRecord

		for _, family := range families {
			tool := makeFamilyTool(family)

			registry := NewToolRegistry()
			registry.RegisterWithSource(tool, family)
			// No judge set

			fam := family // capture
			registry.SetConfirmFunc(func(ctx context.Context, req ConfirmationRequest) (ConfirmationResponse, error) {
				calls = append(calls, callRecord{
					family:         fam,
					toolName:       req.ToolName,
					judgeReasoning: req.JudgeReasoning,
				})
				return ConfirmAllowOnce, nil
			})

			ctx := context.Background()
			input := json.RawMessage(`{"action":"test"}`)

			result, err := registry.Execute(ctx, tool.Name(), input)
			if err != nil {
				t.Fatalf("[%s] unexpected error: %v", family, err)
			}
			if result.IsError {
				t.Errorf("[%s] expected IsError=false", family)
			}
			if result.Content != `{"action":"test"}` {
				t.Errorf("[%s] expected content %q, got %q", family, `{"action":"test"}`, result.Content)
			}
			results = append(results, result)
		}

		// Verify confirmFunc was called for each family with the same reasoning.
		if len(calls) != len(families) {
			t.Fatalf("expected %d confirmFunc calls, got %d", len(families), len(calls))
		}
		for i, c := range calls {
			if c.toolName != families[i]+"_uniform_tool" {
				t.Errorf("[%s] expected toolName %q, got %q", c.family, families[i]+"_uniform_tool", c.toolName)
			}
			if !strings.Contains(c.judgeReasoning, "no judge available") {
				t.Errorf("[%s] expected reasoning to contain 'no judge available', got %q", c.family, c.judgeReasoning)
			}
		}

		// Key assertion: all three families produce identical results.
		for i := 1; i < len(results); i++ {
			if results[i].Content != results[0].Content || results[i].IsError != results[0].IsError {
				t.Errorf("result mismatch between %s and %s: %+v vs %+v",
					families[0], families[i], results[0], results[i])
			}
		}
	})

	t.Run("ScenarioC_WithLLMJudge_AllowSkipsConfirm", func(t *testing.T) {
		// With an LLM Judge that returns VerdictAllow, all three families
		// should bypass confirmFunc and execute directly.
		results := make([]ToolResult, 0, len(families))

		for _, family := range families {
			tool := makeFamilyTool(family)

			registry := NewToolRegistry()
			registry.RegisterWithSource(tool, family)

			mockProvider := &mockLLMProvider{
				response: &llm.ChatResponse{
					Message: llm.Message{Content: "VERDICT: ALLOW\nREASON: Safe operation"},
				},
			}
			judge := NewToolJudge(mockProvider, "test-model")
			registry.SetJudge(judge)

			confirmCalled := false
			registry.SetConfirmFunc(func(ctx context.Context, req ConfirmationRequest) (ConfirmationResponse, error) {
				confirmCalled = true
				return ConfirmAllowOnce, nil
			})

			ctx := context.Background()
			input := json.RawMessage(`{"action":"test"}`)

			result, err := registry.Execute(ctx, tool.Name(), input)
			if err != nil {
				t.Fatalf("[%s] unexpected error: %v", family, err)
			}
			if result.IsError {
				t.Errorf("[%s] expected IsError=false", family)
			}
			if result.Content != `{"action":"test"}` {
				t.Errorf("[%s] expected content %q, got %q", family, `{"action":"test"}`, result.Content)
			}
			if confirmCalled {
				t.Errorf("[%s] expected confirmFunc NOT to be called when LLM Judge allows", family)
			}
			if mockProvider.callCount != 1 {
				t.Errorf("[%s] expected LLM Judge to be called once, got %d", family, mockProvider.callCount)
			}
			results = append(results, result)
		}

		// Key assertion: all three families produce identical results.
		for i := 1; i < len(results); i++ {
			if results[i].Content != results[0].Content || results[i].IsError != results[0].IsError {
				t.Errorf("result mismatch between %s and %s: %+v vs %+v",
					families[0], families[i], results[0], results[i])
			}
		}
	})
}

func TestSetDefaultPolicy_OverriddenByPerTool(t *testing.T) {
	reg := NewToolRegistry()
	tool := newMockReadOnlyTool("test_tool", "desc")
	reg.Register(tool)

	// Set global default to deny
	reg.SetDefaultPolicy(PolicyAlwaysDeny)

	// But per-tool override to allow
	reg.SetPolicyOverrides(map[string]ToolPolicy{
		"test_tool": PolicyAlwaysAllow,
	})

	result, err := reg.Execute(context.Background(), "test_tool", []byte(`{"input":"hello"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Error("per-tool override should take precedence over global default")
	}
}
