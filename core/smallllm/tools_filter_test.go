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

func TestSelectTools_KeepsMatchedPlusProtectedPlusMCP(t *testing.T) {
	matched := []string{"read_file", "write_file", "bash_exec", "ripgrep"}
	got := SelectTools(fullToolSet(), matched, nil, 0)
	names := descriptorNames(got)

	// Matched names are kept.
	for _, n := range matched {
		if !contains(names, n) {
			t.Errorf("matched tool %q should be kept; got %v", n, names)
		}
	}
	// Protected tools kept even though not in matched.
	for _, n := range []string{"finish", "store_fact", "search_facts", "ask_user", "update_checklist"} {
		if !contains(names, n) {
			t.Errorf("protected tool %q should be kept; got %v", n, names)
		}
	}
	// MCP tools kept even though not in matched.
	for _, n := range []string{"search_graph", "get_code_snippet", "mcp_linter"} {
		if !contains(names, n) {
			t.Errorf("MCP tool %q should be kept; got %v", n, names)
		}
	}
}

func TestSelectTools_KeepsAlwaysPresent(t *testing.T) {
	// alwaysPresent lists a tool that is neither matched nor protected nor MCP,
	// yet it must survive because the user pinned it.
	got := SelectTools(fullToolSet(), []string{"read_file"}, []string{"create_directory"}, 0)
	names := descriptorNames(got)
	if !contains(names, "create_directory") {
		t.Errorf("alwaysPresent tool must be kept; got %v", names)
	}
}

func TestSelectTools_ExcludesOrchestrationTools(t *testing.T) {
	got := SelectTools(fullToolSet(), []string{"read_file"}, nil, 0)
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
	// Duplicate matched names + duplicate descriptors must not produce dupes.
	all := []sdktools.ToolDescriptor{
		{Name: "read_file", SourceCategory: sdktools.SourceCategoryCore},
		{Name: "read_file", SourceCategory: sdktools.SourceCategoryCore},
		{Name: "finish", SourceCategory: sdktools.SourceCategoryCore},
	}
	got := SelectTools(all, []string{"read_file", "read_file"}, nil, 0)
	names := descriptorNames(got)
	if len(names) != 2 {
		t.Errorf("expected 2 tools after dedup, got %d %v", len(names), names)
	}
}

func TestSelectTools_AlwaysPreservesFinishEvenWhenUnmatched(t *testing.T) {
	got := SelectTools(fullToolSet(), nil, nil, 0)
	names := descriptorNames(got)
	if !contains(names, finishToolName) {
		t.Errorf("finish must always be preserved; got %v", names)
	}
}

func TestSelectTools_EmptyInput(t *testing.T) {
	got := SelectTools(nil, []string{"read_file"}, []string{"read_file"}, 0)
	if len(got) != 0 {
		t.Errorf("nil input should yield empty output; got %v", descriptorNames(got))
	}
}

func TestSelectTools_PreservesAllMCPTools(t *testing.T) {
	// Even with empty matched/alwaysPresent, every MCP tool survives.
	got := SelectTools(fullToolSet(), nil, nil, 0)
	names := descriptorNames(got)
	for _, m := range []string{"search_graph", "get_code_snippet", "mcp_linter"} {
		if !contains(names, m) {
			t.Errorf("MCP tool %q must be preserved; got %v", m, names)
		}
	}
}

func TestSelectTools_MaxToolsZeroMeansUnlimited(t *testing.T) {
	// maxTools == 0 disables the cap; nothing is trimmed.
	matched := []string{"read_file", "write_file", "edit_file", "bash_exec"}
	got := SelectTools(fullToolSet(), matched, nil, 0)
	names := descriptorNames(got)
	for _, n := range matched {
		if !contains(names, n) {
			t.Errorf("with maxTools=0, matched %q must be kept; got %v", n, names)
		}
	}
}

