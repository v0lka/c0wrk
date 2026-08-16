package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/v0lka/sp4rk/llm"
	sdktools "github.com/v0lka/sp4rk/tools"
	"github.com/v0lka/sp4rk/tools/builtins"
)

// ── Test doubles ──────────────────────────────────────────────────────────

// mockTool is a simple echo tool for testing. Its group mirrors the
// capability taxonomy: newMockTool defaults to local_write (the
// user_confirm-postured mutating group) and newMockReadOnlyTool to
// local_read (the allow-postured read group). defaultPolicy stays for the
// sdktools.Tool interface; the registry resolves policy from the GROUP.
type mockTool struct {
	name          string
	description   string
	inputSchema   json.RawMessage
	defaultPolicy sdktools.ToolPolicy
	group         sdktools.ToolGroup
}

func newMockTool(name, description string) *mockTool {
	return &mockTool{
		name:          name,
		description:   description,
		inputSchema:   json.RawMessage(`{"type":"object","properties":{"input":{"type":"string"}}}`),
		defaultPolicy: sdktools.PolicyUserConfirm,
		group:         sdktools.GroupLocalWrite,
	}
}

func newMockReadOnlyTool(name, description string) *mockTool {
	return &mockTool{
		name:          name,
		description:   description,
		inputSchema:   json.RawMessage(`{"type":"object","properties":{"input":{"type":"string"}}}`),
		defaultPolicy: sdktools.PolicyAlwaysAllow,
		group:         sdktools.GroupLocalRead,
	}
}

func newMockSystemTool(name, description string) *mockTool {
	return &mockTool{
		name:          name,
		description:   description,
		inputSchema:   json.RawMessage(`{"type":"object","properties":{"input":{"type":"string"}}}`),
		defaultPolicy: sdktools.PolicyAlwaysAllow,
		group:         sdktools.GroupSystem,
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

func (m *mockTool) Group() sdktools.ToolGroup { return m.group }

func (m *mockTool) Execute(ctx context.Context, input json.RawMessage) (sdktools.ToolResult, error) {
	// Simple echo: return the input as content
	return sdktools.ToolResult{
		Content: string(input),
		IsError: false,
	}, nil
}

// setDefaultGroupPolicies installs the production default group policies
// (reads allow; everything mutating/remote confirms) mirroring
// backend/config defaultToolGroupPolicies, so tests exercise the same
// resolution the builder performs from security.groups.
func setDefaultGroupPolicies(registry *ToolRegistry) {
	registry.SetGroupPolicies(map[sdktools.ToolGroup]sdktools.ToolPolicy{
		sdktools.GroupLocalRead:   sdktools.PolicyAlwaysAllow,
		sdktools.GroupRemoteRead:  sdktools.PolicyAlwaysAllow,
		sdktools.GroupExecute:     sdktools.PolicyUserConfirm,
		sdktools.GroupLocalWrite:  sdktools.PolicyUserConfirm,
		sdktools.GroupLocalMCP:    sdktools.PolicyUserConfirm,
		sdktools.GroupRemoteMCP:   sdktools.PolicyUserConfirm,
		sdktools.GroupRemoteWrite: sdktools.PolicyUserConfirm,
	})
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

// ── Registry basics ───────────────────────────────────────────────────────

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
	// newMockTool resolves to user_confirm; since nil ConfirmFunc is
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

// ── Confirmation flows ────────────────────────────────────────────────────

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
	setDefaultGroupPolicies(registry) // local_read → allow
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
		t.Error("expected confirmFunc NOT to be called for an allow-group tool")
	}
}

func TestConfirmFunc_NilFunc(t *testing.T) {
	registry := NewToolRegistry()
	setDefaultGroupPolicies(registry)
	// Use a tool in an allow group so it executes without confirmation
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

// ── Group policy resolution ───────────────────────────────────────────────

// TestPolicyAlwaysAllow_ExecutesImmediately tests that an allow group
// executes without confirmation.
func TestPolicyAlwaysAllow_ExecutesImmediately(t *testing.T) {
	registry := NewToolRegistry()
	setDefaultGroupPolicies(registry)
	tool := newMockReadOnlyTool("always_allow", "A tool in the local_read group")
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
		t.Error("expected confirmFunc NOT to be called for an allow group")
	}
}

// TestPolicyAlwaysDeny_BlocksExecution tests that a deny group blocks execution.
func TestPolicyAlwaysDeny_BlocksExecution(t *testing.T) {
	registry := NewToolRegistry()
	tool := newMockTool("always_deny", "A tool in a deny-postured group")
	registry.Register(tool)

	// The tool's own DefaultPolicy is user_confirm, but the GROUP policy is
	// what counts: deny wins regardless of the tool's own default.
	registry.SetGroupPolicies(map[sdktools.ToolGroup]sdktools.ToolPolicy{
		sdktools.GroupLocalWrite: sdktools.PolicyAlwaysDeny,
	})

	ctx := context.Background()
	input := json.RawMessage(`{"data":"test"}`)

	result, err := registry.Execute(ctx, "always_deny", input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected IsError to be true for a deny group")
	}
	if !strings.Contains(result.Content, "blocked by security policy") {
		t.Errorf("expected security policy error, got: %s", result.Content)
	}
	if !strings.Contains(result.Content, "local_write") {
		t.Errorf("expected the deny message to name the group, got: %s", result.Content)
	}
}

// TestPolicyUserConfirm_AlwaysCallsConfirmFunc tests that a user_confirm
// group always calls confirmFunc.
func TestPolicyUserConfirm_AlwaysCallsConfirmFunc(t *testing.T) {
	registry := NewToolRegistry()
	tool := newMockTool("user_confirm", "A tool in a user_confirm group")
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
		t.Error("expected confirmFunc to be called for a user_confirm group")
	}
}

// TestPolicyUserConfirm_SurfacesDefaultReason verifies that when a tool
// resolves to user_confirm and no richer reason (symlink traversal, judge
// flag, or auto-approve denial) is available, the confirmation request still
// carries a human-readable reason explaining why approval is needed.
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
		t.Fatal("expected a non-empty human-readable reason for a user_confirm tool, got empty string")
	}
	if gotReason != "This tool creates or overwrites a file." {
		t.Errorf("expected write_file-specific reason, got %q", gotReason)
	}
}

// TestPolicyUserConfirm_DefaultReason_GenericFallback verifies that a tool
// name without a specific mapping falls back to the generic mutating-action
// reason.
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

