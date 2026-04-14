package core

// ToolProfiles defines the allowed tools per agent role.
// nil means all tools (no filtering). Empty slice means no tools.
var ToolProfiles = map[string][]string{
	"router":    {},
	"planner":   {"read_file", "list_directory", "search_files", "search_content", "ripgrep", "glob"},
	"reflector": {"read_file", "list_directory", "search_files", "search_content", "ripgrep", "glob", "read_evidence"},
	// "evaluator" — has its own filtering mechanism, don't override
	// "executor" — gets all tools (nil)
}
