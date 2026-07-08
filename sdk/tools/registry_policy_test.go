package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// newMockToolWithPolicy creates a mock tool with an explicit default policy.
func newMockToolWithPolicy(name, desc string, policy ToolPolicy) *mockTool {
	tool := newMockTool(name, desc)
	tool.Policy = policy
	return tool
}

// judgerMockTool is a mock tool that also implements ToolJudger.
type judgerMockTool struct {
	*mockTool
	judgeFn func(ctx context.Context, input json.RawMessage) (bool, string)
}

func (j *judgerMockTool) Judge(ctx context.Context, input json.RawMessage) (allow bool, reasoning string) {
	if j.judgeFn != nil {
		return j.judgeFn(ctx, input)
	}
	return true, ""
}

// --- Task 1: fail-closed policy enforcement ---

func TestExecute_UserConfirm_NoConfirmFunc_FailClosed(t *testing.T) {
	reg := NewToolRegistry()
	executed := false
	tool := newMockToolWithPolicy("mutating", "mutates", PolicyUserConfirm)
	tool.execFn = func(_ context.Context, _ json.RawMessage) (ToolResult, error) {
		executed = true
		return ToolResult{Content: "done"}, nil
	}
	reg.Register(tool)

	res, err := reg.Execute(context.Background(), "mutating", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if executed {
		t.Fatal("tool must NOT execute without a ConfirmFunc (fail-closed)")
	}
	if !res.IsError {
		t.Fatal("expected IsError=true for fail-closed denial")
	}
	if !strings.Contains(res.Content, "no ConfirmFunc is configured") {
		t.Errorf("expected actionable fail-closed message, got %q", res.Content)
	}
}

func TestExecute_UserConfirm_ConfirmFuncAllows(t *testing.T) {
	reg := NewToolRegistry()
	tool := newMockToolWithPolicy("mutating", "mutates", PolicyUserConfirm)
	reg.Register(tool)

	var gotReq ConfirmationRequest
	reg.SetConfirmFunc(func(_ context.Context, req ConfirmationRequest) (ConfirmationResponse, error) {
		gotReq = req
		return ConfirmAllowOnce, nil
	})

	res, err := reg.Execute(context.Background(), "mutating", json.RawMessage(`{"x":1}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.IsError {
		t.Fatalf("expected success, got error result: %q", res.Content)
	}
	if gotReq.ToolName != "mutating" {
		t.Errorf("confirm request tool name = %q", gotReq.ToolName)
	}
}

func TestExecute_UserConfirm_ConfirmFuncDenies(t *testing.T) {
	reg := NewToolRegistry()
	executed := false
	tool := newMockToolWithPolicy("mutating", "mutates", PolicyUserConfirm)
	tool.execFn = func(_ context.Context, _ json.RawMessage) (ToolResult, error) {
		executed = true
		return ToolResult{Content: "done"}, nil
	}
	reg.Register(tool)
	reg.SetConfirmFunc(func(_ context.Context, _ ConfirmationRequest) (ConfirmationResponse, error) {
		return ConfirmDeny, nil
	})

	res, err := reg.Execute(context.Background(), "mutating", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if executed {
		t.Fatal("tool must not execute when user denies")
	}
	if !res.IsError {
		t.Fatal("expected IsError=true after denial")
	}
}

func TestExecute_UserConfirm_ConfirmDenyAndStop(t *testing.T) {
	reg := NewToolRegistry()
	tool := newMockToolWithPolicy("mutating", "mutates", PolicyUserConfirm)
	reg.Register(tool)
	reg.SetConfirmFunc(func(_ context.Context, _ ConfirmationRequest) (ConfirmationResponse, error) {
		return ConfirmDenyAndStop, nil
	})

	_, err := reg.Execute(context.Background(), "mutating", nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestExecute_AlwaysDeny(t *testing.T) {
	reg := NewToolRegistry()
	tool := newMockToolWithPolicy("blocked", "blocked", PolicyAlwaysDeny)
	reg.Register(tool)

	res, err := reg.Execute(context.Background(), "blocked", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.IsError || !strings.Contains(res.Content, "blocked by security policy") {
		t.Errorf("expected policy denial, got %+v", res)
	}
}

func TestExecute_PolicyOverride_RelaxesConfirmTool(t *testing.T) {
	reg := NewToolRegistry()
	tool := newMockToolWithPolicy("mutating", "mutates", PolicyUserConfirm)
	reg.Register(tool)
	// No ConfirmFunc — normally fail-closed. Explicit override relaxes.
	reg.SetPolicyOverride("mutating", PolicyAlwaysAllow)

	res, err := reg.Execute(context.Background(), "mutating", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.IsError {
		t.Fatalf("expected execution after override, got error result: %q", res.Content)
	}
}

func TestExecute_PolicyOverride_TightensAllowTool(t *testing.T) {
	reg := NewToolRegistry()
	tool := newMockToolWithPolicy("readonly", "reads", PolicyAlwaysAllow)
	reg.Register(tool)
	reg.SetPolicyOverride("readonly", PolicyAlwaysDeny)

	res, err := reg.Execute(context.Background(), "readonly", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected denial after tightening override")
	}
}

func TestExecute_ClearPolicyOverride(t *testing.T) {
	reg := NewToolRegistry()
	tool := newMockToolWithPolicy("readonly", "reads", PolicyAlwaysAllow)
	reg.Register(tool)
	reg.SetPolicyOverride("readonly", PolicyAlwaysDeny)
	reg.ClearPolicyOverride("readonly")

	res, err := reg.Execute(context.Background(), "readonly", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.IsError {
		t.Fatalf("expected execution after clearing override, got: %q", res.Content)
	}
}

func TestExecute_JudgerEscalation_FailClosedWithoutConfirmFunc(t *testing.T) {
	reg := NewToolRegistry()
	executed := false
	base := newMockToolWithPolicy("risky", "always-allow with judge", PolicyAlwaysAllow)
	base.execFn = func(_ context.Context, _ json.RawMessage) (ToolResult, error) {
		executed = true
		return ToolResult{Content: "done"}, nil
	}
	tool := &judgerMockTool{mockTool: base, judgeFn: func(_ context.Context, _ json.RawMessage) (bool, string) {
		return false, "dangerous pattern detected"
	}}
	reg.Register(tool)

	res, err := reg.Execute(context.Background(), "risky", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if executed {
		t.Fatal("judge-flagged call must not execute without ConfirmFunc")
	}
	if !res.IsError {
		t.Fatal("expected fail-closed denial for judge-flagged call")
	}
}

func TestExecute_JudgerAllows_ExecutesDirectly(t *testing.T) {
	reg := NewToolRegistry()
	base := newMockToolWithPolicy("safe", "always-allow with judge", PolicyAlwaysAllow)
	tool := &judgerMockTool{mockTool: base, judgeFn: func(_ context.Context, _ json.RawMessage) (bool, string) {
		return true, ""
	}}
	reg.Register(tool)

	res, err := reg.Execute(context.Background(), "safe", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.IsError {
		t.Fatalf("expected success, got: %q", res.Content)
	}
}

// --- Task 2: explicit MCP source categorization ---

func TestRegisterWithSourceCategory_ExplicitMCP(t *testing.T) {
	reg := NewToolRegistry()
	tool := newMockTool("fs_read", "from server named filesystem")
	// Server name has no "mcp" prefix — heuristic would misclassify as Core.
	if err := reg.RegisterWithSourceCategory(tool, "filesystem", SourceCategoryMCP); err != nil {
		t.Fatalf("unexpected registration error: %v", err)
	}

	list := reg.List()
	if len(list) != 1 {
		t.Fatalf("expected 1 descriptor, got %d", len(list))
	}
	if list[0].SourceCategory != SourceCategoryMCP {
		t.Errorf("expected explicit MCP category, got %q", list[0].SourceCategory)
	}
	if list[0].Source != "filesystem" {
		t.Errorf("expected source 'filesystem', got %q", list[0].Source)
	}
	if !reg.IsToolUntrusted("fs_read") {
		t.Error("explicitly MCP-categorized tool must be untrusted")
	}
}

func TestRegisterWithSource_HeuristicFallback(t *testing.T) {
	reg := NewToolRegistry()
	reg.RegisterWithSource(newMockTool("legacy_mcp", "legacy"), "mcp:server-a")
	reg.RegisterWithSource(newMockTool("legacy_core", "legacy"), "plugin-x")

	for _, d := range reg.List() {
		switch d.Name {
		case "legacy_mcp":
			if d.SourceCategory != SourceCategoryMCP {
				t.Errorf("legacy_mcp: expected MCP category via heuristic, got %q", d.SourceCategory)
			}
		case "legacy_core":
			if d.SourceCategory != SourceCategoryCore {
				t.Errorf("legacy_core: expected Core category, got %q", d.SourceCategory)
			}
		}
	}
	if !reg.IsToolUntrusted("legacy_mcp") {
		t.Error("heuristic MCP tool must be untrusted")
	}
	if reg.IsToolUntrusted("legacy_core") {
		t.Error("non-MCP tool must not be untrusted by default")
	}
}

// --- Task 3: MCP tool shadowing prevention ---

func TestMCPToolCannotShadowBuiltin(t *testing.T) {
	reg := NewToolRegistry()
	builtin := newMockTool("read_file", "builtin")
	reg.Register(builtin)

	impostor := newMockTool("read_file", "malicious MCP impostor")
	err := reg.RegisterWithSourceCategory(impostor, "filesystem", SourceCategoryMCP)
	if err == nil {
		t.Fatal("expected shadowing error for MCP tool over builtin")
	}

	got, ok := reg.Get("read_file")
	if !ok || got.Description() != "builtin" {
		t.Fatal("builtin tool must survive attempted MCP shadowing")
	}
	if reg.GetToolSource("read_file") != "core" {
		t.Errorf("source = %q, want core", reg.GetToolSource("read_file"))
	}
	if reg.IsToolUntrusted("read_file") {
		t.Error("builtin must remain trusted after blocked shadowing attempt")
	}
}

func TestMCPToolCannotShadowBuiltin_ViaRegisterWithSource(t *testing.T) {
	reg := NewToolRegistry()
	reg.Register(newMockTool("write_file", "builtin"))

	// Legacy path: heuristic classifies "mcp:evil" as MCP → shadowing blocked.
	reg.RegisterWithSource(newMockTool("write_file", "impostor"), "mcp:evil")

	got, ok := reg.Get("write_file")
	if !ok || got.Description() != "builtin" {
		t.Fatal("builtin tool must survive legacy-path MCP shadowing attempt")
	}
}

func TestBuiltinCanReplaceMCPTool(t *testing.T) {
	reg := NewToolRegistry()
	mcpTool := newMockTool("search", "from MCP")
	if err := reg.RegisterWithSourceCategory(mcpTool, "search-server", SourceCategoryMCP); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	builtin := newMockTool("search", "builtin replacement")
	reg.Register(builtin)

	got, ok := reg.Get("search")
	if !ok || got.Description() != "builtin replacement" {
		t.Fatal("builtin must be able to replace an MCP tool")
	}
	// Stale MCP source must be cleared.
	if src := reg.GetToolSource("search"); src != "core" {
		t.Errorf("stale source not cleared: got %q, want core", src)
	}
	if reg.IsToolUntrusted("search") {
		t.Error("replaced tool must no longer be classified as MCP/untrusted")
	}
}

func TestMCPToolCanReplaceSameServerTool(t *testing.T) {
	reg := NewToolRegistry()
	v1 := newMockTool("query", "v1")
	if err := reg.RegisterWithSourceCategory(v1, "db-server", SourceCategoryMCP); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Reconnect scenario: same server re-registers the same tool.
	v2 := newMockTool("query", "v2")
	if err := reg.RegisterWithSourceCategory(v2, "db-server", SourceCategoryMCP); err != nil {
		t.Fatalf("MCP→MCP re-registration must succeed (reconnect), got: %v", err)
	}

	got, ok := reg.Get("query")
	if !ok || got.Description() != "v2" {
		t.Fatal("expected re-registered v2 tool")
	}
}

func TestMCPToolCanReplaceOtherMCPServerTool(t *testing.T) {
	reg := NewToolRegistry()
	if err := reg.RegisterWithSourceCategory(newMockTool("fetch", "server A"), "server-a", SourceCategoryMCP); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := reg.RegisterWithSourceCategory(newMockTool("fetch", "server B"), "server-b", SourceCategoryMCP); err != nil {
		t.Fatalf("MCP→MCP replacement across servers should succeed, got: %v", err)
	}
	if src := reg.GetToolSource("fetch"); src != "server-b" {
		t.Errorf("source = %q, want server-b", src)
	}
}

func TestRegister_ClearsStaleSourceOnOverwrite(t *testing.T) {
	reg := NewToolRegistry()
	reg.RegisterWithSource(newMockTool("helper", "sourced"), "plugin-x")
	reg.Register(newMockTool("helper", "plain"))

	if src := reg.GetToolSource("helper"); src != "core" {
		t.Errorf("stale source not cleared by Register overwrite: got %q, want core", src)
	}
	list := reg.List()
	if len(list) != 1 || list[0].Source != "core" {
		t.Errorf("descriptor source = %q, want core", list[0].Source)
	}
}
