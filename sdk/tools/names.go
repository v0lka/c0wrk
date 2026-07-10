package tools

// Tool name constants for built-in tools. These mirror the names used by
// github.com/v0lka/sp4rk/tools/builtins during registration and are consumed by the executor
// for tool-specific behavior (truncation hints, caching, etc.).
const (
	ToolReadFile  = "read_file"
	ToolWriteFile = "write_file"
	ToolEditFile  = "edit_file"
	ToolRipgrep   = "ripgrep"
	ToolGrep      = "grep"
	ToolGlob      = "glob"
	ToolWebFetch  = "web_fetch"
	ToolBashExec  = "bash_exec"
	ToolPoshExec  = "posh_exec"
	ToolBatch     = "batch"
)
