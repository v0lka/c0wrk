package core

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/v0lka/c0wrk/core/tools"
	sdktools "github.com/v0lka/sp4rk/tools"
)

// subagentToolSet builds a representative registry descriptor list spanning
// read tools, mutating tools, internal meta-tools, and MCP tools.
func subagentToolSet() []sdktools.ToolDescriptor {
	return []sdktools.ToolDescriptor{
		{Name: "read_file", SourceCategory: sdktools.SourceCategoryCore},
		{Name: "list_directory", SourceCategory: sdktools.SourceCategoryCore},
		{Name: "glob", SourceCategory: sdktools.SourceCategoryCore},
		{Name: "ripgrep", SourceCategory: sdktools.SourceCategoryCore},
		{Name: "semantic_search", SourceCategory: sdktools.SourceCategoryCore},
		{Name: "web_search", SourceCategory: sdktools.SourceCategoryCore},
		{Name: "read_skill_resource", SourceCategory: sdktools.SourceCategoryCore},
		{Name: "finish", SourceCategory: sdktools.SourceCategoryCore},
		{Name: "store_fact", SourceCategory: sdktools.SourceCategoryCore},
		{Name: "search_facts", SourceCategory: sdktools.SourceCategoryCore},
		{Name: "read_step_output", SourceCategory: sdktools.SourceCategoryCore},
		{Name: "tool_result_read", SourceCategory: sdktools.SourceCategoryCore},
		// Mutating tools the Conductor may grant.
		{Name: "edit_file", SourceCategory: sdktools.SourceCategoryCore},
		{Name: "write_file", SourceCategory: sdktools.SourceCategoryCore},
		{Name: "bash_exec", SourceCategory: sdktools.SourceCategoryCore},
		{Name: "create_directory", SourceCategory: sdktools.SourceCategoryCore},
		// MCP tools (must always be present for any subagent).
		{Name: "search_graph", SourceCategory: sdktools.SourceCategoryMCP},
		{Name: "get_code_snippet", SourceCategory: sdktools.SourceCategoryMCP},
		{Name: "mcp_write", SourceCategory: sdktools.SourceCategoryMCP},
	}
}

func descriptorNames(descs []sdktools.ToolDescriptor) map[string]struct{} {
	out := make(map[string]struct{}, len(descs))
	for _, d := range descs {
		out[d.Name] = struct{}{}
	}
	return out
}

// TestMandatorySubagentTools_CodeMode reproduces the session-413a8e0d bug: a
// subagent that received only the explicitly-named mutating tools lost
// read_file/list_directory and ALL MCP tools. The mandatory base must always
// include read + MCP + meta tools.
func TestMandatorySubagentTools_CodeMode(t *testing.T) {
	l := &conductorLauncher{} // CODE mode: nothing disabled
	got := l.mandatorySubagentTools(subagentToolSet())
	names := descriptorNames(got)

	// Read-only built-ins present.
	for _, n := range []string{"read_file", "list_directory", "glob", "ripgrep", "semantic_search", "web_search", "finish"} {
		if _, ok := names[n]; !ok {
			t.Errorf("mandatory base missing read/meta tool %q", n)
		}
	}
	// ALL MCP tools present (including a mutating-named MCP tool).
	for _, n := range []string{"search_graph", "get_code_snippet", "mcp_write"} {
		if _, ok := names[n]; !ok {
			t.Errorf("mandatory base missing MCP tool %q", n)
		}
	}
	// Mutating built-ins NOT in the base.
	for _, n := range []string{"edit_file", "write_file", "bash_exec", "create_directory"} {
		if _, ok := names[n]; ok {
			t.Errorf("mandatory base must not include mutating tool %q", n)
		}
	}
}

// TestMandatorySubagentTools_ChatMode verifies the read set shrinks for CHAT
// (No-Project) mode, where glob/ripgrep/semantic_search are disabled.
func TestMandatorySubagentTools_ChatMode(t *testing.T) {
	l := &conductorLauncher{deps: conductorDeps{
		disabledTools: map[string]bool{ToolGlob: true, ToolRipgrep: true, ToolSemanticSearch: true},
	}}
	got := l.mandatorySubagentTools(subagentToolSet())
	names := descriptorNames(got)

	// Disabled read tools excluded.
	for _, n := range []string{ToolGlob, ToolRipgrep, ToolSemanticSearch} {
		if _, ok := names[n]; ok {
			t.Errorf("CHAT mode must exclude disabled read tool %q", n)
		}
	}
	// Remaining read + all MCP still present.
	for _, n := range []string{"read_file", "list_directory", "finish", "search_graph", "get_code_snippet", "mcp_write"} {
		if _, ok := names[n]; !ok {
			t.Errorf("CHAT mode must keep read/MCP tool %q", n)
		}
	}
}

