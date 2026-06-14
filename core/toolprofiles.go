package core

// ToolProfiles defines the allowed tools per agent role.
// nil means all tools (no filtering). Empty slice means no tools.
var ToolProfiles = map[string][]string{
	"router":    {},
	"planner":   {ToolReadFile, ToolListDirectory, ToolRipgrep, ToolGlob, ToolSemanticSearch},
	"reflector": {ToolReadFile, ToolListDirectory, ToolRipgrep, ToolGlob, ToolReadEvidence, ToolSemanticSearch},
	// "evaluator" — has its own filtering mechanism, don't override
	// "executor" — gets all tools (nil)
}
