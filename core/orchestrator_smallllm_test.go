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

// smallLLMTestTools is a compact tool set exercising pinned, orchestration,
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
	got := o.applySmallLLMToolFilter(in)
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
	got2 := o2.applySmallLLMToolFilter(in)
	if len(got2) != len(in) {
		t.Errorf("variant OFF: expected %d tools (untouched), got %d", len(in), len(got2))
	}
}

// TestApplySmallLLMToolFilter_StaticSelection verifies the core SelectTools
// contract routed through the orchestrator: the kept set is exactly the
// user's always-present pins ∪ the protected orchestration tools ∪ every MCP
// tool — no slot budget, no router matching — while unpinned core tools and
// conductor-only orchestration tools are dropped.
func TestApplySmallLLMToolFilter_StaticSelection(t *testing.T) {
	in := smallLLMTestTools()
	o := &Orchestrator{config: OrchestratorConfig{SmallLLM: SmallLLMSettings{
		Enabled: true,
		EssentialTools: SmallLLMEssentialSettings{
			Enabled:       true,
			AlwaysPresent: []string{"web_search"},
		},
	}}}

	got := o.applySmallLLMToolFilter(in)

	// Exactly pins ∪ protected (finish, store_fact) ∪ MCP: the unpinned core
	// tools (read_file, write_file, bash_exec) are dropped alongside the
	// conductor-only orchestration tools.
	want := []string{"finish", "mcp_linter", "store_fact", "web_search"}
	if have := sortedToolNames(got); !equalNames(want, have) {
		t.Errorf("static selection mismatch: got %v, want %v", have, want)
	}
}

// TestApplySmallLLMToolFilter_EmitsNoToolEvents verifies the UI contract: the
// narrowing is a silent, deterministic background step — it must not emit any
// service diagnostics or tool-assignment cards into the chat.
func TestApplySmallLLMToolFilter_EmitsNoToolEvents(t *testing.T) {
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

	_ = o.applySmallLLMToolFilter(smallLLMTestTools())

	if len(spy.calls) != 0 {
		t.Errorf("tool narrowing must emit no events; got %d", len(spy.calls))
	}
}

// stubRouterCaller is an agent.LLMCaller that always answers with the same
// (deliberately unparseable) content, driving the router's built-in repair
// cycle to exhaustion.
type stubRouterCaller struct{ content string }

func (s *stubRouterCaller) Call(_ context.Context, _ llm.ChatRequest) (*llm.ChatResponse, error) {
	return &llm.ChatResponse{Message: llm.Message{Role: "assistant", Content: s.content}}, nil
}

// TestRouteAndActivateSkills_RoutingParseErrorFallsBackUnderSmallLLM covers
// the exhausted routing-JSON repair cycle: with the small-LLM profile active,
// an unparseable routing decision must degrade to default routing (general /
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
	_, routing, _, _, err := o.routeAndActivateSkills(
		context.Background(), "fix the failing test", HandleOptions{}, nil, smallLLMTestTools())
	if err != nil {
		t.Fatalf("routing parse error must not fail the task under the small-LLM essential-tools narrowing: %v", err)
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

// TestApplySmallLLMToolFilter_CompactsDescriptions verifies that description
// compaction applies to the static selection: known builtins carry their
// compact one-liners while unknown (MCP) tools keep their original
// descriptions.
func TestApplySmallLLMToolFilter_CompactsDescriptions(t *testing.T) {
	in := smallLLMTestTools()
	o := &Orchestrator{config: OrchestratorConfig{SmallLLM: SmallLLMSettings{
		Enabled: true,
		EssentialTools: SmallLLMEssentialSettings{
			Enabled:             true,
			AlwaysPresent:       []string{"read_file"},
			CompactDescriptions: true,
		},
	}}}

	got := o.applySmallLLMToolFilter(in)

	byName := map[string]string{}
	for _, d := range got {
		byName[d.Name] = d.Description
	}
	if want := smallllm.CompactDescription("read_file"); byName["read_file"] != want {
		t.Errorf("known builtin must carry its compact description: got %q, want %q", byName["read_file"], want)
	}
	if byName["mcp_linter"] != "" {
		t.Errorf("unknown (MCP) tool description must be untouched, got %q", byName["mcp_linter"])
	}
}

// TestApplySmallLLMToolFilter_RequestedAgentsGuaranteeDelegate verifies the
// turn-scoped delegate guarantee: with narrowing active and an explicit
// #agent mention threaded into the context (enrichAgentContext →
// WithUserAgents), the delegate tool survives the filter even though it is
// neither pinned, nor protected, nor MCP-sourced — the "## Requested
// Subagents" directive in the Conductor's prompt must never reference a tool
// the model cannot call. The call mirrors the production wiring in
// HandleMessage's task flow exactly.
func TestApplySmallLLMToolFilter_RequestedAgentsGuaranteeDelegate(t *testing.T) {
	in := smallLLMTestTools()
	o := &Orchestrator{config: OrchestratorConfig{SmallLLM: SmallLLMSettings{
		Enabled: true,
		EssentialTools: SmallLLMEssentialSettings{
			Enabled:       true,
			AlwaysPresent: []string{"read_file"},
		},
	}}}

	ctx := WithUserAgents(context.Background(), []string{"x"})
	got := o.applySmallLLMToolFilter(in, smallLLMAgentGuaranteedTools(ctx)...)
	names := sortedToolNames(got)

	if !containsToolName(names, "delegate") {
		t.Errorf("requested subagents must guarantee delegate in the narrowed set; got %v", names)
	}
	// Only delegate is turn-guaranteed: unrelated orchestration tools stay
	// excluded.
	for _, drop := range []string{"declare_plan", "reflect"} {
		if containsToolName(names, drop) {
			t.Errorf("only delegate is turn-guaranteed; %q must stay excluded; got %v", drop, names)
		}
	}
}

// TestApplySmallLLMToolFilter_NoRequestedAgentsExcludesDelegate is the
// no-regression guard for the turn-scoped guarantee: without explicit
// #mentions the helper yields no extra guarantees, so the filtered set is
// identical to the extra-free call and delegate stays a conductor-only,
// excluded tool.
func TestApplySmallLLMToolFilter_NoRequestedAgentsExcludesDelegate(t *testing.T) {
	in := smallLLMTestTools()
	o := &Orchestrator{config: OrchestratorConfig{SmallLLM: SmallLLMSettings{
		Enabled: true,
		EssentialTools: SmallLLMEssentialSettings{
			Enabled:       true,
			AlwaysPresent: []string{"read_file"},
		},
	}}}

	if got := smallLLMAgentGuaranteedTools(context.Background()); got != nil {
		t.Errorf("no requested agents must yield no extra guarantees, got %v", got)
	}

	base := o.applySmallLLMToolFilter(in)
	withCtx := o.applySmallLLMToolFilter(in, smallLLMAgentGuaranteedTools(context.Background())...)
	if !equalNames(sortedToolNames(base), sortedToolNames(withCtx)) {
		t.Errorf("empty UserAgents must not change the filtered set: got %v, want %v",
			sortedToolNames(withCtx), sortedToolNames(base))
	}
	if containsToolName(sortedToolNames(withCtx), "delegate") {
		t.Error("without requested agents delegate must stay excluded")
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
