package smallllm

import (
	"slices"
	"testing"

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
	// With a tight budget, protected + MCP survive and non-protected matched
	// tools are trimmed to fit the remaining slots.
	matched := []string{"read_file", "write_file", "edit_file", "bash_exec"}
	got := SelectTools(fullToolSet(), matched, nil, 9)
	// Protected base = finish, store_fact, search_facts, ask_user, update_checklist (5).
	// MCP = search_graph, get_code_snippet, mcp_linter (3).
	// budget = 9 - 8 = 1 slot left for non-protected matched tools.
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
	// Exactly one non-protected matched tool fits.
	nonProtectedKept := 0
	for _, n := range matched {
		if contains(names, n) {
			nonProtectedKept++
		}
	}
	if nonProtectedKept != 1 {
		t.Errorf("expected 1 non-protected matched tool after cap, got %d (%v)", nonProtectedKept, names)
	}
}

func TestSelectTools_MaxToolsBudgetExceededByProtectedAlone(t *testing.T) {
	// When protected + MCP alone exceed maxTools, they still all survive and
	// nothing else is kept.
	got := SelectTools(fullToolSet(), []string{"read_file", "write_file"}, nil, 2)
	// Protected (5) + MCP (3) = 8 > 2, so they all survive, matched tools dropped.
	if len(got) != 8 {
		t.Fatalf("expected 8 protected+MCP survivors (cap not applied to them); got %d (%v)", len(got), descriptorNames(got))
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
