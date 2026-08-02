package core

import (
	"sort"
	"testing"

	"github.com/v0lka/sp4rk/agent/router"
	sdktools "github.com/v0lka/sp4rk/tools"
)

// smallLLMTestTools is a compact tool set exercising matched, orchestration,
// protected, and MCP tools. Note: search_facts / ask_user / update_checklist
// (also protected) are intentionally ABSENT to prove the filter keeps only the
// protected tools that actually exist in the input.
func smallLLMTestTools() []sdktools.ToolDescriptor {
	return []sdktools.ToolDescriptor{
		{Name: "read_file", SourceCategory: sdktools.SourceCategoryCore},
		{Name: "write_file", SourceCategory: sdktools.SourceCategoryCore},
		{Name: "bash_exec", SourceCategory: sdktools.SourceCategoryCore},
		{Name: "web_search", SourceCategory: sdktools.SourceCategoryCore},
		{Name: "finish", SourceCategory: sdktools.SourceCategoryCore},
		{Name: "store_fact", SourceCategory: sdktools.SourceCategoryCore},
		{Name: "delegate", SourceCategory: sdktools.SourceCategoryCore},
		{Name: "declare_plan", SourceCategory: sdktools.SourceCategoryCore},
		{Name: "reflect", SourceCategory: sdktools.SourceCategoryCore},
		{Name: "mcp_linter", SourceCategory: sdktools.SourceCategoryMCP},
	}
}

// TestApplySmallLLMToolFilter_OffPassthrough verifies that when the profile is
// disabled (master toggle OR essential-tools variant), the tool set is returned
// UNTOUCHED — zero behavior change.
func TestApplySmallLLMToolFilter_OffPassthrough(t *testing.T) {
	in := smallLLMTestTools()

	// Master toggle off.
	o := &Orchestrator{config: OrchestratorConfig{SmallLLM: SmallLLMSettings{
		Enabled: false,
		EssentialTools: SmallLLMEssentialSettings{
			Enabled:       true,
			AlwaysPresent: []string{"read_file"},
		},
	}}}
	got := o.applySmallLLMToolFilter(in, &router.RoutingDecision{Domain: router.DomainCode})
	if len(got) != len(in) {
		t.Errorf("master OFF: expected %d tools (untouched), got %d", len(in), len(got))
	}

	// Essential-tools variant off.
	o2 := &Orchestrator{config: OrchestratorConfig{SmallLLM: SmallLLMSettings{
		Enabled: true,
		EssentialTools: SmallLLMEssentialSettings{
			Enabled: false,
		},
	}}}
	got2 := o2.applySmallLLMToolFilter(in, &router.RoutingDecision{Domain: router.DomainCode})
	if len(got2) != len(in) {
		t.Errorf("variant OFF: expected %d tools (untouched), got %d", len(in), len(got2))
	}
}

// TestApplySmallLLMToolFilter_SelectsMatchedAlwaysPresentProtectedAndMCP
// verifies the core SelectTools contract routed through the orchestrator: the
// kept set is the union of router-matched names (routing.MatchedTools), the
// always-present list, the protected orchestration tools, and every MCP tool —
// while conductor-only orchestration tools are dropped.
func TestApplySmallLLMToolFilter_SelectsMatchedAlwaysPresentProtectedAndMCP(t *testing.T) {
	in := smallLLMTestTools()
	o := &Orchestrator{config: OrchestratorConfig{SmallLLM: SmallLLMSettings{
		Enabled: true,
		EssentialTools: SmallLLMEssentialSettings{
			Enabled:       true,
			AlwaysPresent: []string{"web_search"},
		},
	}}}

	// Router matched file/bash tools; web_search comes from always-present.
	got := o.applySmallLLMToolFilter(in, &router.RoutingDecision{
		Domain:       router.DomainCode,
		MatchedTools: []string{"read_file", "write_file", "bash_exec"},
	})
	names := sortedToolNames(got)
	set := make(map[string]struct{}, len(names))
	for _, n := range names {
		set[n] = struct{}{}
	}

	// Matched + always-present + protected (finish, store_fact) + MCP all kept.
	for _, keep := range []string{"read_file", "write_file", "bash_exec", "web_search", "finish", "store_fact", "mcp_linter"} {
		if _, ok := set[keep]; !ok {
			t.Errorf("should keep %q; got %v", keep, names)
		}
	}
	// Conductor-only orchestration tools are dropped.
	for _, drop := range []string{"delegate", "declare_plan", "reflect"} {
		if _, ok := set[drop]; ok {
			t.Errorf("should drop orchestration tool %q; got %v", drop, names)
		}
	}
}

