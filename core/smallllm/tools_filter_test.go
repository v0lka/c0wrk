package smallllm

import (
	"slices"
	"testing"

	cmp "github.com/google/go-cmp/cmp"
	sdktools "github.com/v0lka/sp4rk/tools"
)

// descriptorNames extracts the sorted Name slice from a descriptor list so
// tests can compare against a stable, readable expectation.
func descriptorNames(descs []sdktools.ToolDescriptor) []string {
	names := make([]string, 0, len(descs))
	for _, d := range descs {
		names = append(names, d.Name)
	}
	slices.Sort(names)
	return names
}

// contains reports whether the sorted slice names contains want.
func contains(names []string, want string) bool {
	return slices.Contains(names, want)
}

// fullToolSet mirrors the conductor's full advertised tool list: read tools,
// mutating tools, internal meta-tools, orchestration tools, and MCP tools.
func fullToolSet() []sdktools.ToolDescriptor {
	return []sdktools.ToolDescriptor{
		// Read-only exploration.
		{Name: "read_file", SourceCategory: sdktools.SourceCategoryCore},
		{Name: "list_directory", SourceCategory: sdktools.SourceCategoryCore},
		{Name: "glob", SourceCategory: sdktools.SourceCategoryCore},
		{Name: "ripgrep", SourceCategory: sdktools.SourceCategoryCore},
		{Name: "semantic_search", SourceCategory: sdktools.SourceCategoryCore},
		{Name: "web_search", SourceCategory: sdktools.SourceCategoryCore},
		{Name: "web_fetch", SourceCategory: sdktools.SourceCategoryCore},
		// Mutating.
		{Name: "write_file", SourceCategory: sdktools.SourceCategoryCore},
		{Name: "edit_file", SourceCategory: sdktools.SourceCategoryCore},
		{Name: "bash_exec", SourceCategory: sdktools.SourceCategoryCore},
		{Name: "create_directory", SourceCategory: sdktools.SourceCategoryCore},
		// Internal meta / protected base.
		{Name: "finish", SourceCategory: sdktools.SourceCategoryCore},
		{Name: "store_fact", SourceCategory: sdktools.SourceCategoryCore},
		{Name: "search_facts", SourceCategory: sdktools.SourceCategoryCore},
		{Name: "ask_user", SourceCategory: sdktools.SourceCategoryCore},
		{Name: "update_checklist", SourceCategory: sdktools.SourceCategoryCore},
		// Orchestration tools (conductor-only / goal-mode) — must be excluded.
		{Name: "delegate", SourceCategory: sdktools.SourceCategoryCore},
		{Name: "declare_plan", SourceCategory: sdktools.SourceCategoryCore},
		{Name: "execute_plan", SourceCategory: sdktools.SourceCategoryCore},
		{Name: "reflect", SourceCategory: sdktools.SourceCategoryCore},
		{Name: "propose_goal", SourceCategory: sdktools.SourceCategoryCore},
		// MCP tools — must always survive.
		{Name: "search_graph", SourceCategory: sdktools.SourceCategoryMCP},
		{Name: "get_code_snippet", SourceCategory: sdktools.SourceCategoryMCP},
		{Name: "mcp_linter", SourceCategory: sdktools.SourceCategoryMCP},
	}
}

func TestSelectTools_StaticUnionOfPinsProtectedAndMCP(t *testing.T) {
	pins := []string{"read_file", "write_file", "bash_exec", "ripgrep"}
	got := SelectTools(fullToolSet(), pins)

	// The result is exactly pins ∪ protected ∪ MCP — nothing more (no
	// unpinned core tools, no orchestration tools), nothing less.
	want := []string{
		"ask_user", "bash_exec", "finish", "get_code_snippet",
		"mcp_linter", "read_file", "ripgrep", "search_facts",
		"search_graph", "store_fact", "update_checklist", "write_file",
	}
	if diff := cmp.Diff(want, descriptorNames(got)); diff != "" {
		t.Errorf("static selection must be exactly pins ∪ protected ∪ MCP:\n%s", diff)
	}
}

