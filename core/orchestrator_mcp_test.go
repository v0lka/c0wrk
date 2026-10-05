package core

import (
	"context"
	"testing"

	sdktools "github.com/v0lka/sp4rk/tools"

	coretools "github.com/v0lka/c0wrk/core/tools"
)

func TestGatedMCPServerSet(t *testing.T) {
	modes := map[string]string{
		"auto-srv":   mcpModeAuto,
		"manual-a":   mcpModeManual,
		"manual-b":   mcpModeManual,
		"disabled-s": mcpModeDisabled,
		"":           "", // absent/empty mode → auto
	}
	tests := []struct {
		name      string
		mentioned []string
		want      map[string]bool
	}{
		{"no mentions: manual + disabled gated", nil, map[string]bool{"manual-a": true, "manual-b": true, "disabled-s": true}},
		{"mention unblocks only the mentioned manual server", []string{"manual-a"}, map[string]bool{"manual-b": true, "disabled-s": true}},
		{"mentioning an auto or disabled server changes nothing", []string{"auto-srv", "disabled-s"}, map[string]bool{"manual-a": true, "manual-b": true, "disabled-s": true}},
		{"all manual mentioned: only disabled stays gated", []string{"manual-a", "manual-b"}, map[string]bool{"disabled-s": true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := gatedMCPServerSet(modes, tt.mentioned)
			if len(got) != len(tt.want) {
				t.Fatalf("gatedMCPServerSet(%v) = %v, want %v", tt.mentioned, got, tt.want)
			}
			for name := range tt.want {
				if !got[name] {
					t.Errorf("server %q must be gated, got %v", name, got)
				}
			}
		})
	}
	// No modes configured → nothing gated (zero regression for default config).
	if got := gatedMCPServerSet(nil, nil); got != nil {
		t.Fatalf("gatedMCPServerSet(nil) = %v, want nil", got)
	}
	// Only auto servers → nothing gated even with no mentions.
	if got := gatedMCPServerSet(map[string]string{"a": mcpModeAuto}, nil); got != nil {
		t.Fatalf("auto-only modes must gate nothing, got %v", got)
	}
}

func mcpGateTestDescriptors() []sdktools.ToolDescriptor {
	return []sdktools.ToolDescriptor{
		{Name: "auto_query", Source: "auto-srv", SourceCategory: sdktools.SourceCategoryMCP, Group: sdktools.GroupRemoteMCP},
		{Name: "manual_query", Source: "manual-srv", SourceCategory: sdktools.SourceCategoryMCP, Group: sdktools.GroupRemoteMCP},
		{Name: "stale_query", Source: "disabled-srv", SourceCategory: sdktools.SourceCategoryMCP, Group: sdktools.GroupLocalMCP},
		{Name: "read_file", Source: "core", Group: sdktools.GroupLocalRead},
	}
}

func TestFilterGatedMCPTools(t *testing.T) {
	gated := map[string]bool{"manual-srv": true, "disabled-srv": true}
	got := filterGatedMCPTools(mcpGateTestDescriptors(), gated)
	names := make(map[string]bool, len(got))
	for _, d := range got {
		names[d.Name] = true
	}
	for _, want := range []string{"auto_query", "read_file"} {
		if !names[want] {
			t.Errorf("tool %q must survive the filter, got %v", want, names)
		}
	}
	for _, unwanted := range []string{"manual_query", "stale_query"} {
		if names[unwanted] {
			t.Errorf("tool %q must be filtered out (gated-off server), got %v", unwanted, names)
		}
	}
	// Empty gated set is a passthrough.
	if got := filterGatedMCPTools(mcpGateTestDescriptors(), nil); len(got) != 4 {
		t.Fatalf("nil gated set must be a passthrough, got %d tools", len(got))
	}
}

func TestApplyMCPModeGating(t *testing.T) {
	o := &Orchestrator{config: OrchestratorConfig{MCPServerModes: map[string]string{
		"auto-srv":     mcpModeAuto,
		"manual-srv":   mcpModeManual,
		"disabled-srv": mcpModeDisabled,
	}}}
	ctx := context.Background()

	// Without a mention: manual + disabled tools filtered, gated set in ctx.
	got, gatedCtx := o.applyMCPModeGating(ctx, mcpGateTestDescriptors(), nil)
	if len(got) != 2 {
		t.Fatalf("unmentioned run: want 2 surviving tools, got %d", len(got))
	}
	if g := coretools.GatedMCPServersFromContext(gatedCtx); g == nil || !g["manual-srv"] || !g["disabled-srv"] {
		t.Fatalf("gated set must carry manual+disabled servers, got %v", g)
	}

	// With a mention: the manual server survives; only disabled stays gated.
	got, gatedCtx = o.applyMCPModeGating(ctx, mcpGateTestDescriptors(), []string{"manual-srv"})
	names := make(map[string]bool, len(got))
	for _, d := range got {
		names[d.Name] = true
	}
	if !names["manual_query"] || names["stale_query"] {
		t.Fatalf("mentioned run: want manual_query present and stale_query absent, got %v", names)
	}
	if g := coretools.GatedMCPServersFromContext(gatedCtx); g == nil || !g["disabled-srv"] || g["manual-srv"] {
		t.Fatalf("mentioned run: gated set must be {disabled-srv}, got %v", g)
	}

	// Nothing gated (all auto) → list and ctx pass through unchanged.
	oAll := &Orchestrator{config: OrchestratorConfig{MCPServerModes: map[string]string{"auto-srv": mcpModeAuto}}}
	got, gatedCtx = oAll.applyMCPModeGating(ctx, mcpGateTestDescriptors(), nil)
	if len(got) != 4 || gatedCtx != ctx {
		t.Fatalf("auto-only config must be a no-op (got %d tools, ctx changed=%v)", len(got), gatedCtx != ctx)
	}
}

func TestRecordAndRecallMCPMentions(t *testing.T) {
	o := &Orchestrator{}
	o.recordMCPMentions("task-1", []string{"srv-b", "srv-a"})
	o.recordMCPMentions("task-1", []string{"srv-a", "srv-c"}) // union, no retraction

	got := o.mcpMentionsFor("task-1")
	if len(got) != 3 || got[0] != "srv-a" || got[1] != "srv-b" || got[2] != "srv-c" {
		t.Fatalf("mentions must union task-wide and sort, got %v", got)
	}
	if other := o.mcpMentionsFor("task-2"); other != nil {
		t.Fatalf("task isolation broken: task-2 saw %v", other)
	}
	o.recordMCPMentions("", []string{"srv"}) // no-op on empty task id
	if got := o.mcpMentionsFor(""); got != nil {
		t.Fatalf("empty task id must yield nil, got %v", got)
	}
}

// TestVerifierToolset_InheritsMCPGating pins the verifier composition: the
// goal verifier's toolset is built (verifierToolFilter) from the orchestrator's
// ALREADY-GATED available-tool list, so a manual server without a mention (or
// a disabled server) must not reach the verifier either.
func TestVerifierToolset_InheritsMCPGating(t *testing.T) {
	gated := map[string]bool{"manual-srv": true, "disabled-srv": true}
	filtered := filterGatedMCPTools(mcpGateTestDescriptors(), gated)
	got := verifierToolFilter(filtered, nil)
	names := make(map[string]bool, len(got))
	for _, d := range got {
		names[d.Name] = true
	}
	if names["manual_query"] || names["stale_query"] {
		t.Fatalf("gated-off MCP tools must not reach the verifier, got %v", names)
	}
	if !names["auto_query"] {
		t.Fatalf("auto MCP tools must stay available to the verifier, got %v", names)
	}
}