// TestApplySmallLLMToolFilter_NilRoutingNarrowsButEmitsNoEvent verifies that a
// nil routing decision still narrows the tool set (always-present ∪ protected ∪
// MCP) but does NOT emit a ToolsAssigned event — the event requires a routing
// decision to be present.
func TestApplySmallLLMToolFilter_NilRoutingNarrowsButEmitsNoEvent(t *testing.T) {
	in := smallLLMTestTools()
	spy := &spyEmitter{}
	o := &Orchestrator{
		config: OrchestratorConfig{SmallLLM: SmallLLMSettings{
			Enabled: true,
			EssentialTools: SmallLLMEssentialSettings{
				Enabled:       true,
				AlwaysPresent: []string{"read_file"},
			},
		}},
		emitter: spy,
	}

	got := o.applySmallLLMToolFilter(in, nil)
	names := sortedToolNames(got)

	// Narrowed: read_file (always-present) + protected (finish, store_fact) + MCP.
	for _, keep := range []string{"read_file", "finish", "store_fact", "mcp_linter"} {
		if !containsToolName(names, keep) {
			t.Errorf("nil routing: %q should survive; got %v", keep, names)
		}
	}
	// Non-kept tools dropped.
	for _, drop := range []string{"web_search", "delegate", "bash_exec"} {
		if containsToolName(names, drop) {
			t.Errorf("nil routing: %q should be dropped; got %v", drop, names)
		}
	}
	// No ToolsAssigned event with nil routing.
	for _, c := range spy.calls {
		if c.method == "ToolsAssigned" {
			t.Error("nil routing must not emit ToolsAssigned; got one")
		}
	}
}

// TestApplySmallLLMToolFilter_EmitsToolsAssignedWhenRoutingPresent verifies the
// ToolsAssigned event fires exactly once when filtering is active (master ON +
// essential ON + routing present) and carries the curated tool names, matching
// the filtered set.
func TestApplySmallLLMToolFilter_EmitsToolsAssignedWhenRoutingPresent(t *testing.T) {
	in := smallLLMTestTools()
	spy := &spyEmitter{}
	o := &Orchestrator{
		config: OrchestratorConfig{SmallLLM: SmallLLMSettings{
			Enabled: true,
			EssentialTools: SmallLLMEssentialSettings{
				Enabled:       true,
				AlwaysPresent: []string{"read_file"},
			},
		}},
		emitter: spy,
	}

	got := o.applySmallLLMToolFilter(in, &router.RoutingDecision{
		Domain:       router.DomainCode,
		MatchedTools: []string{"bash_exec"},
	})

	var emitted []string
	count := 0
	for _, c := range spy.calls {
		if c.method == "ToolsAssigned" {
			count++
			if len(c.args) > 0 {
				if names, ok := c.args[0].([]string); ok {
					emitted = names
				}
			}
		}
	}
	if count != 1 {
		t.Fatalf("expected exactly 1 ToolsAssigned event, got %d", count)
	}
	// The emitted names must match the filtered tool set.
	if len(emitted) != len(got) {
		t.Fatalf("emitted names len = %d, filtered = %d", len(emitted), len(got))
	}
	emittedSet := make(map[string]struct{}, len(emitted))
	for _, n := range emitted {
		emittedSet[n] = struct{}{}
	}
	for _, d := range got {
		if _, ok := emittedSet[d.Name]; !ok {
			t.Errorf("filtered tool %q not present in emitted event %v", d.Name, emitted)
		}
	}
}

// TestApplySmallLLMToolFilter_MaxToolsCapPreservesProtectedAndMCP verifies the
// MaxTools budget is enforced while protected tools and MCP tools always
// survive the cap (the loop must terminate and use user-installed tools).
func TestApplySmallLLMToolFilter_MaxToolsCapPreservesProtectedAndMCP(t *testing.T) {
	in := smallLLMTestTools()
	o := &Orchestrator{config: OrchestratorConfig{SmallLLM: SmallLLMSettings{
		Enabled: true,
		EssentialTools: SmallLLMEssentialSettings{
			Enabled:       true,
			AlwaysPresent: []string{"read_file", "write_file", "bash_exec", "web_search"},
			// budget (3) == protected(2) + MCP(1), so non-protected matched/
			// always-present tools are trimmed away entirely.
			MaxTools: 3,
		},
	}}}

	got := o.applySmallLLMToolFilter(in, &router.RoutingDecision{Domain: router.DomainCode})
	names := sortedToolNames(got)

	// Protected (finish, store_fact) + MCP (mcp_linter) survive even under a
	// tight cap that excludes the non-protected always-present tools.
	for _, keep := range []string{"finish", "store_fact", "mcp_linter"} {
		if !containsToolName(names, keep) {
			t.Errorf("maxTools cap: protected/MCP tool %q must survive; got %v", keep, names)
		}
	}
	if len(got) > 3 {
		t.Errorf("maxTools cap: result must not exceed budget 3; got %d (%v)", len(got), names)
	}
}

func sortedToolNames(descs []sdktools.ToolDescriptor) []string {
	names := make([]string, 0, len(descs))
	for _, d := range descs {
		names = append(names, d.Name)
	}
	sort.Strings(names)
	return names
}

func containsToolName(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