func TestSelectTools_KeepsAlwaysPresent(t *testing.T) {
	// alwaysPresent lists a tool that is neither protected nor MCP, yet it
	// must survive because the user pinned it.
	got := SelectTools(fullToolSet(), []string{"create_directory"})
	names := descriptorNames(got)
	if !contains(names, "create_directory") {
		t.Errorf("alwaysPresent tool must be kept; got %v", names)
	}
}

func TestSelectTools_ExcludesOrchestrationTools(t *testing.T) {
	got := SelectTools(fullToolSet(), []string{"read_file"})
	names := descriptorNames(got)

	excluded := []string{
		"delegate", "declare_plan", "execute_plan", "reflect",
		"propose_goal",
	}
	for _, ex := range excluded {
		if contains(names, ex) {
			t.Errorf("orchestration tool %q should be excluded, but was kept", ex)
		}
	}
}

func TestSelectTools_DedupsByName(t *testing.T) {
	// Duplicate pins + duplicate descriptors must not produce dupes.
	all := []sdktools.ToolDescriptor{
		{Name: "read_file", SourceCategory: sdktools.SourceCategoryCore},
		{Name: "read_file", SourceCategory: sdktools.SourceCategoryCore},
		{Name: "finish", SourceCategory: sdktools.SourceCategoryCore},
	}
	got := SelectTools(all, []string{"read_file", "read_file"})
	names := descriptorNames(got)
	if len(names) != 2 {
		t.Errorf("expected 2 tools after dedup, got %d %v", len(names), names)
	}
}

func TestSelectTools_AlwaysPreservesFinishEvenWhenUnpinned(t *testing.T) {
	got := SelectTools(fullToolSet(), nil)
	names := descriptorNames(got)
	if !contains(names, finishToolName) {
		t.Errorf("finish must always be preserved; got %v", names)
	}
}

func TestSelectTools_EmptyInput(t *testing.T) {
	got := SelectTools(nil, []string{"read_file"})
	if len(got) != 0 {
		t.Errorf("nil input should yield empty output; got %v", descriptorNames(got))
	}
}

func TestSelectTools_PreservesAllMCPTools(t *testing.T) {
	// Even with empty pins, every MCP tool survives.
	got := SelectTools(fullToolSet(), nil)
	names := descriptorNames(got)
	for _, m := range []string{"search_graph", "get_code_snippet", "mcp_linter"} {
		if !contains(names, m) {
			t.Errorf("MCP tool %q must be preserved; got %v", m, names)
		}
	}
}

func TestSelectTools_UnionDedupAcrossSources(t *testing.T) {
	// A tool that is BOTH pinned and protected is kept exactly once.
	got := SelectTools(fullToolSet(), []string{"finish"})
	names := descriptorNames(got)
	count := 0
	for _, n := range names {
		if n == "finish" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("finish in both pins and protected must appear once; got %d", count)
	}
}

// defaultAlwaysPresent mirrors backend/config.defaultSmallLLMAlwaysPresent.
// Duplicated here (rather than imported) because core must not depend on the
// backend layer; the backend tests pin the default list against the shipped
// defaults.
func defaultAlwaysPresent() []string {
	return []string{
		"read_file", "write_file", "edit_file", "list_directory",
		"glob", "ripgrep", "bash_exec", "semantic_search",
		"store_fact", "search_facts", "ask_user", "finish",
	}
}

// coreToolSet is the registry subset with the MCP tools stripped, for tests
// that reason about the static selection without runtime-dependent MCP counts.
func coreToolSet() []sdktools.ToolDescriptor {
	all := fullToolSet()
	out := make([]sdktools.ToolDescriptor, 0, len(all))
	for _, d := range all {
		if d.SourceCategory != sdktools.SourceCategoryMCP {
			out = append(out, d)
		}
	}
	return out
}

func TestSelectTools_DefaultAlwaysPresentSelection(t *testing.T) {
	// The default always-present list (12) unioned with the 5 protected
	// tools (4 overlap → 13 unique). Unpinned core tools and orchestration
	// tools stay excluded.
	got := SelectTools(coreToolSet(), defaultAlwaysPresent())
	names := descriptorNames(got)

	// Every default pin is present…
	for _, n := range defaultAlwaysPresent() {
		if !contains(names, n) {
			t.Errorf("default always-present %q must never be dropped; got %v", n, names)
		}
	}
	// …plus update_checklist (protected, not among the default pins).
	if !contains(names, "update_checklist") {
		t.Errorf("protected update_checklist must always be present; got %v", names)
	}

	// Unpinned core tools and orchestration tools stay excluded.
	for _, ex := range []string{"web_search", "web_fetch", "create_directory", "delegate", "reflect", "propose_goal"} {
		if contains(names, ex) {
			t.Errorf("unpinned tool %q must stay excluded; got %v", ex, names)
		}
	}
	if len(got) != 13 {
		t.Errorf("expected 12 pins ∪ 5 protected = 13 unique tools; got %d (%v)", len(got), names)
	}
}