func TestSelectTools_MaxToolsTrimsNonProtected(t *testing.T) {
	// With a tight budget, guaranteed (protected + MCP) tools survive and
	// non-protected matched tools fill the remaining slots in registry order.
	matched := []string{"read_file", "write_file", "edit_file", "bash_exec"}
	got := SelectTools(fullToolSet(), matched, nil, 9)
	// Guaranteed base = finish, store_fact, search_facts, ask_user,
	// update_checklist (5 protected) + search_graph, get_code_snippet,
	// mcp_linter (3 MCP) = 8, never trimmed.
	// Free slots = 9 - 8 = 1 → exactly one matched tool fits, and it is the
	// first one in REGISTRY order (read_file), regardless of the matched-list
	// order.
	if len(got) > 9 {
		t.Fatalf("result must not exceed maxTools=9; got %d (%v)", len(got), descriptorNames(got))
	}
	names := descriptorNames(got)
	// Protected + MCP always survive the cap.
	for _, n := range []string{
		"finish", "store_fact", "search_facts", "ask_user", "update_checklist",
		"search_graph", "get_code_snippet", "mcp_linter",
	} {
		if !contains(names, n) {
			t.Errorf("protected/MCP tool %q must survive the cap; got %v", n, names)
		}
	}
	// Exactly one non-protected matched tool fits: read_file (registry index 0).
	for _, n := range matched {
		kept := contains(names, n)
		if n == "read_file" && !kept {
			t.Errorf("the single free slot must go to read_file (first in registry order); got %v", names)
		}
		if n != "read_file" && kept {
			t.Errorf("matched %q must be dropped once free slots are exhausted; got %v", n, names)
		}
	}
}

func TestSelectTools_MaxToolsBudgetExceededByProtectedAlone(t *testing.T) {
	// When the guaranteed set alone exceeds maxTools, it is still returned in
	// full (guaranteed is never trimmed) and matched tools get zero slots.
	// Config validation rejects such profiles up front; SelectTools is the
	// runtime defense in depth.
	got := SelectTools(fullToolSet(), []string{"read_file", "write_file"}, nil, 2)
	// Guaranteed = protected (5) + MCP (3) = 8 > 2, so all 8 survive (the
	// result legitimately exceeds maxTools), matched tools are dropped.
	if len(got) != 8 {
		t.Fatalf("expected all 8 guaranteed survivors (never trimmed, even over budget); got %d (%v)", len(got), descriptorNames(got))
	}
	names := descriptorNames(got)
	for _, n := range []string{"read_file", "write_file"} {
		if contains(names, n) {
			t.Errorf("non-protected matched %q must be dropped when budget is exhausted by protected; got %v", n, names)
		}
	}
}

func TestSelectTools_MaxToolsNoTrimWhenUnderBudget(t *testing.T) {
	// A generous budget leaves the union unchanged.
	matched := []string{"read_file", "write_file"}
	got := SelectTools(fullToolSet(), matched, nil, 100)
	// Union = matched(2) + protected(5) + MCP(3) = 10.
	if len(got) != 10 {
		t.Errorf("under-budget result should be unchanged at 10; got %d (%v)", len(got), descriptorNames(got))
	}
}

func TestSelectTools_UnionDedupAcrossSources(t *testing.T) {
	// A tool that is BOTH matched and alwaysPresent is kept exactly once.
	got := SelectTools(fullToolSet(), []string{"read_file"}, []string{"read_file"}, 0)
	names := descriptorNames(got)
	count := 0
	for _, n := range names {
		if n == "read_file" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("read_file in both matched and alwaysPresent must appear once; got %d", count)
	}
}

// defaultAlwaysPresent mirrors backend/config.defaultSmallLLMAlwaysPresent.
// Duplicated here (rather than imported) because core must not depend on the
// backend layer; the backend test
// TestDefaultSmallLLMAlwaysPresentFitsDefaultMaxTools pins the default list
// against the shipped defaults.
func defaultAlwaysPresent() []string {
	return []string{
		"read_file", "write_file", "edit_file", "list_directory",
		"glob", "ripgrep", "bash_exec", "semantic_search",
		"store_fact", "search_facts", "ask_user", "finish",
	}
}

// coreToolSet is the registry subset with the MCP tools stripped, for tests
// that reason about slot arithmetic without runtime-dependent MCP counts.
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

