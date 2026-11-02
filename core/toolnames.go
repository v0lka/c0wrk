package core

// Tool name constants used across core to refer to built-in or MCP tools by
// name — one definition prevents drift and silent typos. The values mirror
// sp4rk's RegisterBuiltinTools and core/tools/registry.go's internal list.
const (
	// Internal infrastructure tools (always allowed, bypass policy).
	ToolFinish              = "finish"
	ToolStoreFact           = "store_fact"
	ToolSearchFacts         = "search_facts"
	ToolAskUser             = "ask_user"
	ToolUpdateChecklist     = "update_checklist"
	ToolDeclareStepComplete = "declare_step_complete"
	ToolReadStepOutput      = "read_step_output"
	ToolListStepOutput      = "list_step_outputs"
	ToolReadFinalResult     = "read_final_result"
	ToolToolResultRead      = "tool_result_read"
	ToolReadSkillRes        = "read_skill_resource"
	ToolBatch               = "batch"

	// Read-only file/code exploration tools.
	ToolReadFile       = "read_file"
	ToolListDirectory  = "list_directory"
	ToolRipgrep        = "ripgrep"
	ToolGlob           = "glob"
	ToolSemanticSearch = "semantic_search"
	ToolReadEvidence   = "read_evidence"
	ToolSearchGraph    = "search_graph"

	// Mutating file tools.
	ToolWriteFile = "write_file"
	ToolEditFile  = "edit_file"

	// Execution tools.
	ToolBashExec = "bash_exec"
	ToolPoshExec = "posh_exec"
	ToolSubAgent = "subagent"

	// Web tools.
	ToolWebSearch = "web_search"
	ToolWebFetch  = "web_fetch"
)

// FileMutatingTools is the set of tool names that can modify files on disk; the
// PostExecuteHook uses it to notify the vector index manager. The shell tool is
// included: it mutates files in ways the in-process write/edit tools cannot
// (sed, git checkout, build artifacts). Cost: each sweep is O(files) even with
// no changes and the 1s debounce coalesces bursts but not isolated shell calls —
// accepted for search freshness; revisit on indexer pressure.
// See specs/domains/workspace.md (file-change trigger set).
var FileMutatingTools = map[string]bool{
	ToolWriteFile: true,
	ToolEditFile:  true,
	ToolBashExec:  true,
	ToolPoshExec:  true,
}

// NoProjectDisabledTools is blocked from listing and execution in No Project
// (CHAT) mode. Only semantic_search: no project, no vector index — a query
// would fail or return the previous CODE project's stale results (ripgrep/glob
// stay allowed under normal group policies). write/edit are NOT listed — the
// Judge constrains their targets to the session workspace (judgeWriteInSessionRoots);
// CHAT was never strictly read-only. See specs/domains/orchestration/conductor.md (Lifecycle).
var NoProjectDisabledTools = map[string]bool{
	ToolSemanticSearch: true,
}