// TestGroupPolicyFromConfig tests that group policies supplied by the builder
// (security.groups in config.yaml) drive execution: an execute-group tool set
// to allow executes without confirmation.
func TestGroupPolicyFromConfig(t *testing.T) {
	registry := NewToolRegistry()
	// local_write group → user_confirm posture by default
	tool := newMockTool("overridden_tool", "A tool whose group is set to allow")
	registry.Register(tool)

	// Widen the group to allow
	registry.SetGroupPolicies(map[sdktools.ToolGroup]sdktools.ToolPolicy{
		sdktools.GroupLocalWrite: sdktools.PolicyAlwaysAllow,
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
		t.Error("expected confirmFunc NOT to be called when the group policy is allow")
	}
}

// TestGroupPolicies_FailSafeDefault verifies that a group WITHOUT a
// configured entry fails safe to user_confirm — an unconfigured read tool
// must not silently execute.
func TestGroupPolicies_FailSafeDefault(t *testing.T) {
	registry := NewToolRegistry()
	// No SetGroupPolicies call at all: unconfigured groups confirm.
	registry.Register(newMockReadOnlyTool("test_tool", "desc"))

	confirmCalled := false
	registry.SetConfirmFunc(func(context.Context, sdktools.ConfirmationRequest) (sdktools.ConfirmationResponse, error) {
		confirmCalled = true
		return sdktools.ConfirmAllowOnce, nil
	})

	result, err := registry.Execute(context.Background(), "test_tool", []byte(`{"input":"hello"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Error("expected no error (confirm auto-allowed)")
	}
	if !confirmCalled {
		t.Error("expected confirmFunc to be called for a tool whose group has no configured policy")
	}
}

// TestGroupPolicies_ReplacementNotMerge verifies SetGroupPolicies REPLACES
// the map: tightening one group must not leave a stale allow from a previous
// configuration round (mirrors UpdateSecurityPolicies re-application).
func TestGroupPolicies_ReplacementNotMerge(t *testing.T) {
	registry := NewToolRegistry()
	registry.SetGroupPolicies(map[sdktools.ToolGroup]sdktools.ToolPolicy{
		sdktools.GroupLocalRead: sdktools.PolicyAlwaysAllow,
	})
	registry.Register(newMockReadOnlyTool("reader", "reads"))

	// Second application narrows the map to a deny-only entry.
	registry.SetGroupPolicies(map[sdktools.ToolGroup]sdktools.ToolPolicy{
		sdktools.GroupLocalRead: sdktools.PolicyAlwaysDeny,
	})

	result, err := registry.Execute(context.Background(), "reader", []byte(`{"input":"hello"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected IsError=true when the group is re-applied as deny")
	}
}

// TestApplySecurityState_ReplacesAllThreeComponentsAtomically verifies the
// runtime security push API: one call must replace the group-policy map, the
// auto-approve flag, and the Smart Approve flag together (a torn update would
// let a call run with new policies but old flags), deep-copy the caller's map
// (a broadcast push passes one map to many registries; each must stay
// independently mutable), and leave the caller's map unaliased.
func TestApplySecurityState_ReplacesAllThreeComponentsAtomically(t *testing.T) {
	registry := NewToolRegistry()
	registry.SetGroupPolicies(map[sdktools.ToolGroup]sdktools.ToolPolicy{
		sdktools.GroupExecute: sdktools.PolicyAlwaysAllow,
	})
	registry.SetAutoApproveWorkspaceWrites(true)
	registry.SetSmartApprove(true)

	source := map[sdktools.ToolGroup]sdktools.ToolPolicy{
		sdktools.GroupExecute: sdktools.PolicyAlwaysDeny,
	}
	registry.ApplySecurityState(source, false, false)

	if got := registry.GroupPolicies()[sdktools.GroupExecute]; got != sdktools.PolicyAlwaysDeny {
		t.Fatalf("execute policy = %v, want always_deny", got)
	}
	registry.mu.RLock()
	autoApprove, smartApprove := registry.autoApproveWorkspaceWrites, registry.smartApprove
	registry.mu.RUnlock()
	if autoApprove {
		t.Error("autoApproveWorkspaceWrites must be replaced with false")
	}
	if smartApprove {
		t.Error("smartApprove must be replaced with false")
	}

	// The caller's map must not alias registry state.
	source[sdktools.GroupLocalWrite] = sdktools.PolicyAlwaysAllow
	if _, ok := registry.GroupPolicies()[sdktools.GroupLocalWrite]; ok {
		t.Error("registry aliased the caller's map — ApplySecurityState must deep-copy")
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

// ── Tool-local judge mocks ────────────────────────────────────────────────

// mockJudgerTool is a tool in an allow group (local_read) that implements
// ToolJudger, defaulting to a SOFT outcome (path-containment posture).
type mockJudgerTool struct {
	mockTool
	outcome sdktools.JudgeOutcome
}

func newMockJudgerTool(name string, allow bool, reasoning string) *mockJudgerTool {
	return &mockJudgerTool{
		mockTool: mockTool{
			name:          name,
			description:   "A tool with ToolJudger",
			inputSchema:   json.RawMessage(`{"type":"object","properties":{"input":{"type":"string"}}}`),
			defaultPolicy: sdktools.PolicyAlwaysAllow,
			group:         sdktools.GroupLocalRead,
		},
		outcome: sdktools.JudgeOutcome{Allow: allow, Reason: reasoning, Severity: sdktools.JudgeSeveritySoft},
	}
}

// newMockHardJudgerTool builds a judger tool whose outcome is a HARD
// security-control trigger (command blacklist / SSRF posture).
func newMockHardJudgerTool(name, reasoning string) *mockJudgerTool {
	tool := newMockJudgerTool(name, false, reasoning)
	tool.outcome.Severity = sdktools.JudgeSeverityHard
	return tool
}

// newMockExecuteJudgerTool builds a judger tool in the execute group with an
// explicit outcome (bash_exec posture).
func newMockExecuteJudgerTool(name string, outcome sdktools.JudgeOutcome) *mockJudgerTool {
	tool := newMockJudgerTool(name, outcome.Allow, outcome.Reason)
	tool.group = sdktools.GroupExecute
	tool.outcome = outcome
	return tool
}

func (m *mockJudgerTool) Judge(ctx context.Context, input json.RawMessage) sdktools.JudgeOutcome {
	return m.outcome
}

// mockConfirmJudgerTool is a local_write tool that implements ToolJudger,
// mirroring the write builtins: an allowed (in-roots) outcome carries a
// reason; a denied outcome carries the containment reason. Soft severity.
type mockConfirmJudgerTool struct {
	mockTool
	judgeResult    bool
	judgeReasoning string
}

func newMockConfirmJudgerTool(name string, policy sdktools.ToolPolicy, allow bool, reasoning string) *mockConfirmJudgerTool {
	_ = policy // the policy parameter is retained for call-site clarity; group drives resolution
	return &mockConfirmJudgerTool{
		mockTool: mockTool{
			name:          name,
			description:   "A confirm tool with ToolJudger",
			inputSchema:   json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}}}`),
			defaultPolicy: sdktools.PolicyUserConfirm,
			group:         sdktools.GroupLocalWrite,
		},
		judgeResult:    allow,
		judgeReasoning: reasoning,
	}
}

func (m *mockConfirmJudgerTool) Judge(ctx context.Context, input json.RawMessage) sdktools.JudgeOutcome {
	return sdktools.JudgeOutcome{
		Allow:    m.judgeResult,
		Reason:   m.judgeReasoning,
		Severity: sdktools.JudgeSeveritySoft,
	}
}

// ── Allow-group judge interactions ────────────────────────────────────────

// TestPolicyAlwaysAllow_WithToolJudgerFlags tests that an allow-group tool
// with a SOFT judge escalation goes to confirmation when Smart Approve is off
// (the default), and that the judge reason is surfaced.
func TestPolicyAlwaysAllow_WithToolJudgerFlags(t *testing.T) {
	registry := NewToolRegistry()
	setDefaultGroupPolicies(registry)
	tool := newMockJudgerTool("judger_tool", false, "path outside session roots: /etc/passwd")
	registry.Register(tool)

	confirmCalled := false
	var receivedReasoning string
	var receivedDisableJudge bool
	registry.SetConfirmFunc(func(ctx context.Context, req sdktools.ConfirmationRequest) (sdktools.ConfirmationResponse, error) {
		confirmCalled = true
		receivedReasoning = req.JudgeReasoning
		receivedDisableJudge = req.DisableJudge
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
	if receivedReasoning != "path outside session roots: /etc/passwd" {
		t.Errorf("expected the judge reason to be surfaced, got %q", receivedReasoning)
	}
	if receivedDisableJudge {
		t.Error("a soft escalation with Smart Approve off must keep the advisory Ask Agent action available")
	}
}

// TestPolicyAlwaysAllow_HardReasonForcesConfirmationWithDisabledJudge is the
// hard-severity counterpart: a HARD reason (command blacklist, SSRF) under an
// allow group forces confirmation with the advisory judge disabled — a fired
// security control must never be weakened by the advisory Ask Agent action.
func TestPolicyAlwaysAllow_HardReasonForcesConfirmationWithDisabledJudge(t *testing.T) {
	registry := NewToolRegistry()
	setDefaultGroupPolicies(registry)
	const pattern = `rm\s+-rf\s+/`
	registry.Register(newMockHardJudgerTool("judger_tool", "command matches blacklist pattern: "+pattern))

	var req sdktools.ConfirmationRequest
	registry.SetConfirmFunc(func(_ context.Context, r sdktools.ConfirmationRequest) (sdktools.ConfirmationResponse, error) {
		req = r
		return sdktools.ConfirmDeny, nil
	})

	result, err := registry.Execute(context.Background(), "judger_tool", json.RawMessage(`{"command":"rm -rf /"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected IsError=true after denying the forced confirmation")
	}
	if !strings.Contains(req.JudgeReasoning, pattern) {
		t.Errorf("expected the confirmation reason to contain the matched blacklist pattern %q, got %q", pattern, req.JudgeReasoning)
	}
	if !req.DisableJudge {
		t.Error("a hard reason must disable the advisory Ask Agent action (DisableJudge=true)")
	}
}

// TestPolicyAlwaysAllow_HardReasonNeverPassesSmartApprove proves hard
// reasons never reach Smart Approve: even with Smart Approve on and the
// strict judge scripted to ALLOW, a hard escalation still confirms (and the
// strict judge is not consulted at all).
func TestPolicyAlwaysAllow_HardReasonNeverPassesSmartApprove(t *testing.T) {
	registry := NewToolRegistry()
	setDefaultGroupPolicies(registry)
	registry.SetSmartApprove(true)
	registry.Register(newMockHardJudgerTool("judger_tool", "command matches blacklist pattern: shutdown"))

	judge, provider := newStrictJudge("VERDICT: ALLOW\nREASON: looks fine to me", nil)
	registry.SetJudge(judge)

	var req sdktools.ConfirmationRequest
	confirmCalled := false
	registry.SetConfirmFunc(func(_ context.Context, r sdktools.ConfirmationRequest) (sdktools.ConfirmationResponse, error) {
		confirmCalled = true
		req = r
		return sdktools.ConfirmDeny, nil
	})

	result, err := registry.Execute(context.Background(), "judger_tool", json.RawMessage(`{"command":"shutdown now"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !confirmCalled {
		t.Fatal("expected confirmation: hard reasons must not be auto-approved by Smart Approve")
	}
	if result.IsError != true {
		t.Error("expected IsError=true after denying the forced confirmation")
	}
	if got := provider.callCount(); got != 0 {
		t.Errorf("strict judge calls = %d, want 0 (hard reasons short-circuit Smart Approve)", got)
	}
	if !req.DisableJudge {
		t.Error("hard-reason confirmation must disable the advisory judge")
	}
}

// TestPolicyAlwaysAllow_SoftReasonSmartApproveAllow verifies a soft
// escalation under an allow group CAN be auto-approved when Smart Approve's
// strict judge allows — soft evidence is advisory and may be weighed.
func TestPolicyAlwaysAllow_SoftReasonSmartApproveAllow(t *testing.T) {
	registry := NewToolRegistry()
	setDefaultGroupPolicies(registry)
	registry.SetSmartApprove(true)
	registry.Register(newMockJudgerTool("judger_tool", false, "path outside session roots: /etc/hosts"))

	judge, provider := newStrictJudge("VERDICT: ALLOW\nREASON: benign read of a public file", nil)
	registry.SetJudge(judge)

	confirmCalled := false
	registry.SetConfirmFunc(func(context.Context, sdktools.ConfirmationRequest) (sdktools.ConfirmationResponse, error) {
		confirmCalled = true
		return sdktools.ConfirmAllowOnce, nil
	})

	result, err := registry.Execute(context.Background(), "judger_tool", json.RawMessage(`{"path":"/etc/hosts"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Error("expected the strict-judge ALLOW to execute the call")
	}
	if confirmCalled {
		t.Error("expected no confirmation when Smart Approve allows a soft escalation")
	}
	if got := provider.callCount(); got != 1 {
		t.Errorf("strict judge calls = %d, want 1", got)
	}
}

// TestPolicyAlwaysAllow_WithToolJudgerAllows tests that an allow-group tool
// whose judge reports no concern executes directly.
func TestPolicyAlwaysAllow_WithToolJudgerAllows(t *testing.T) {
	registry := NewToolRegistry()
	setDefaultGroupPolicies(registry)
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

// TestPolicyAlwaysAllow_WithoutToolJudger tests that an allow-group tool
// without a judge executes directly.
func TestPolicyAlwaysAllow_WithoutToolJudger(t *testing.T) {
	registry := NewToolRegistry()
	setDefaultGroupPolicies(registry)
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

// TestPolicyAlwaysAllow_WithToolJudgerEmptyReasoning tests that an allow
// group executes directly when the tool's judge returns no concern
// (allow=false with empty reasoning means "no tool-specific concern").
func TestPolicyAlwaysAllow_WithToolJudgerEmptyReasoning(t *testing.T) {
	registry := NewToolRegistry()
	setDefaultGroupPolicies(registry)
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

// ── user_confirm auto-approval (local_write) ──────────────────────────────

// TestAutoApproval_WorkspacePath verifies that a plain user_confirm tool
// WITHOUT a judge is never auto-approved, even when all paths in the input
// are within the workspace. Workspace-locality alone must not silently
// downgrade an explicit confirm posture.
func TestAutoApproval_WorkspacePath(t *testing.T) {
	registry := NewToolRegistry()
	setDefaultGroupPolicies(registry)
	tool := newMockTool("mutating", "A mutating tool without a judge")
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
		t.Error("expected confirmFunc to be called: workspace-locality must not bypass user_confirm without a judge")
	}
}

// TestAutoApproval_UserConfirmWithJudger_WorkspaceEnabled tests that when
// autoApproveWorkspaceWrites is enabled and a local_write tool's Judge
// reports an in-roots target, the tool executes without confirmation.
func TestAutoApproval_UserConfirmWithJudger_WorkspaceEnabled(t *testing.T) {
	registry := NewToolRegistry()
	setDefaultGroupPolicies(registry)
	registry.SetAutoApproveWorkspaceWrites(true)

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
// autoApproveWorkspaceWrites is disabled, a local_write tool always requires
// confirmation regardless of Judge result.
func TestAutoApproval_UserConfirmWithJudger_WorkspaceDisabled(t *testing.T) {
	registry := NewToolRegistry()
	setDefaultGroupPolicies(registry)
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

// TestAutoApproval_UserConfirmWithJudger_OutsideWorkspace tests that even
// with auto-approve enabled, a local_write tool still requires confirmation
// when the Judge reports an out-of-roots target.
func TestAutoApproval_UserConfirmWithJudger_OutsideWorkspace(t *testing.T) {
	registry := NewToolRegistry()
	registry.SetAutoApproveWorkspaceWrites(true)

	// Judge denies with no reason: no soft reason is recorded, so the plain
	// default reason confirmation fires.
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
// non-empty reason, that reason is surfaced via
// ConfirmationRequest.JudgeReasoning rather than being discarded.
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

// TestAutoApproval_TempDir verifies a plain user_confirm tool is not
// auto-approved even when all paths are within the session temp directory.
func TestAutoApproval_TempDir(t *testing.T) {
	registry := NewToolRegistry()
	tool := newMockTool("mutating", "A mutating tool")
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
		t.Error("expected confirmFunc to be called: temp-dir locality must not bypass user_confirm")
	}
}

// TestAutoApproval_NonLocalWriteGroupNotAutoApproved verifies the
// group-scoping of workspace auto-approval: an execute-group tool whose Judge
// allows (in-roots command) is NOT auto-approved — auto-approval is a
// local_write privilege; execute goes through Smart Approve / confirmation.
func TestAutoApproval_NonLocalWriteGroupNotAutoApproved(t *testing.T) {
	registry := NewToolRegistry()
	setDefaultGroupPolicies(registry)
	registry.SetAutoApproveWorkspaceWrites(true)

	registry.Register(newMockExecuteJudgerTool("bash_exec", sdktools.JudgeOutcome{
		Allow:  true,
		Reason: "command stays within session roots",
	}))

	confirmCalled := false
	registry.SetConfirmFunc(func(context.Context, sdktools.ConfirmationRequest) (sdktools.ConfirmationResponse, error) {
		confirmCalled = true
		return sdktools.ConfirmAllowOnce, nil
	})

	ctx := sdktools.WithWorkspacePath(context.Background(), "/workspace")
	_, err := registry.Execute(ctx, "bash_exec", json.RawMessage(`{"command":"ls /workspace"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !confirmCalled {
		t.Error("expected confirmation: execute-group tools must not use local_write auto-approval")
	}
}

// TestAutoApproval_AllowGroupCleanExecutes verifies that an allow-group tool
// with no judge concerns executes without confirmation when paths are inside
// the workspace.
func TestAutoApproval_AllowGroupCleanExecutes(t *testing.T) {
	registry := NewToolRegistry()
	setDefaultGroupPolicies(registry)
	registry.Register(newMockReadOnlyTool("readonly", "A read-only tool"))

	confirmCalled := false
	registry.SetConfirmFunc(func(ctx context.Context, req sdktools.ConfirmationRequest) (sdktools.ConfirmationResponse, error) {
		confirmCalled = true
		return sdktools.ConfirmAllowOnce, nil
	})

	ctx := sdktools.WithWorkspacePath(context.Background(), "/workspace")
	_, err := registry.Execute(ctx, "readonly", json.RawMessage(`{"path": "/workspace/file.txt"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if confirmCalled {
		t.Error("expected confirmFunc NOT to be called for a clean allow-group call")
	}
}

// TestAutoApproval_AllowedRoot verifies that an allow-group tool with a path
// inside an additional allowed root (auxiliary work directory) executes
// without confirmation — the same treatment as workspace/temp paths.
func TestAutoApproval_AllowedRoot(t *testing.T) {
	registry := NewToolRegistry()
	setDefaultGroupPolicies(registry)
	registry.Register(newMockReadOnlyTool("readonly", "A read-only tool"))

	confirmCalled := false
	registry.SetConfirmFunc(func(ctx context.Context, req sdktools.ConfirmationRequest) (sdktools.ConfirmationResponse, error) {
		confirmCalled = true
		return sdktools.ConfirmAllowOnce, nil
	})

	ctx := sdktools.WithAllowedRoots(context.Background(), []string{"/aux"})
	_, err := registry.Execute(ctx, "readonly", json.RawMessage(`{"path": "/aux/file.txt"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if confirmCalled {
		t.Error("expected confirmFunc NOT to be called for allow-group tools with paths inside an allowed root")
	}
}

// TestAutoApproval_AllowGroup_JudgerHardFlagsBeforeAutoApprove is a
// regression test for a security hole: when an allow-group tool implements
// ToolJudger and the judge reports a HARD flag (e.g. a blacklisted command),
// the call MUST escalate to user confirmation even if all paths in the input
// are inside the session workspace — and with the advisory judge disabled.
func TestAutoApproval_AllowGroup_JudgerHardFlagsBeforeAutoApprove(t *testing.T) {
	registry := NewToolRegistry()
	setDefaultGroupPolicies(registry)
	registry.Register(newMockHardJudgerTool("bash_exec", "command matches blacklist pattern: rm -rf"))

	var req sdktools.ConfirmationRequest
	registry.SetConfirmFunc(func(_ context.Context, r sdktools.ConfirmationRequest) (sdktools.ConfirmationResponse, error) {
		req = r
		return sdktools.ConfirmAllowOnce, nil
	})

	ctx := sdktools.WithWorkspacePath(context.Background(), "/workspace")
	// Input has paths inside the workspace — locality must not matter.
	input := json.RawMessage(`{"command": "rm -rf /workspace/.git"}`)

	_, err := registry.Execute(ctx, "bash_exec", input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if req.ToolName == "" {
		t.Fatal("expected confirmFunc to be called: hard-flagged calls must escalate even when paths are inside workspace")
	}
	if !req.DisableJudge {
		t.Error("hard-flagged confirmation must disable the advisory judge")
	}
}

// TestAutoApproval_AllowGroup_JudgerAllowsWithWorkspacePath verifies that an
// allow-group tool whose judge reports no concern executes when paths are
// inside the workspace.
func TestAutoApproval_AllowGroup_JudgerAllowsWithWorkspacePath(t *testing.T) {
	registry := NewToolRegistry()
	setDefaultGroupPolicies(registry)
	registry.Register(newMockJudgerTool("read_file", true, ""))

	confirmCalled := false
	registry.SetConfirmFunc(func(context.Context, sdktools.ConfirmationRequest) (sdktools.ConfirmationResponse, error) {
		confirmCalled = true
		return sdktools.ConfirmAllowOnce, nil
	})

	ctx := sdktools.WithWorkspacePath(context.Background(), "/workspace")
	_, err := registry.Execute(ctx, "read_file", json.RawMessage(`{"path": "/workspace/file.txt"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if confirmCalled {
		t.Error("expected confirmFunc NOT to be called when the judge reports no concern")
	}
}

// TestAutoApproval_AllowGroup_JudgerEmptyReasoningWithWorkspacePath verifies
// that an allow-group tool whose judge returns no concern (empty reason)
// still executes when paths are inside the workspace: empty reasoning means
// "no concern to report".
func TestAutoApproval_AllowGroup_JudgerEmptyReasoningWithWorkspacePath(t *testing.T) {
	registry := NewToolRegistry()
	setDefaultGroupPolicies(registry)
	registry.Register(newMockJudgerTool("bash_exec", false, ""))

	confirmCalled := false
	registry.SetConfirmFunc(func(context.Context, sdktools.ConfirmationRequest) (sdktools.ConfirmationResponse, error) {
		confirmCalled = true
		return sdktools.ConfirmAllowOnce, nil
	})

	ctx := sdktools.WithWorkspacePath(context.Background(), "/workspace")
	_, err := registry.Execute(ctx, "bash_exec", json.RawMessage(`{"command": "ls /workspace"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if confirmCalled {
		t.Error("expected confirmFunc NOT to be called when the judge reports no concern")
	}
}

// TestAutoApproval_OutsideWorkspace tests that a user_confirm tool still
// requires confirmation when paths are outside the workspace.
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

// ── local_write containment with real write_file (symlinks, "..") ─────────

// TestLocalWriteAutoApproval_SymlinkInsideRootsAutoApproved proves that a
// write through a symlink whose resolution lands INSIDE the session roots is
// auto-approved (no confirmation): containment reasons about resolved paths.
// Uses the real WriteFileTool so the real EvalSymlinks-based judge runs; the
// write never happens (confirmation is never requested) except through the
// symlinked in-root target.
func TestLocalWriteAutoApproval_SymlinkInsideRootsAutoApproved(t *testing.T) {
	ws := t.TempDir()
	realDir := filepath.Join(ws, "real")
	if err := os.MkdirAll(realDir, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(ws, "link")
	if err := os.Symlink(realDir, link); err != nil {
		t.Fatal(err)
	}

	registry := NewToolRegistry()
	registry.SetAutoApproveWorkspaceWrites(true)
	registry.Register(builtins.NewWriteFileTool())

	confirmCalled := false
	registry.SetConfirmFunc(func(context.Context, sdktools.ConfirmationRequest) (sdktools.ConfirmationResponse, error) {
		confirmCalled = true
		return sdktools.ConfirmDeny, nil
	})

	ctx := sdktools.WithWorkspacePath(context.Background(), ws)
	input, _ := json.Marshal(map[string]string{"path": filepath.Join(link, "file.txt"), "content": "x"})

	_, err := registry.Execute(ctx, "write_file", input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if confirmCalled {
		t.Error("expected no confirmation: a symlink resolving inside the roots auto-approves")
	}
}

// TestLocalWriteAutoApproval_SymlinkEscapeForcesHardConfirm proves the
// counterpart: a write through a symlink that ESCAPES the session roots is a
// hard confirmation even with auto-approval and Smart Approve both enabled
// (the strict judge is never consulted, and the advisory judge is disabled).
func TestLocalWriteAutoApproval_SymlinkEscapeForcesHardConfirm(t *testing.T) {
	ws := t.TempDir()
	outside := t.TempDir() // a different root: outside the workspace
	link := filepath.Join(ws, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}

	registry := NewToolRegistry()
	registry.SetAutoApproveWorkspaceWrites(true)
	registry.SetSmartApprove(true)
	registry.Register(builtins.NewWriteFileTool())
	judge, provider := newStrictJudge("VERDICT: ALLOW\nREASON: looks fine", nil)
	registry.SetJudge(judge)

	var req sdktools.ConfirmationRequest
	confirmCalled := false
	registry.SetConfirmFunc(func(_ context.Context, r sdktools.ConfirmationRequest) (sdktools.ConfirmationResponse, error) {
		confirmCalled = true
		req = r
		return sdktools.ConfirmDeny, nil
	})

	ctx := sdktools.WithWorkspacePath(context.Background(), ws)
	input, _ := json.Marshal(map[string]string{"path": filepath.Join(link, "file.txt"), "content": "x"})

	_, err := registry.Execute(ctx, "write_file", input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !confirmCalled {
		t.Fatal("expected a hard confirmation for a symlink escaping the session roots")
	}
	if !strings.Contains(strings.ToLower(req.JudgeReasoning), "symlink") {
		t.Errorf("expected the confirmation reason to explain the symlink escape, got %q", req.JudgeReasoning)
	}
	if !req.DisableJudge {
		t.Error("symlink-escape confirmation must disable the advisory judge")
	}
	if got := provider.callCount(); got != 0 {
		t.Errorf("strict judge calls = %d, want 0 (hard reasons short-circuit Smart Approve)", got)
	}
}

// TestLocalWriteAutoApproval_DotDotNormalizationAutoApproved verifies ".."
// normalization on the happy side: a path with an inner ".." that still
// resolves inside the workspace auto-approves.
func TestLocalWriteAutoApproval_DotDotNormalizationAutoApproved(t *testing.T) {
	ws := t.TempDir()
	if err := os.MkdirAll(filepath.Join(ws, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}

	registry := NewToolRegistry()
	registry.SetAutoApproveWorkspaceWrites(true)
	registry.Register(builtins.NewWriteFileTool())

	confirmCalled := false
	registry.SetConfirmFunc(func(context.Context, sdktools.ConfirmationRequest) (sdktools.ConfirmationResponse, error) {
		confirmCalled = true
		return sdktools.ConfirmDeny, nil
	})

	ctx := sdktools.WithWorkspacePath(context.Background(), ws)
	input, _ := json.Marshal(map[string]string{"path": filepath.Join(ws, "sub", "..", "ok.txt"), "content": "x"})

	_, err := registry.Execute(ctx, "write_file", input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if confirmCalled {
		t.Error("expected no confirmation: an in-root path with inner '..' normalizes inside the roots")
	}
}

// TestLocalWriteAutoApproval_DotDotEscapeBlocked verifies the escape side of
// ".." normalization: enough ".." segments to climb OUT of the workspace must
// fail containment and force confirmation.
func TestLocalWriteAutoApproval_DotDotEscapeBlocked(t *testing.T) {
	ws := t.TempDir()
	if err := os.MkdirAll(filepath.Join(ws, "sub", "deep"), 0o755); err != nil {
		t.Fatal(err)
	}

	registry := NewToolRegistry()
	registry.SetAutoApproveWorkspaceWrites(true)
	registry.Register(builtins.NewWriteFileTool())

	confirmCalled := false
	registry.SetConfirmFunc(func(context.Context, sdktools.ConfirmationRequest) (sdktools.ConfirmationResponse, error) {
		confirmCalled = true
		return sdktools.ConfirmDeny, nil
	})

	ctx := sdktools.WithWorkspacePath(context.Background(), ws)
	input, _ := json.Marshal(map[string]string{
		"path":    filepath.Join(ws, "sub", "deep", "..", "..", "..", "outside.txt"),
		"content": "x",
	})

	_, err := registry.Execute(ctx, "write_file", input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !confirmCalled {
		t.Error("expected confirmation: a '..'-normalized escape out of the workspace must not auto-approve")
	}
}

// ── System group (internal tools) ─────────────────────────────────────────

// TestSystemToolGroupBypassesPolicyAndJudge verifies the core replacement for
// the old name-based IsInternalTool gate: a tool whose GROUP is system
// executes directly, bypassing a deny-everything group map, the judge, and
// confirmation.
func TestSystemToolGroupBypassesPolicyAndJudge(t *testing.T) {
	registry := NewToolRegistry()
	// Deny every configurable group: system must still execute.
	denyAll := map[sdktools.ToolGroup]sdktools.ToolPolicy{}
	for _, g := range sdktools.AllToolGroups() {
		if g != sdktools.GroupSystem {
			denyAll[g] = sdktools.PolicyAlwaysDeny
		}
	}
	registry.SetGroupPolicies(denyAll)
	registry.Register(newMockSystemTool("finish", "Finish the task"))

	judge, provider := newStrictJudge("VERDICT: CONFIRM\nREASON: never runs", nil)
	registry.SetJudge(judge)

	confirmCalled := false
	registry.SetConfirmFunc(func(context.Context, sdktools.ConfirmationRequest) (sdktools.ConfirmationResponse, error) {
		confirmCalled = true
		return sdktools.ConfirmAllowOnce, nil
	})

	result, err := registry.Execute(context.Background(), "finish", json.RawMessage(`{"status":"success"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Errorf("expected IsError to be false for a system tool, got true with content: %s", result.Content)
	}
	if result.Content != `{"status":"success"}` {
		t.Errorf("expected content %q, got %q", `{"status":"success"}`, result.Content)
	}
	if confirmCalled {
		t.Error("expected confirmFunc NOT to be called for a system tool")
	}
	if got := provider.callCount(); got != 0 {
		t.Errorf("strict judge calls = %d, want 0 for a system tool", got)
	}
}

// TestSystemGroupByDeclarationNotName proves the bypass keys on the GROUP,
// not the name: a tool NAMED "finish" but declaring local_write is NOT
// bypassed, while a tool with an arbitrary name in the system group IS.
func TestSystemGroupByDeclarationNotName(t *testing.T) {
	registry := NewToolRegistry()
	setDefaultGroupPolicies(registry)
	// "finish" name but local_write group → must confirm.
	registry.Register(newMockTool("finish", "name-collision tool"))

	confirmCalled := false
	registry.SetConfirmFunc(func(context.Context, sdktools.ConfirmationRequest) (sdktools.ConfirmationResponse, error) {
		confirmCalled = true
		return sdktools.ConfirmAllowOnce, nil
	})

	if _, err := registry.Execute(context.Background(), "finish", json.RawMessage(`{}`)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !confirmCalled {
		t.Error("a tool named 'finish' but grouped local_write must still confirm — the gate keys on Group(), not the name")
	}
}

// TestSystemToolDisabledInNoProjectStillBlocked verifies gate ordering: the
// disabled-tools gate (No Project mode) runs BEFORE the system-group bypass,
// so a disabled system tool (semantic_search) is blocked at execution time.
func TestSystemToolDisabledInNoProjectStillBlocked(t *testing.T) {
	registry := NewToolRegistry()
	registry.Register(newMockSystemTool("semantic_search", "vector search"))
	registry.SetDisabledTools(map[string]bool{"semantic_search": true})

	result, err := registry.Execute(context.Background(), "semantic_search", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected IsError=true: disabled tools are blocked even in the system group")
	}
	if !strings.Contains(result.Content, "No Project mode") {
		t.Errorf("expected the No Project block message, got %q", result.Content)
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

// TestGoalModeTools_AreAllSystem is a completeness guard over the REAL tool
// instances: every goal-mode tool must (a) declare the system group on its
// BaseTool — system classification is what hides it from the security UI and
// exempts it from policy/confirmation — and (b) appear in the goalModeTools
// availability set, so the orchestrator can strip it from non-goal runs.
func TestGoalModeTools_AreAllSystem(t *testing.T) {
	for _, tool := range []sdktools.Tool{
		NewProposeGoalTool(),
		NewDeclareGoalStatusTool(),
		NewDeclareVerificationTool(),
	} {
		name := tool.Name()
		if !IsGoalModeTool(name) {
			t.Errorf("goal tool %q is missing from the goalModeTools availability set", name)
		}
		if got := tool.Group(); got != sdktools.GroupSystem {
			t.Errorf("goal tool %q declares group %q, want %q — it must stay hidden from the security UI and exempt from policies", name, got, sdktools.GroupSystem)
		}
	}

	// The availability set must not outgrow the real goal tools: a fourth
	// entry would strip a tool that no longer exists here.
	if len(goalModeTools) != 3 {
		t.Errorf("goalModeTools has %d entries, want 3 — update TestGoalModeTools_AreAllSystem with the added/removed tool", len(goalModeTools))
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
			t.Errorf("expected goal-mode tool %q to be stripped", removed)
		}
	}
	for _, kept := range []string{"read_file", "bash_exec", "finish", "delegate", "declare_plan", "reflect"} {
		if !names[kept] {
			t.Errorf("expected non-goal tool %q to be kept", kept)
		}
	}
}

// ── Pre-execute hook ──────────────────────────────────────────────────────

// TestPreExecuteHook_BlocksUntilReleased verifies that the hook can block
// tool execution until released, and then the tool result is correct.
func TestPreExecuteHook_BlocksUntilReleased(t *testing.T) {
	registry := NewToolRegistry()
	setDefaultGroupPolicies(registry)
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

// TestPreExecuteHook_ErrorPreventsExecution verifies that when the hook
// returns an error, tool execution is aborted and the error is returned in
// the result.
func TestPreExecuteHook_ErrorPreventsExecution(t *testing.T) {
	registry := NewToolRegistry()
	setDefaultGroupPolicies(registry)
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

// TestPreExecuteHook_NotCalledForSystemTools verifies that the pre-execute
// hook is NOT invoked for system tools (e.g., ask_user).
func TestPreExecuteHook_NotCalledForSystemTools(t *testing.T) {
	registry := NewToolRegistry()
	// Register a mock system tool under an internal tool name
	tool := newMockSystemTool("ask_user", "System ask_user tool")
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
		t.Error("expected IsError to be false for system tool")
	}
	if hookCalled {
		t.Error("expected pre-execute hook NOT to be called for system tool 'ask_user'")
	}
}

// TestPreExecuteHook_ReceivesCorrectSource verifies that the hook receives
// the correct source string for a tool registered with RegisterWithSource.
func TestPreExecuteHook_ReceivesCorrectSource(t *testing.T) {
	registry := NewToolRegistry()
	setDefaultGroupPolicies(registry)
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

// ── Smart Approve ─────────────────────────────────────────────────────────

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

// TestSmartApprove_UnavailableJudgeKeepsConcreteReason pins the reasoning
// shown when Smart Approve is enabled but no strict judge is configured: the
// confirmation must still explain WHY the call needs confirmation (the
// concrete reason — a soft escalation or the per-tool default), prefixed only
// with a note that the strict judge could not decide, instead of degrading to
// a generic "judge unavailable" text that hides the actual cause.
func TestSmartApprove_UnavailableJudgeKeepsConcreteReason(t *testing.T) {
	registry := NewToolRegistry()
	setDefaultGroupPolicies(registry)
	registry.SetSmartApprove(true)
	registry.Register(newMockTool("bash_exec", "runs a command"))

	var gotRequest sdktools.ConfirmationRequest
	registry.SetConfirmFunc(func(_ context.Context, req sdktools.ConfirmationRequest) (sdktools.ConfirmationResponse, error) {
		gotRequest = req
		return sdktools.ConfirmDeny, nil
	})

	if _, err := registry.Execute(context.Background(), "bash_exec", json.RawMessage(`{"command":"ls"}`)); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	const wantPrefix = "Strict judge is unavailable; "
	if !strings.HasPrefix(gotRequest.JudgeReasoning, wantPrefix) {
		t.Errorf("JudgeReasoning = %q, want prefix %q", gotRequest.JudgeReasoning, wantPrefix)
	}
	if want := defaultConfirmReason("bash_exec"); !strings.Contains(gotRequest.JudgeReasoning, want) {
		t.Errorf("JudgeReasoning = %q, want it to contain the per-tool default reason %q", gotRequest.JudgeReasoning, want)
	}
}

// TestSmartApprove_UserConfirmHardReasonSkipsStrictJudge proves hard reasons
// never reach Smart Approve on the user_confirm path: a user_confirm tool
// with a HARD judge reason confirms directly even with Smart Approve on and
// the strict judge scripted to ALLOW.
func TestSmartApprove_UserConfirmHardReasonSkipsStrictJudge(t *testing.T) {
	registry := NewToolRegistry()
	setDefaultGroupPolicies(registry)
	registry.SetSmartApprove(true)
	registry.Register(newMockExecuteJudgerTool("bash_exec", sdktools.JudgeOutcome{
		Reason:   "command matches blacklist pattern: mkfs",
		Severity: sdktools.JudgeSeverityHard,
	}))

	judge, provider := newStrictJudge("VERDICT: ALLOW\nREASON: trust me", nil)
	registry.SetJudge(judge)

	var req sdktools.ConfirmationRequest
	confirmCalled := false
	registry.SetConfirmFunc(func(_ context.Context, r sdktools.ConfirmationRequest) (sdktools.ConfirmationResponse, error) {
		confirmCalled = true
		req = r
		return sdktools.ConfirmDeny, nil
	})

	result, err := registry.Execute(context.Background(), "bash_exec", json.RawMessage(`{"command":"mkfs.ext4 /dev/sda1"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !confirmCalled {
		t.Fatal("expected confirmation: hard reasons must not be auto-approved by Smart Approve")
	}
	if !result.IsError {
		t.Error("expected IsError=true after denying the forced confirmation")
	}
	if !strings.Contains(req.JudgeReasoning, "mkfs") {
		t.Errorf("expected the blacklist reason to surface, got %q", req.JudgeReasoning)
	}
	if !req.DisableJudge {
		t.Error("hard-reason confirmation must disable the advisory judge")
	}
	if got := provider.callCount(); got != 0 {
		t.Errorf("strict judge calls = %d, want 0 (hard reasons short-circuit Smart Approve)", got)
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

// TestSmartApprove_OnlyEffectiveUserConfirm verifies Smart Approve applies
// only to the effective user_confirm posture: allow-group clean calls
// execute without consulting the strict judge, and deny groups stay blocked.
func TestSmartApprove_OnlyEffectiveUserConfirm(t *testing.T) {
	for _, tt := range []struct {
		name   string
		group  sdktools.ToolGroup
		policy sdktools.ToolPolicy
	}{
		{name: "allow group executes without strict judge", group: sdktools.GroupLocalRead, policy: sdktools.PolicyAlwaysAllow},
		{name: "deny group stays blocked without strict judge", group: sdktools.GroupLocalWrite, policy: sdktools.PolicyAlwaysDeny},
	} {
		t.Run(tt.name, func(t *testing.T) {
			registry := NewToolRegistry()
			registry.SetSmartApprove(true)
			registry.SetGroupPolicies(map[sdktools.ToolGroup]sdktools.ToolPolicy{tt.group: tt.policy})
			tool := newMockReadOnlyTool("policy_tool", "policy test")
			tool.group = tt.group
			registry.Register(tool)
			judge, provider := newStrictJudge("VERDICT: ALLOW\nREASON: safe", nil)
			registry.SetJudge(judge)

			result, err := registry.Execute(context.Background(), "policy_tool", json.RawMessage(`{"data":"test"}`))
			if err != nil {
				t.Fatalf("Execute() error = %v", err)
			}
			if tt.policy == sdktools.PolicyAlwaysDeny && !result.IsError {
				t.Error("deny group must remain blocked")
			}
			if tt.policy == sdktools.PolicyAlwaysAllow && result.IsError {
				t.Error("allow group must execute")
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
		t.Fatal("nil ConfirmFunc must deny user_confirm execution")
	}
	if !strings.Contains(result.Content, "confirmation is unavailable") {
		t.Errorf("unexpected denial content: %q", result.Content)
	}
}

// ── Post-execute hook ─────────────────────────────────────────────────────

// TestPostExecuteHook_CalledAfterSuccessfulExecution verifies that the
// post-execute hook is called with the correct tool name and result after a
// successful tool execution.
func TestPostExecuteHook_CalledAfterSuccessfulExecution(t *testing.T) {
	registry := NewToolRegistry()
	setDefaultGroupPolicies(registry)
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

// TestPostExecuteHook_NotCalledForSystemTools verifies that the hook is NOT
// invoked for system tools (e.g. ask_user, finish).
func TestPostExecuteHook_NotCalledForSystemTools(t *testing.T) {
	registry := NewToolRegistry()
	// Register a mock system tool under an internal tool name.
	tool := newMockSystemTool("ask_user", "System ask_user tool")
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
		t.Error("expected post-execute hook NOT to be called for system tool 'ask_user'")
	}
}

// TestPostExecuteHook_CalledOnGroupDeny verifies that the hook IS called
// (with an error result) when a tool is blocked by its group policy. The
// defer covers all return paths below the early gates — the hook filters on
// result.IsError.
func TestPostExecuteHook_CalledOnGroupDeny(t *testing.T) {
	registry := NewToolRegistry()
	tool := newMockTool("denied_tool", "A tool in a deny group")
	registry.Register(tool)
	registry.SetGroupPolicies(map[sdktools.ToolGroup]sdktools.ToolPolicy{
		sdktools.GroupLocalWrite: sdktools.PolicyAlwaysDeny,
	})

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

// TestPostExecuteHook_PreservedInClone verifies that the hook is shared with
// cloned registries (per-session clones inherit the hook).
func TestPostExecuteHook_PreservedInClone(t *testing.T) {
	registry := NewToolRegistry()
	setDefaultGroupPolicies(registry)
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

// ── ToolFilter ────────────────────────────────────────────────────────────

// TestToolFilter_BlocksRegistration verifies that a filter can reject tools
// during registration.
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

// TestToolFilter_AllowsRegistration verifies that a permissive filter allows
// tools.
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

// TestToolFilter_NilAllowsAll verifies that a nil filter allows all tools
// (default behavior).
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

// TestAutoApproval_DenyGroupRespected tests that a deny group is still
// respected even when all paths are within the workspace.
func TestAutoApproval_DenyGroupRespected(t *testing.T) {
	registry := NewToolRegistry()
	tool := newMockTool("always_deny", "A tool in a deny-postured group")
	registry.Register(tool)
	registry.SetGroupPolicies(map[sdktools.ToolGroup]sdktools.ToolPolicy{
		sdktools.GroupLocalWrite: sdktools.PolicyAlwaysDeny,
	})

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
		t.Error("expected IsError to be true for a deny group")
	}
	if !strings.Contains(result.Content, "blocked by security policy") {
		t.Errorf("expected security policy error, got: %s", result.Content)
	}
	if confirmCalled {
		t.Error("expected confirmFunc NOT to be called for a deny group")
	}
}

// ── Gate ordering ─────────────────────────────────────────────────────────

// TestGateOrder_DenyBeforeJudgeAndSymlink verifies a deny group blocks BEFORE
// any judge or symlink reasoning runs: the block message is the policy
// message, not a confirmation.
func TestGateOrder_DenyBeforeJudgeAndSymlink(t *testing.T) {
	ws := t.TempDir()
	outside := t.TempDir()
	link := filepath.Join(ws, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}

	registry := NewToolRegistry()
	registry.SetGroupPolicies(map[sdktools.ToolGroup]sdktools.ToolPolicy{
		sdktools.GroupLocalRead: sdktools.PolicyAlwaysDeny,
	})
	registry.Register(builtins.NewReadFileTool()) // judge would soft-deny out-of-root too

	confirmCalled := false
	registry.SetConfirmFunc(func(context.Context, sdktools.ConfirmationRequest) (sdktools.ConfirmationResponse, error) {
		confirmCalled = true
		return sdktools.ConfirmAllowOnce, nil
	})

	ctx := sdktools.WithWorkspacePath(context.Background(), ws)
	input, _ := json.Marshal(map[string]string{"path": filepath.Join(link, "file.txt")})

	result, err := registry.Execute(ctx, "read_file", input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected the deny group to block the call")
	}
	if !strings.Contains(result.Content, "blocked by security policy") {
		t.Errorf("expected the policy block message, got %q", result.Content)
	}
	if confirmCalled {
		t.Error("deny must block before any confirmation path is reached")
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
