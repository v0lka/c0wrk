package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	sdktools "github.com/v0lka/sp4rk/tools"
)

// mcpGateProbeTool is a minimal system-group tool whose source tag is set by
// the test registry — GroupSystem keeps the ALLOWED path short (it executes
// directly at Gate 3), while the gate under test fires BEFORE Gate 3, so the
// blocked path is exercised regardless of the group.
type mcpGateProbeTool struct {
	name string
}

func (m *mcpGateProbeTool) Name() string                 { return m.name }
func (m *mcpGateProbeTool) Description() string          { return "probe" }
func (m *mcpGateProbeTool) InputSchema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (m *mcpGateProbeTool) Execute(context.Context, json.RawMessage) (sdktools.ToolResult, error) {
	return sdktools.ToolResult{Content: "ok"}, nil
}
func (m *mcpGateProbeTool) DefaultPolicy() sdktools.ToolPolicy { return sdktools.PolicyAlwaysAllow }
func (m *mcpGateProbeTool) IsUntrusted() bool                  { return false }
func (m *mcpGateProbeTool) Group() sdktools.ToolGroup          { return sdktools.GroupSystem }

func newMCPGateTestRegistry() *ToolRegistry {
	r := NewToolRegistry()
	if err := r.RegisterWithSourceCategory(&mcpGateProbeTool{name: "srv_x_tool"}, "server-x", sdktools.SourceCategoryMCP); err != nil {
		panic(err)
	}
	r.Register(&mcpGateProbeTool{name: "builtin_tool"})
	return r
}

// TestExecute_MCPGateUsesSourceCategory separates the MCP category from the
// server label, including labels shared with true builtins.
func TestExecute_MCPGateUsesSourceCategory(t *testing.T) {
	tests := []struct {
		name     string
		source   string
		category sdktools.ToolSourceCategory
		blocked  bool
	}{
		{name: "MCP server named core", source: "core", category: sdktools.SourceCategoryMCP, blocked: true},
		{name: "builtin sharing core source", source: "core", category: sdktools.SourceCategoryCore},
		{name: "builtin sharing gated server source", source: "server-x", category: sdktools.SourceCategoryCore},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewToolRegistry()
			if err := r.RegisterWithSourceCategory(&mcpGateProbeTool{name: "probe"}, tt.source, tt.category); err != nil {
				t.Fatalf("RegisterWithSourceCategory(%q, %q) = %v, want nil", tt.source, tt.category, err)
			}
			var diagnostics []expectedToolDiagnostic
			if tt.blocked {
				diagnostics = append(diagnostics, expectedToolDiagnostic{
					message: "security: tool blocked by MCP server mode gating",
					attrs:   map[string]string{"tool": "probe", "server": tt.source},
				})
			}
			captureToolDiagnostics(t, r, diagnostics...)
			ctx := WithGatedMCPServers(context.Background(), map[string]bool{tt.source: true})
			result, err := r.Execute(ctx, "probe", json.RawMessage(`{}`))
			if err != nil || result.IsError != tt.blocked {
				t.Errorf("Execute(source=%q, category=%q) = (%v, %v), want blocked=%v", tt.source, tt.category, result, err, tt.blocked)
			}
		})
	}
}

// TestExecute_GatedMCPServerRejected is the defense-in-depth contract: a
// hallucinated call naming a tool of a gated-off MCP server (manual without a
// mention, or disabled) is rejected IN Execute with an actionable error even
// though the descriptor filters already hid it from every catalog.
func TestExecute_GatedMCPServerRejected(t *testing.T) {
	r := newMCPGateTestRegistry()
	// The blocked dispatch emits exactly one WARN security diagnostic; the
	// expected-diagnostic contract below intercepts it at the source (the
	// clean-dispatch paths after it must stay silent).
	captureToolDiagnostics(t, r, expectedToolDiagnostic{
		message: "security: tool blocked by MCP server mode gating",
		attrs:   map[string]string{"tool": "srv_x_tool", "server": "server-x"},
	})
	gated := WithGatedMCPServers(context.Background(), map[string]bool{"server-x": true})

	res, err := r.Execute(gated, "srv_x_tool", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Execute returned Go error %v, want error RESULT", err)
	}
	if !res.IsError {
		t.Fatal("Execute must reject a gated-off MCP server's tool")
	}
	if !strings.Contains(res.Content, "server-x") {
		t.Fatalf("rejection must name the gated server, got %q", res.Content)
	}

	// Same call WITHOUT the gated set (e.g. a task that mentioned the server,
	// or no manual servers configured) dispatches normally.
	res, err = r.Execute(context.Background(), "srv_x_tool", json.RawMessage(`{}`))
	if err != nil || res.IsError {
		t.Fatalf("ungated Execute = (%v, %v), want clean dispatch", res, err)
	}

	// A gated set that does NOT contain the tool's server blocks nothing.
	res, err = r.Execute(WithGatedMCPServers(context.Background(), map[string]bool{"other-srv": true}), "srv_x_tool", json.RawMessage(`{}`))
	if err != nil || res.IsError {
		t.Fatalf("unrelated gated set must not block server-x, got (%v, %v)", res, err)
	}

	// Built-in tools are exempt by category, not by their source label.
	res, err = r.Execute(gated, "builtin_tool", json.RawMessage(`{}`))
	if err != nil || res.IsError {
		t.Fatalf("built-in tool must not be gated, got (%v, %v)", res, err)
	}
}
