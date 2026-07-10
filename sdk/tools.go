package sp4rk

import (
	"github.com/v0lka/sp4rk/agent"
	"github.com/v0lka/sp4rk/tools"
	"github.com/v0lka/sp4rk/tools/builtins"
)

// FileTools returns the built-in workspace file tools: read_file, write_file,
// edit_file, list_directory, glob, and create_directory. These cover the
// common file-manipulation surface most agents need.
func FileTools() []tools.Tool {
	return []tools.Tool{
		builtins.NewReadFileTool(),
		builtins.NewWriteFileTool(),
		builtins.NewEditFileTool(),
		builtins.NewListDirectoryTool(),
		builtins.NewGlobTool(),
		builtins.NewCreateDirectoryTool(),
	}
}

// MemoryTools returns the blackboard fact-memory tools: store_fact and
// search_facts. Use these when steps need to share findings with later steps.
func MemoryTools() []tools.Tool {
	return []tools.Tool{
		builtins.NewStoreFactTool(),
		builtins.NewSearchFactsTool(),
	}
}

// CodeTools returns FileTools plus ripgrep, delete_file, and delete_directory
// — a complete code-editing bundle for agents that search and modify a
// codebase.
func CodeTools() []tools.Tool {
	return append(
		FileTools(),
		builtins.NewRipgrepTool(),
		builtins.NewDeleteFileTool(),
		builtins.NewDeleteDirectoryTool(),
	)
}

// FinishTool returns the [agent.FinishTool], which signals task completion.
// [NewF] auto-registers it by default; this helper is exposed for callers that
// build a [Config] directly (classic API) or disable auto-registration.
func FinishTool() []tools.Tool {
	return []tools.Tool{agent.NewFinishTool()}
}

// AllBuiltinTools returns every zero-configuration built-in tool (file, search,
// memory, and orchestration-readiness tools). Parameterized tools
// (bash_exec, vector_search, web_fetch) are excluded — construct those
// individually when needed.
func AllBuiltinTools() []tools.Tool {
	return []tools.Tool{
		// File & workspace
		builtins.NewReadFileTool(),
		builtins.NewWriteFileTool(),
		builtins.NewEditFileTool(),
		builtins.NewListDirectoryTool(),
		builtins.NewGlobTool(),
		builtins.NewCreateDirectoryTool(),
		builtins.NewDeleteFileTool(),
		builtins.NewDeleteDirectoryTool(),
		// Search
		builtins.NewRipgrepTool(),
		// Memory
		builtins.NewStoreFactTool(),
		builtins.NewSearchFactsTool(),
		// Orchestration support
		builtins.NewToolResultReadTool(),
		builtins.NewBatchTool(),
		builtins.NewUpdateChecklistTool(),
		builtins.NewReadStepOutputTool(),
		builtins.NewListStepOutputsTool(),
		builtins.NewReadFinalResultTool(),
	}
}

// Tools is a passthrough grouping helper that returns its arguments as a slice.
// Useful for combining bundles with custom tools in a single [WithTools] call:
//
//	sp4rk.WithTools(append(
//	    sp4rk.FileTools(),
//	    sp4rk.Tools(myCustomTool, anotherTool...)...,
//	)...)
func Tools(ts ...tools.Tool) []tools.Tool {
	return ts
}

// ─── FrameworkBuilder methods ───────────────────────────────────────────────

// FileTools registers the built-in workspace file tools bundle (read, write,
// edit, list, glob, mkdir).
func (b *FrameworkBuilder) FileTools() *FrameworkBuilder {
	b.opts.tools = append(b.opts.tools, FileTools()...)
	return b
}

// MemoryTools registers the blackboard fact-memory tools bundle (store_fact,
// search_facts).
func (b *FrameworkBuilder) MemoryTools() *FrameworkBuilder {
	b.opts.tools = append(b.opts.tools, MemoryTools()...)
	return b
}

// CodeTools registers FileTools plus ripgrep and delete helpers — a complete
// code-editing bundle for agents that search and modify a codebase.
func (b *FrameworkBuilder) CodeTools() *FrameworkBuilder {
	b.opts.tools = append(b.opts.tools, CodeTools()...)
	return b
}

// AllBuiltinTools registers every zero-configuration built-in tool (file,
// search, memory, and orchestration-readiness tools).
func (b *FrameworkBuilder) AllBuiltinTools() *FrameworkBuilder {
	b.opts.tools = append(b.opts.tools, AllBuiltinTools()...)
	return b
}

// Tools appends arbitrary tools to the auto-registration set. Use this to add
// custom tools or a pre-assembled slice alongside the bundles above.
func (b *FrameworkBuilder) Tools(ts ...tools.Tool) *FrameworkBuilder {
	b.opts.tools = append(b.opts.tools, ts...)
	return b
}
