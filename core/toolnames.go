package core

// Tool name constants used across core to refer to specific built-in or MCP
// tools by name. Centralizing these prevents drift and silent typos when the
// planner or step configurator references a tool string in multiple files.
//
// The values mirror the names registered by github.com/v0lka/sp4rk/tools/builtins.RegisterBuiltinTools
// and the internal-tool list in core/tools/registry.go.
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

// FileMutatingTools is the set of tool names that can modify files on disk.
// The PostExecuteHook uses this to decide whether to notify the vector index
// manager of a potential content change after tool execution. bash_exec is
// included because it can mutate files in ways the in-process write_file/
// edit_file tools cannot (e.g. sed, git checkout, build artifacts), and the
// macOS file watcher alone may miss some of those changes.
//
// Cost note: the resulting debounced incremental pass is NOT free even when no
// files changed — ValidateCollection reads and hashes every indexable file in
// the workspace (O(files) disk/CPU). The 1s debounce coalesces rapid bursts
// (e.g. a batch of edits) but NOT isolated bash_exec calls (each test run, git
// status, or build triggers one full sweep). For large repos this is non-
// trivial churn on a hot path. The tradeoff (freshness of search results after
// shell-driven changes) is considered worth the per-call O(files) sweep;
// revisit if profiling shows indexer pressure during heavy coding sessions.
var FileMutatingTools = map[string]bool{
	ToolWriteFile: true,
	ToolEditFile:  true,
	ToolBashExec:  true,
}

// NoProjectDisabledTools is the set of tool names that are blocked from both
// listing and execution when the current project is "No Project" (__no_project__).
//
// Only semantic_search is disabled: without a project there is no vector index
// (the subsystem stays off for No Project sessions), so a query would either
// fail or return stale results from the previous CODE-mode project. ripgrep
// and glob were historically disabled here as "code-oriented" tools, but CHAT
// sessions legitimately use them beyond code (OS config, DevOps, dotfiles),
// so they are now allowed under the normal security.groups policies.
//
// Note: edit_file and write_file are intentionally NOT in this list. In No Project
// (CHAT) mode, write/edit operations are constrained to the per-session isolated
// workspace or temp directory by the Judge layer (judgeWriteInSessionRoots). This
// enables editing arbitrary files within the session workspace (including
// .c0wrk/plans/ for Plan Review) without exposing the broader filesystem. CHAT
// mode was never strictly read-only — bash_exec has always been allowed for
// session-scoped command execution.
var NoProjectDisabledTools = map[string]bool{
	ToolSemanticSearch: true,
}
