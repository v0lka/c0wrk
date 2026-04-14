package tools

// FilterToolsByProfile filters a list of tool descriptors based on the allowed tool names.
// If allowedNames is nil, returns all tools (no filtering).
// If allowedNames is empty slice, returns empty (no tools).
func FilterToolsByProfile(allTools []ToolDescriptor, allowedNames []string) []ToolDescriptor {
	// nil means all tools (no filtering)
	if allowedNames == nil {
		return allTools
	}

	// empty slice means no tools
	if len(allowedNames) == 0 {
		return []ToolDescriptor{}
	}

	// Build lookup set for allowed tools
	allowSet := make(map[string]bool, len(allowedNames))
	for _, name := range allowedNames {
		allowSet[name] = true
	}

	// Filter tools
	var filtered []ToolDescriptor
	for _, tool := range allTools {
		if !allowSet[tool.Name] {
			continue
		}

		filtered = append(filtered, tool)
	}

	return filtered
}