// TestResolveTaskTools_ExplicitListAddsReadAndMCP verifies that when the
// Conductor grants an explicit list of MUTATING tools, the subagent still
// receives every read + MCP tool on top (the original bug dropped them).
func TestResolveTaskTools_ExplicitListAddsReadAndMCP(t *testing.T) {
	l := &conductorLauncher{deps: conductorDeps{toolRegistry: newSubagentTestRegistry(subagentToolSet())}}
	task := tools.DelegationTask{Tools: []any{"edit_file", "bash_exec"}}
	got := l.resolveTaskTools(task)
	names := descriptorNames(got)

	// Conductor-granted mutating tools present.
	for _, n := range []string{"edit_file", "bash_exec"} {
		if _, ok := names[n]; !ok {
			t.Errorf("requested mutating tool %q missing", n)
		}
	}
	// Read + MCP base auto-added.
	for _, n := range []string{"read_file", "list_directory", "search_graph", "get_code_snippet", "finish"} {
		if _, ok := names[n]; !ok {
			t.Errorf("read/MCP tool %q must be auto-added to explicit list", n)
		}
	}
	// No duplicate tool names (DeepSeek rejects duplicates with HTTP 400).
	seen := make(map[string]int, len(got))
	for _, d := range got {
		seen[d.Name]++
	}
	for name, cnt := range seen {
		if cnt > 1 {
			t.Errorf("tool %q appears %d times; duplicates are rejected by the LLM", name, cnt)
		}
	}
}

// TestResolveTaskTools_ReadOnlyIncludesMCP verifies "read-only" delegation
// still includes MCP tools (per requirement: all read- AND MCP-tools for any
// subagent), and excludes mutating built-ins.
func TestResolveTaskTools_ReadOnlyIncludesMCP(t *testing.T) {
	l := &conductorLauncher{deps: conductorDeps{toolRegistry: newSubagentTestRegistry(subagentToolSet())}}
	task := tools.DelegationTask{Tools: "read-only"}
	got := l.resolveTaskTools(task)
	names := descriptorNames(got)

	for _, n := range []string{"read_file", "search_graph", "mcp_write", "finish"} {
		if _, ok := names[n]; !ok {
			t.Errorf("read-only delegation must include read/MCP tool %q", n)
		}
	}
	for _, n := range []string{"edit_file", "bash_exec"} {
		if _, ok := names[n]; ok {
			t.Errorf("read-only delegation must exclude mutating tool %q", n)
		}
	}
}

// TestResolveTaskTools_UnexpectedTypeReturnsSafeMinimum verifies that when
// DelegationTask.Tools carries an unrecognized type (not a string and not a
// []any of tool names), resolveTaskTools falls back to the safe minimum
// (read-only + MCP base) instead of granting the full mutating toolset.
// Granting everything would silently defeat the Conductor's tool-selection
// intent if an unknown shape ever reaches this code path.
func TestResolveTaskTools_UnexpectedTypeReturnsSafeMinimum(t *testing.T) {
	l := &conductorLauncher{deps: conductorDeps{toolRegistry: newSubagentTestRegistry(subagentToolSet())}}
	task := tools.DelegationTask{Tools: 42} // int is not a recognized tool-request type
	got := l.resolveTaskTools(task)
	names := descriptorNames(got)

	// Mutating built-ins must NOT be granted on an unknown request type.
	for _, n := range []string{"edit_file", "write_file", "bash_exec", "create_directory"} {
		if _, ok := names[n]; ok {
			t.Errorf("unexpected tool-request type must not grant mutating tool %q", n)
		}
	}
	// Safe minimum (read-only + MCP base) must still be present.
	for _, n := range []string{"read_file", "list_directory", "finish", "search_graph", "get_code_snippet"} {
		if _, ok := names[n]; !ok {
			t.Errorf("unexpected type must fall back to safe minimum (read/MCP tool %q missing)", n)
		}
	}
}

// TestResolveTaskTools_UnknownStringReturnsSafeMinimum verifies that a string
// tool request that is not "all"/""/"read-only" (e.g. a single tool name sent
// as a bare string, which the delegate schema does not document) falls back to
// the safe minimum instead of fail-opening to the full mutating toolset —
// symmetric with the unknown-type branch above.
func TestResolveTaskTools_UnknownStringReturnsSafeMinimum(t *testing.T) {
	l := &conductorLauncher{deps: conductorDeps{toolRegistry: newSubagentTestRegistry(subagentToolSet())}}
	task := tools.DelegationTask{Tools: "edit_file"} // bare string is not a documented tool request
	got := l.resolveTaskTools(task)
	names := descriptorNames(got)

	for _, n := range []string{"edit_file", "write_file", "bash_exec", "create_directory"} {
		if _, ok := names[n]; ok {
			t.Errorf("unknown string tool request must not grant mutating tool %q", n)
		}
	}
	for _, n := range []string{"read_file", "list_directory", "finish", "search_graph", "get_code_snippet"} {
		if _, ok := names[n]; !ok {
			t.Errorf("unknown string tool request must fall back to safe minimum (read/MCP tool %q missing)", n)
		}
	}
}

