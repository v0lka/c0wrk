package core

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	sdktools "github.com/v0lka/sp4rk/tools"

	coretools "github.com/v0lka/c0wrk/core/tools"
)

// mcpCategoryParityTool uses the system group to prove the server gate runs
// before the system bypass. Its counter detects a blocked call reaching the body.
type mcpCategoryParityTool struct {
	sdktools.BaseTool
	calls int
}

func (p *mcpCategoryParityTool) Execute(context.Context, json.RawMessage) (sdktools.ToolResult, error) {
	p.calls++
	return sdktools.ToolResult{Content: "ok"}, nil
}

func TestMCPModeGating_CoreServerDescriptorDispatchParity(t *testing.T) {
	for _, mentioned := range []bool{false, true} {
		name := "unmentioned"
		if mentioned {
			name = "mentioned"
		}
		t.Run(name, func(t *testing.T) {
			r := coretools.NewToolRegistry()
			mcp := &mcpCategoryParityTool{BaseTool: sdktools.BaseTool{
				ToolName: "core_server_probe", Schema: json.RawMessage(`{"type":"object"}`), ToolGroup: sdktools.GroupSystem,
			}}
			builtin := &mcpCategoryParityTool{BaseTool: sdktools.BaseTool{
				ToolName: "builtin_probe", Schema: json.RawMessage(`{"type":"object"}`), ToolGroup: sdktools.GroupSystem,
			}}
			if err := r.RegisterWithSourceCategory(mcp, "core", sdktools.SourceCategoryMCP); err != nil {
				t.Fatalf("RegisterWithSourceCategory(core, MCP) = %v, want nil", err)
			}
			r.Register(builtin)
			var diagnostics []expectedCoreDiagnostic
			if !mentioned {
				diagnostics = append(diagnostics, expectedCoreDiagnostic{
					level: "WARN", message: "security: tool blocked by MCP server mode gating",
					fields: map[string]string{"tool": mcp.Name(), "server": "core"},
				})
			}
			logger := expectedCoreLogger(t, diagnostics...)
			r.SetLogger(logger)
			o := &Orchestrator{config: OrchestratorConfig{MCPServerModes: map[string]string{"core": mcpModeManual}}}
			var mentions []string
			if mentioned {
				mentions = []string{"core"}
			}
			catalog, ctx := o.applyMCPModeGating(context.Background(), r.List(), mentions)
			available := make(map[string]bool, len(catalog))
			for _, d := range catalog {
				available[d.Name] = true
			}
			if available[mcp.Name()] != mentioned || !available[builtin.Name()] {
				t.Fatalf("applyMCPModeGating(core, mentioned=%v) = %v, want MCP present=%v and builtin present", mentioned, available, mentioned)
			}
			for _, probe := range []*mcpCategoryParityTool{mcp, builtin} {
				result, err := r.Execute(ctx, probe.Name(), json.RawMessage(`{}`))
				wantAllowed := available[probe.Name()]
				if err != nil || result.IsError == wantAllowed {
					t.Errorf("Execute(%q, mentioned=%v) = (%v, %v), want allowed=%v matching descriptor", probe.Name(), mentioned, result, err, wantAllowed)
				}
				wantCalls := 0
				if wantAllowed {
					wantCalls = 1
				}
				if probe.calls != wantCalls {
					t.Errorf("Execute(%q, mentioned=%v) body calls = %d, want %d", probe.Name(), mentioned, probe.calls, wantCalls)
				}
				if !wantAllowed && !strings.Contains(result.Content, `MCP server "core"`) {
					t.Errorf("Execute(%q) rejection = %q, want MCP server core attribution", probe.Name(), result.Content)
				}
			}
		})
	}
}
