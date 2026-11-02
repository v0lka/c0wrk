package core

// ToolProfiles defines the allowed tools per agent role:
// nil = all tools (no filtering), empty slice = no tools.
var ToolProfiles = map[string][]string{
	"router":    {},
	"planner":   {ToolReadFile, ToolListDirectory, ToolRipgrep, ToolGlob, ToolSemanticSearch},
	"reflector": {ToolReadFile, ToolListDirectory, ToolRipgrep, ToolGlob, ToolReadEvidence, ToolSemanticSearch},
	// "evaluator" filters via its own mechanism — don't override;
	// "executor" gets all tools (nil, absent here).
}