// mockSubagentTool is a minimal sdktools.Tool used only to populate a registry
// for conductor tool-resolution tests.
type mockSubagentTool struct {
	name string
}

func (m *mockSubagentTool) Name() string                                          { return m.name }
func (m *mockSubagentTool) Description() string                                   { return "mock" }
func (m *mockSubagentTool) InputSchema() json.RawMessage                          { return json.RawMessage(`{}`) }
func (m *mockSubagentTool) Execute(context.Context, json.RawMessage) (sdktools.ToolResult, error) {
	return sdktools.ToolResult{}, nil
}
func (m *mockSubagentTool) DefaultPolicy() sdktools.ToolPolicy { return sdktools.PolicyAlwaysAllow }
func (m *mockSubagentTool) IsUntrusted() bool                  { return false }

// newSubagentTestRegistry builds a real *sdktools.ToolRegistry whose List()
// reflects the given descriptor set (with the correct SourceCategory), so
// conductorLauncher.allToolDescriptors works without the full app wiring.
func newSubagentTestRegistry(descs []sdktools.ToolDescriptor) *sdktools.ToolRegistry {
	r := sdktools.NewToolRegistry()
	for _, d := range descs {
		source := "core"
		if d.SourceCategory == sdktools.SourceCategoryMCP {
			source = "mcp:test"
		}
		_ = r.RegisterWithSourceCategory(&mockSubagentTool{name: d.Name}, source, d.SourceCategory)
	}
	return r
}

func TestFilterToolsByName_NoDuplicateInternalTools(t *testing.T) {
	// Reproduces the session 40961b3c failure: the Conductor's delegate
	// call listed semantic_search and tool_result_read in tasks[].tools,
	// and filterToolsByName appended them again via the internal-tools
	// loop, producing duplicate tool names. DeepSeek then rejected the
	// subagent request with HTTP 400 "Tool names must be unique."
	all := []sdktools.ToolDescriptor{
		{Name: "list_directory"},
		{Name: "search_code"},
		{Name: "tool_result_read"},
		{Name: "glob"},
		{Name: "trace_path"},
		{Name: "get_code_snippet"},
		{Name: "bash_exec"},
		{Name: "semantic_search"},
		{Name: "read_file"},
		{Name: "ripgrep"},
		{Name: "search_graph"},
		{Name: "finish"},
		{Name: "store_fact"},
		{Name: "search_facts"},
		{Name: "read_step_output"},
		{Name: "read_final_result"},
		{Name: "update_checklist"},
		{Name: "declare_step_complete"},
		{Name: "ask_user"},
		{Name: "list_step_outputs"},
	}
	names := []string{
		"list_directory", "search_code", "tool_result_read", "glob",
		"trace_path", "get_code_snippet", "bash_exec", "semantic_search",
		"read_file", "ripgrep", "search_graph",
	}

	out := filterToolsByName(all, names)

	seen := make(map[string]int, len(out))
	for _, d := range out {
		seen[d.Name]++
	}
	for name, cnt := range seen {
		if cnt > 1 {
			t.Errorf("tool %q appears %d times in filtered output; duplicates are rejected by DeepSeek", name, cnt)
		}
	}
	if seen["semantic_search"] != 1 {
		t.Errorf("semantic_search should appear exactly once, got %d", seen["semantic_search"])
	}
	if seen["tool_result_read"] != 1 {
		t.Errorf("tool_result_read should appear exactly once, got %d", seen["tool_result_read"])
	}
	if seen["finish"] != 1 {
		t.Errorf("finish should be injected exactly once, got %d", seen["finish"])
	}
}

func TestFilterToolsByName_DuplicateNamesInInputDeduped(t *testing.T) {
	// Even if the LLM lists the same tool twice in tasks[].tools, the
	// result must contain it only once.
	all := []sdktools.ToolDescriptor{
		{Name: "bash_exec"},
		{Name: "semantic_search"},
		{Name: "finish"},
		{Name: "store_fact"},
		{Name: "search_facts"},
		{Name: "read_step_output"},
		{Name: "read_final_result"},
		{Name: "update_checklist"},
		{Name: "declare_step_complete"},
		{Name: "ask_user"},
		{Name: "tool_result_read"},
		{Name: "list_step_outputs"},
	}
	names := []string{"bash_exec", "semantic_search", "semantic_search", "bash_exec"}

	out := filterToolsByName(all, names)

	seen := make(map[string]int, len(out))
	for _, d := range out {
		seen[d.Name]++
	}
	if seen["bash_exec"] != 1 {
		t.Errorf("bash_exec should appear once, got %d", seen["bash_exec"])
	}
	if seen["semantic_search"] != 1 {
		t.Errorf("semantic_search should appear once, got %d", seen["semantic_search"])
	}
}