func TestSelectTools_RegistryOrderPreserved(t *testing.T) {
	// Emission preserves the input registry order (not sorted, not
	// pin-list order) and repeated calls return the same result.
	got := SelectTools(coreToolSet(), defaultAlwaysPresent())
	want := []string{
		"read_file", "list_directory", "glob", "ripgrep",
		"semantic_search", "write_file", "edit_file", "bash_exec",
		"finish", "store_fact", "search_facts", "ask_user",
		"update_checklist",
	}
	names := make([]string, 0, len(got))
	for _, d := range got {
		names = append(names, d.Name)
	}
	if diff := cmp.Diff(want, names); diff != "" {
		t.Errorf("selection must preserve registry order:\n%s", diff)
	}
	again := SelectTools(coreToolSet(), defaultAlwaysPresent())
	againNames := make([]string, 0, len(again))
	for _, d := range again {
		againNames = append(againNames, d.Name)
	}
	if diff := cmp.Diff(names, againNames); diff != "" {
		t.Errorf("selection must be deterministic across calls:\n%s", diff)
	}
}

func TestSelectTools_ExtraGuaranteedKeepsUnmatchedDelegate(t *testing.T) {
	// The turn requested subagents: delegate is passed as extra-guaranteed
	// and must survive the narrowing even though it is neither pinned, nor
	// protected, nor MCP-sourced.
	got := SelectTools(fullToolSet(), []string{"read_file"}, "delegate")
	names := descriptorNames(got)
	if !contains(names, "delegate") {
		t.Errorf("extra-guaranteed delegate must be kept even when unpinned; got %v", names)
	}
	if !contains(names, "read_file") {
		t.Errorf("pinned tool must still be kept; got %v", names)
	}
	// Only the requested guarantee opens the door: unrelated orchestration
	// tools stay excluded.
	for _, ex := range []string{"declare_plan", "execute_plan", "reflect", "propose_goal"} {
		if contains(names, ex) {
			t.Errorf("unrelated orchestration tool %q must stay excluded; got %v", ex, names)
		}
	}
}

func TestSelectTools_NoExtraGuaranteedIsNoOp(t *testing.T) {
	// Zero or nil extra names must reproduce the selection exactly.
	pins := []string{"read_file", "write_file"}
	base := SelectTools(fullToolSet(), pins)
	var nilExtra []string
	withNilSpread := SelectTools(fullToolSet(), pins, nilExtra...)
	if diff := cmp.Diff(descriptorNames(base), descriptorNames(withNilSpread)); diff != "" {
		t.Errorf("nil extraGuaranteed must be a no-op:\n%s", diff)
	}
	// In particular delegate (conductor-only) stays excluded without the
	// turn-scoped guarantee.
	if names := descriptorNames(base); contains(names, "delegate") {
		t.Errorf("delegate must stay excluded without extraGuaranteed; got %v", names)
	}
}

func TestSelectTools_ExtraGuaranteedDedupsWithPinned(t *testing.T) {
	// A tool that is both pinned and extra-guaranteed appears exactly once.
	got := SelectTools(fullToolSet(), []string{"read_file", "delegate"}, "delegate")
	count := 0
	for _, d := range got {
		if d.Name == "delegate" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("delegate must appear exactly once; got %d (%v)", count, descriptorNames(got))
	}

	// An extra-guaranteed name matching nothing registered is a silent no-op:
	// a guarantee cannot invent a tool.
	ghost := SelectTools(fullToolSet(), []string{"read_file"}, "no_such_tool")
	if names := descriptorNames(ghost); contains(names, "no_such_tool") {
		t.Errorf("unregistered extra-guaranteed name must not invent a tool; got %v", names)
	}
}