func TestSelectTools_DefaultAlwaysPresentAtDefaultBudget(t *testing.T) {
	// The default always-present list (12) unioned with the 5 protected
	// tools (4 overlap → 13 unique guaranteed) must fit the default budget of
	// 16 with room left for router-matched slots. With no MCP tools
	// registered, free slots = 16 - 13 = 3, filled in registry order.
	matched := []string{"web_search", "web_fetch", "create_directory", "delegate"}
	got := SelectTools(coreToolSet(), matched, defaultAlwaysPresent(), 16)
	names := descriptorNames(got)

	// Every guaranteed tool is present: the 12 default always-present pins…
	for _, n := range defaultAlwaysPresent() {
		if !contains(names, n) {
			t.Errorf("default always-present %q must never be trimmed; got %v", n, names)
		}
	}
	// …plus update_checklist (protected, not among the default pins).
	if !contains(names, "update_checklist") {
		t.Errorf("protected update_checklist must always be present; got %v", names)
	}

	// The 3 free slots go to the first matched tools in registry order
	// (web_search, web_fetch, create_directory); delegate is out of luck, and
	// no unmatched orchestration tool leaks in.
	for _, n := range []string{"web_search", "web_fetch", "create_directory"} {
		if !contains(names, n) {
			t.Errorf("matched %q must fill a free slot (registry order); got %v", n, names)
		}
	}
	if contains(names, "delegate") {
		t.Errorf("matched delegate exceeds the free slots; got %v", names)
	}
	if contains(names, "reflect") || contains(names, "propose_goal") {
		t.Errorf("unmatched orchestration tools must stay excluded; got %v", names)
	}
	if len(got) != 16 {
		t.Errorf("expected 13 guaranteed + 3 matched = 16; got %d (%v)", len(got), names)
	}
}

func TestSelectTools_GuaranteedExceedingBudgetNeverTrimmed(t *testing.T) {
	// guaranteed > maxTools → SelectTools must NOT trim the guaranteed set.
	// validateSmallLLMConfig rejects such configs up front; this pins the
	// runtime behavior as defense in depth. Non-protected always-present
	// tools are guaranteed too and must survive.
	always := []string{"read_file", "write_file", "bash_exec", "web_search"} // 4, none protected
	got := SelectTools(fullToolSet(), []string{"semantic_search", "glob"}, always, 5)
	// Guaranteed = always(4) ∪ protected(5) ∪ MCP(3) = 12 > 5 → all 12 kept,
	// matched dropped, and the result legitimately exceeds the budget.
	if len(got) != 12 {
		t.Fatalf("guaranteed set of 12 must survive the budget of 5 intact; got %d (%v)", len(got), descriptorNames(got))
	}
	names := descriptorNames(got)
	for _, n := range always { // includes the non-protected web_search
		if !contains(names, n) {
			t.Errorf("always-present %q is guaranteed and must never be trimmed; got %v", n, names)
		}
	}
	for _, n := range ProtectedToolNames() {
		if !contains(names, n) {
			t.Errorf("protected %q must never be trimmed; got %v", n, names)
		}
	}
	if !contains(names, "mcp_linter") || !contains(names, "search_graph") || !contains(names, "get_code_snippet") {
		t.Errorf("MCP tools are guaranteed and must never be trimmed; got %v", names)
	}
	for _, n := range []string{"semantic_search", "glob"} {
		if contains(names, n) {
			t.Errorf("matched %q must get zero slots when guaranteed alone exceeds the budget; got %v", n, names)
		}
	}
}

func TestSelectTools_MatchedFillFreeSlotsInRegistryOrder(t *testing.T) {
	// Matched tools fill only the free slots left after the guaranteed set,
	// chosen deterministically by REGISTRY order — not by the order of the
	// matched slice — and repeated calls return the same result.
	all := coreToolSet()
	// Guaranteed = 5 protected tools → free slots = 6 - 5 = 1.
	matched := []string{"web_fetch", "read_file", "write_file"}
	got := SelectTools(all, matched, nil, 6)
	names := descriptorNames(got)
	if len(got) != 6 {
		t.Fatalf("expected 5 guaranteed + 1 matched = 6; got %d (%v)", len(got), names)
	}
	if !contains(names, "read_file") {
		t.Errorf("the single free slot must go to read_file (first matched in registry order); got %v", names)
	}
	for _, n := range []string{"web_fetch", "write_file"} {
		if contains(names, n) {
			t.Errorf("matched %q must be dropped once free slots are exhausted; got %v", n, names)
		}
	}
	// Deterministic regardless of matched-list order and across repetitions.
	again := SelectTools(all, []string{"write_file", "web_fetch", "read_file"}, nil, 6)
	if diff := cmp.Diff(names, descriptorNames(again)); diff != "" {
		t.Errorf("selection must be deterministic regardless of matched-list order:\n%s", diff)
	}
}
