package core

import (
	"testing"

	sdktools "github.com/v0lka/c0wrk/sdk/tools"
)

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
