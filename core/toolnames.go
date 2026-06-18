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

// NoProjectDisabledTools is the set of tool names that are blocked from both
// listing and execution when the current project is "No Project" (__no_project__).
var NoProjectDisabledTools = map[string]bool{
	ToolRipgrep:        true,
	ToolGlob:           true,
	ToolEditFile:       true,
	ToolSemanticSearch: true,
}

// NoProjectBashBlacklist contains regex patterns for commands blocked in
// No Project mode. These are development/build tools that only make sense
// inside a real project workspace.
var NoProjectBashBlacklist = []string{
	`^(git|npm|npx|yarn|pnpm|go|rustc|cargo|make|cmake|gcc|g\+\+|cc|clang)\b`,
	`^(pip|pip3|python|python3|gem|bundle|dotnet|msbuild|docker|kubectl)\b`,
	`^(helm|terraform|vagrant|ansible|gradle|mvn|sbt|stack|cabal)\b`,
	`^(nuget|choco|brew|port|apt|apt-get|yum|dnf|pacman|zypper|snap)\b`,
}
