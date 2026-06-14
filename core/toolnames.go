package core

// Tool name constants used across core to refer to specific built-in or MCP
// tools by name. Centralizing these prevents drift and silent typos when the
// planner or step configurator references a tool string in multiple files.
//
// The values mirror the names registered by sdk/tools/builtins.RegisterBuiltinTools
// and the internal-tool list in core/tools/registry.go.
const (
	// Internal infrastructure tools (always allowed, bypass policy).
	ToolFinish         = "finish"
	ToolStoreFact      = "store_fact"
	ToolSearchFacts    = "search_facts"
	ToolAskUser        = "ask_user"
	ToolSetStepStatus  = "set_step_status"
	ToolReadStepOutput = "read_step_output"
	ToolListStepOutput = "list_step_outputs"
	ToolToolResultRead = "tool_result_read"
	ToolReadSkillRes   = "read_skill_resource"

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
	ToolSubAgent = "subagent"

	// Web tools.
	ToolWebSearch = "web_search"
	ToolWebFetch  = "web_fetch"
)
