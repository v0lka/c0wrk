package core

// ToolProfiles defines the allowed tools per agent role.
// nil means all tools (no filtering). Empty slice means no tools.
var ToolProfiles = map[string][]string{
	"router":    {},
	"planner":   {"read_file", "list_directory", "ripgrep", "glob", "semantic_search"},
	"reflector": {"read_file", "list_directory", "ripgrep", "glob", "read_evidence", "semantic_search"},
	// "evaluator" — has its own filtering mechanism, don't override
	// "executor" — gets all tools (nil)
}
