package core

import (
	"context"
	"errors"
	"sort"
	"testing"

	"github.com/v0lka/c0wrk/core/smallllm"
	"github.com/v0lka/sp4rk/agent/router"
	"github.com/v0lka/sp4rk/llm"
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

// TestApplySmallLLMToolFilter_NilRoutingFallsBackToFullSet verifies the
// degradation guard: with no routing decision there are no usable matched
// tools, so the filter falls back to the FULL tool set instead of narrowing to
// the guaranteed-only (empty-match) set that would strip every file/exec tool
// from the Conductor. The fallback surfaces a diagnostic event and emits no
// ToolsAssigned (there is no curated set to show).
func TestApplySmallLLMToolFilter_NilRoutingFallsBackToFullSet(t *testing.T) {
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

	if want, have := sortedToolNames(in), sortedToolNames(got); !equalNames(want, have) {
		t.Errorf("nil routing should fall back to the full tool set: got %v, want %v", have, want)
	}
	assertNoToolsAssigned(t, spy)
	assertToolSelectionFallbackDiagnostic(t, spy)
}

// TestApplySmallLLMToolFilter_EmptyMatchedToolsFallsBackToFullSet covers the
// router answering matched_tools: [] (or a persisted decision with no matched
// tools): the task must keep the full tool set and continue, not run on the
// guaranteed-only set.
func TestApplySmallLLMToolFilter_EmptyMatchedToolsFallsBackToFullSet(t *testing.T) {
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

	routing := &router.RoutingDecision{Domain: "code", Complexity: 3, MatchedTools: []string{}}
	got := o.applySmallLLMToolFilter(in, routing)

	if want, have := sortedToolNames(in), sortedToolNames(got); !equalNames(want, have) {
		t.Errorf("empty matched_tools should fall back to the full tool set: got %v, want %v", have, want)
	}
	assertNoToolsAssigned(t, spy)
	assertToolSelectionFallbackDiagnostic(t, spy)
}

// TestApplySmallLLMToolFilter_UnregisteredMatchedToolsFallsBackToFullSet
// covers a routing LLM that hallucinates tool names: a non-empty match that
// refers to nothing registered is as unusable as no match at all.
func TestApplySmallLLMToolFilter_UnregisteredMatchedToolsFallsBackToFullSet(t *testing.T) {
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

	routing := &router.RoutingDecision{
		Domain:       "code",
		Complexity:   3,
		MatchedTools: []string{"totally_made_up_tool", "another_fake_one"},
	}
	got := o.applySmallLLMToolFilter(in, routing)

	if want, have := sortedToolNames(in), sortedToolNames(got); !equalNames(want, have) {
		t.Errorf("unregistered matched_tools should fall back to the full tool set: got %v, want %v", have, want)
	}
	assertNoToolsAssigned(t, spy)
	assertToolSelectionFallbackDiagnostic(t, spy)
}

// stubRouterCaller is an agent.LLMCaller that always answers with the same
// (deliberately unparseable) content, driving the router's built-in repair
// cycle to exhaustion.
type stubRouterCaller struct{ content string }

func (s *stubRouterCaller) Call(_ context.Context, _ llm.ChatRequest) (*llm.ChatResponse, error) {
	return &llm.ChatResponse{Message: llm.Message{Role: "assistant", Content: s.content}}, nil
}

// TestRouteAndActivateSkills_RoutingParseErrorFallsBackUnderSmallLLM covers
// the exhausted routing-JSON repair cycle: with semantic tool matching on, an
// unparseable routing decision must degrade to default routing (general /
// defaultResumeComplexity) instead of failing the task.
func TestRouteAndActivateSkills_RoutingParseErrorFallsBackUnderSmallLLM(t *testing.T) {
	spy := &spyEmitter{}
	o := &Orchestrator{
		router: router.New(&stubRouterCaller{content: "definitely not json <<<"}, router.Config{
			SystemPrompt:  "Tools: {{AVAILABLE-TOOLS}}\nMatching: {{TOOL-MATCHING}}",
			HistoryWindow: 5,
		}),
		config: OrchestratorConfig{SmallLLM: SmallLLMSettings{
			Enabled: true,
			EssentialTools: SmallLLMEssentialSettings{
				Enabled:       true,
				AlwaysPresent: []string{"read_file"},
			},
		}},
		emitter: spy,
	}
	o.router.SetToolMatching(true)

	_, routing, _, _, err := o.routeAndActivateSkills(
		context.Background(), "fix the failing test", HandleOptions{}, nil, smallLLMTestTools())
	if err != nil {
		t.Fatalf("routing parse error must not fail the task under small-LLM tool matching: %v", err)
	}
	if routing == nil || routing.Domain != "general" || routing.Complexity != defaultResumeComplexity {
		t.Errorf("expected default routing decision (general/%d), got %+v", defaultResumeComplexity, routing)
	}
	assertFallbackDiagnostic(t, spy, "routing_parse")
}

// TestRouteAndActivateSkills_RoutingParseErrorStillFailsWhenProfileOff is the
// no-regression guard: without the small-LLM profile the routing parse error
// keeps failing the task exactly as before.
func TestRouteAndActivateSkills_RoutingParseErrorStillFailsWhenProfileOff(t *testing.T) {
	o := &Orchestrator{
		router: router.New(&stubRouterCaller{content: "definitely not json <<<"}, router.Config{
			SystemPrompt:  "Tools: {{AVAILABLE-TOOLS}}\nMatching: {{TOOL-MATCHING}}",
			HistoryWindow: 5,
		}),
		config:  OrchestratorConfig{}, // profile off (default)
		emitter: &spyEmitter{},
	}

	_, _, _, _, err := o.routeAndActivateSkills(
		context.Background(), "fix the failing test", HandleOptions{}, nil, smallLLMTestTools())
	if err == nil {
		t.Fatal("routing parse error must still fail the task when the small-LLM profile is off")
	}
	if !errors.Is(err, router.ErrRoutingParse) {
		t.Errorf("error should wrap router.ErrRoutingParse, got: %v", err)
	}
}

func equalNames(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func assertNoToolsAssigned(t *testing.T, spy *spyEmitter) {
	t.Helper()
	for _, c := range spy.calls {
		if c.method == "ToolsAssigned" {
			t.Error("fallback must not emit ToolsAssigned; got one")
		}
	}
}

// assertToolSelectionFallbackDiagnostic verifies the tool-selection fallback
// surfaced a ServiceWithMeta diagnostic carrying fallback=small_llm_tool_match.
func assertToolSelectionFallbackDiagnostic(t *testing.T, spy *spyEmitter) {
	t.Helper()
	assertFallbackDiagnostic(t, spy, "small_llm_tool_match")
}

func assertFallbackDiagnostic(t *testing.T, spy *spyEmitter, fallback string) {
	t.Helper()
	for _, c := range spy.calls {
		if c.method != "ServiceWithMeta" {
			continue
		}
		if len(c.args) > 1 {
			if meta, ok := c.args[1].(map[string]any); ok && meta["fallback"] == fallback {
				return
			}
		}
	}
	t.Errorf("fallback must emit a ServiceWithMeta diagnostic with fallback=%s", fallback)
}

// TestApplySmallLLMToolFilter_FallbackStillCompactsDescriptions verifies that
// the selection-fallback path (empty/unregistered matched tools) still applies
// description compaction: the fallback ships the FULL tool set — the largest
// possible descriptor payload — so compact_descriptions must not be silently
// dropped exactly where it matters most. Unknown (MCP) tools keep their
// original descriptions.
func TestApplySmallLLMToolFilter_FallbackStillCompactsDescriptions(t *testing.T) {
	in := smallLLMTestTools()
	spy := &spyEmitter{}
	o := &Orchestrator{
		config: OrchestratorConfig{SmallLLM: SmallLLMSettings{
			Enabled: true,
			EssentialTools: SmallLLMEssentialSettings{
				Enabled:             true,
				AlwaysPresent:       []string{"read_file"},
				CompactDescriptions: true,
			},
		}},
		emitter: spy,
	}

	got := o.applySmallLLMToolFilter(in, &router.RoutingDecision{
		Domain:       "code",
		MatchedTools: []string{},
	})

	if len(got) != len(in) {
		t.Fatalf("fallback must keep the full tool set: got %d tools, want %d", len(got), len(in))
	}
	byName := map[string]string{}
	for _, d := range got {
		byName[d.Name] = d.Description
	}
	if want := smallllm.CompactDescription("read_file"); byName["read_file"] != want {
		t.Errorf("known builtin must carry its compact description on the fallback path: got %q, want %q", byName["read_file"], want)
	}
	if byName["mcp_linter"] != "" {
		t.Errorf("unknown (MCP) tool description must be untouched on the fallback path, got %q", byName["mcp_linter"])
	}
	assertToolSelectionFallbackDiagnostic(t, spy)
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

// TestApplySmallLLMToolFilter_MaxToolsCapPreservesProtectedAndMCP verifies
// that the never-trimmed guaranteed set (always-present ∪ protected ∪ MCP)
// survives a MaxTools budget that is smaller than itself: matched tools get
// zero free slots, but guaranteed tools are never dropped (validation rejects
// such configs up front; SelectTools is defense in depth).
func TestApplySmallLLMToolFilter_MaxToolsCapPreservesProtectedAndMCP(t *testing.T) {
	o := &Orchestrator{config: OrchestratorConfig{SmallLLM: SmallLLMSettings{
		Enabled: true,
		EssentialTools: SmallLLMEssentialSettings{
			Enabled:       true,
			AlwaysPresent: []string{"read_file", "write_file", "bash_exec", "web_search"},
			// budget (3) < guaranteed = always-present(4) + protected(2:
			// finish, store_fact) + MCP(1: mcp_linter) = 7 → zero free slots
			// for matched tools, and the guaranteed set is returned in full.
			MaxTools: 3,
		},
	}}}

	// A registered matched tool outside the guaranteed set ("grep") proves
	// the cap semantics: matched tools fill only the free slots (none here),
	// while the guaranteed set is returned in full.
	capped := append(smallLLMTestTools(), sdktools.ToolDescriptor{
		Name: "grep", SourceCategory: sdktools.SourceCategoryCore,
	})
	got := o.applySmallLLMToolFilter(
		capped, &router.RoutingDecision{Domain: router.DomainCode, MatchedTools: []string{"grep"}})
	names := sortedToolNames(got)

	// The entire guaranteed set survives the tight cap: protected (finish,
	// store_fact), MCP (mcp_linter), and the non-protected always-present
	// tools (read_file, write_file, bash_exec, web_search) alike.
	want := []string{
		"finish", "store_fact", "mcp_linter",
		"read_file", "write_file", "bash_exec", "web_search",
	}
	for _, keep := range want {
		if !containsToolName(names, keep) {
			t.Errorf("maxTools cap: guaranteed tool %q must never be trimmed; got %v", keep, names)
		}
	}
	if len(got) != len(want) {
		t.Errorf("maxTools cap: expected exactly the %d guaranteed tools (zero matched slots); got %d (%v)",
			len(want), len(got), names)
	}
	if containsToolName(names, "grep") {
		t.Error("maxTools cap: matched tool beyond the budget must be dropped")
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
