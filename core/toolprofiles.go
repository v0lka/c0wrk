package core

import (
	tools "github.com/user/agent/sdk/tools"
)

// ToolProfiles defines the allowed tools per agent role.
// nil means all tools (no filtering). Empty slice means no tools.
var ToolProfiles = map[string][]string{
	"router":    {},
	"planner":   {"read_file", "list_directory", "search_files", "search_content", "ripgrep", "glob"},
	"reflector": {"read_file", "list_directory", "search_files", "search_content", "ripgrep", "glob", "read_evidence"},
	// "evaluator" — has its own filtering mechanism, don't override
	// "executor" — gets all tools (nil)
}

// FilterToolsByProfile filters a list of tool descriptors based on the allowed tool names.
// If allowedNames is nil, returns all tools (no filtering).
// If allowedNames is empty slice, returns empty (no tools).
func FilterToolsByProfile(allTools []tools.ToolDescriptor, allowedNames []string) []tools.ToolDescriptor {
	// nil means all tools (no filtering)
	if allowedNames == nil {
		return allTools
	}

	// empty slice means no tools
	if len(allowedNames) == 0 {
		return []tools.ToolDescriptor{}
	}

	// Build lookup set for allowed tools
	allowSet := make(map[string]bool, len(allowedNames))
	for _, name := range allowedNames {
		allowSet[name] = true
	}

	// Filter tools
	var filtered []tools.ToolDescriptor
	for _, tool := range allTools {
		if !allowSet[tool.Name] {
			continue
		}

		filtered = append(filtered, tool)
	}

	return filtered
}
