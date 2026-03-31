package tools

import (
	"context"
	"encoding/json"
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

func (m *mockJudgerTool) Judge(ctx context.Context, input json.RawMessage) (bool, string) {
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
	if result.Content != "Tool execution denied by user" {
		t.Errorf("expected content 'Tool execution denied by user', got %q", result.Content)
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
	if err != context.Canceled {
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
	if err != expectedErr {
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
